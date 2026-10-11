// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build native && linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeCommandHistoryManagementLockedCancellation(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "locked-delete.db")
	logPath := filepath.Join(directory, "locked-delete.log")
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	request, runID := seedNativeManagementRun(t, database, "192.0.2.94", "locked-delete", hostname, int64(os.Getpid()), "finished")
	releaseLock := acquireNativeWriteLock(t, database)
	lockReleased := false
	defer func() {
		if !lockReleased {
			releaseLock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--delete-run="+runID, "--format", "json", "--history-file", database, "--log-file", logPath)
	command.Dir = directory
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start locked run deletion: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	waited := false
	defer func() {
		if waited {
			return
		}
		select {
		case <-waitDone:
			waited = true
		default:
			_ = command.Process.Kill()
			select {
			case <-waitDone:
				waited = true
			case <-time.After(2 * time.Second):
				t.Error("locked deletion child did not exit after kill")
			}
		}
	}()
	if err := waitForManagementLogLine(logPath, "starting delete-run operation", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// The external IMMEDIATE transaction prevents commit; allow the child to
	// reach the mutation while still keeping this wait well inside SQLite's
	// busy timeout.
	time.Sleep(250 * time.Millisecond)
	select {
	case err := <-waitDone:
		waited = true
		t.Fatalf("delete child exited before cancellation with held write lock: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	default:
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send Ctrl+C to locked deletion: %v", err)
	}
	select {
	case err := <-waitDone:
		waited = true
		if childExitCode(err) != 130 {
			t.Fatalf("locked deletion exit=%d err=%v stdout=%q stderr=%q, want 130", childExitCode(err), err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("locked deletion did not honor Ctrl+C within five seconds")
	}
	if ctx.Err() != nil {
		t.Fatalf("locked deletion command exceeded its deadline: %v", ctx.Err())
	}
	releaseLock()
	lockReleased = true
	assertNativeManagementHistoryHasRuns(t, database, request.Key, 1)
	t.Log("CNMON_NATIVE_COMMAND_HISTORY_MANAGEMENT locked-delete=ctrlc exit=130 committed=false history=unchanged")
}

func waitForManagementLogLine(path, marker string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), marker) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("timed out waiting for management child to log operation startup")
}
