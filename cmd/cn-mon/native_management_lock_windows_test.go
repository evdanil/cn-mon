// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build native && windows

package main

import (
	"context"
	"database/sql"
	"os"
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
	lockDB, err := sql.Open("sqlite3", windowsSQLiteURI(database))
	if err != nil {
		t.Fatal(err)
	}
	lockDB.SetMaxOpenConns(1)
	defer lockDB.Close()
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("hold SQLite write lock: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = lockConn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	command := startWindowsCommand(t, binary, directory, database, logPath,
		[]string{"--delete-run=" + runID, "--format", "json", "--history-file", database, "--log-file", logPath})
	defer command.killIfRunning()
	if err := waitForWindowsManagementLogLine(logPath, "starting delete-run operation", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	select {
	case <-command.done:
		result, err := command.waitFor(time.Second)
		t.Fatalf("delete child exited before Ctrl+C with held write lock: err=%v stdout=%q stderr=%q", err, result.stdout, result.stderr)
	default:
	}
	if err := sendPrivateConsoleCtrlC(command.cmd.Process.Pid); err != nil {
		t.Fatalf("send Ctrl+C to locked deletion: %v", err)
	}
	result, err := command.waitFor(5 * time.Second)
	if code := exitCode(err); code != 130 {
		t.Fatalf("locked deletion exit=%d err=%v stdout=%q stderr=%q, want 130", code, err, result.stdout, result.stderr)
	}
	if _, err := lockConn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatalf("release SQLite write lock: %v", err)
	}
	locked = false
	assertNativeManagementHistoryHasRuns(t, database, request.Key, 1)
	t.Log("CNMON_NATIVE_COMMAND_HISTORY_MANAGEMENT locked-delete=ctrlc exit=130 committed=false history=unchanged")
}

func waitForWindowsManagementLogLine(path, marker string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), marker) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return os.ErrDeadlineExceeded
}
