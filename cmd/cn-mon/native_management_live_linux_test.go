// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build native && linux

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/target"
)

func TestNativeCommandHistoryManagementLiveWriterProtection(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "live-writer.db")
	logPath := filepath.Join(directory, "live-writer.log")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build live writer request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1")
	command.Dir = directory
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start live history writer: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = command.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			t.Error("live writer did not exit after cleanup kill")
		}
	}()
	active, err := waitNativeManagementRun(t, database, request.Key, 20*time.Second, func(run history.RunInfo) bool {
		return run.Status == "running" && run.Rounds >= 1
	})
	if err != nil {
		t.Fatalf("wait for live writer's persisted round: %v", err)
	}
	hostname := mustManagementHostname(t)
	if active.Host != hostname || history.LocalPIDState(active.PID) != history.PIDAlive {
		t.Fatalf("native active owner evidence host=%q pid=%d pid-state=%v", active.Host, active.PID, history.LocalPIDState(active.PID))
	}
	seedNativeManagementSibling(t, database, request, port)
	forceNativeManagementHeartbeatStale(t, database, active.ID)
	stale, err := waitNativeManagementRun(t, database, request.Key, 3*time.Second, func(run history.RunInfo) bool {
		return run.ID == active.ID && run.Status == "running" && run.Rounds >= 1
	})
	if err != nil || history.LocalPIDState(stale.PID) != history.PIDAlive || history.RunState(stale, time.Now().UTC(), hostname, history.LocalPIDState) != "lost" {
		t.Fatalf("stale writer evidence row=%+v state=%q pid-state=%v err=%v", stale, history.RunState(stale, time.Now().UTC(), hostname, history.LocalPIDState), history.LocalPIDState(stale.PID), err)
	}
	code, deleteStdout, deleteStderr := runNativeHistoryManagementCommand(t, binary, directory, database, logPath,
		"--delete-request", request.Key[:8], "--format", "json")
	if code != 3 || len(deleteStdout) != 0 || !bytes.Contains(deleteStderr, []byte("active or cannot be safely verified")) {
		t.Fatalf("mixed-request deletion while writer is alive exit=%d stdout=%q stderr=%q", code, deleteStdout, deleteStderr)
	}
	assertNativeManagementHistoryHasRuns(t, database, request.Key, 2)
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("gracefully stop live history writer: %v", err)
	}
	select {
	case childErr := <-waitDone:
		waited = true
		if code := childExitCode(childErr); code != 0 && code != 3 {
			t.Fatalf("live writer stop exit=%d err=%v stdout=%q stderr=%q", code, childErr, stdout.String(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("live writer did not stop within ten seconds")
	}
	ended, err := waitNativeManagementRun(t, database, request.Key, 5*time.Second, func(run history.RunInfo) bool {
		return run.ID != "ended-sibling" && run.Status != "running" && run.Rounds >= 1
	})
	if err != nil {
		t.Fatalf("wait for gracefully stopped writer history: %v", err)
	}
	if history.LocalPIDState(active.PID) != history.PIDDead {
		t.Fatalf("stopped writer PID %d is still reported alive", active.PID)
	}
	code, deleteStdout, deleteStderr = runNativeHistoryManagementCommand(t, binary, directory, database, logPath,
		"--delete-request", request.Key[:8], "--format", "json")
	if code != 0 {
		t.Fatalf("delete mixed request after writer stopped exit=%d stderr=%q", code, deleteStderr)
	}
	var report struct {
		Committed bool `json:"committed"`
		Deleted   struct {
			Requests int64 `json:"requests"`
			Runs     int64 `json:"runs"`
			Rounds   int64 `json:"rounds"`
			Results  int64 `json:"results"`
		} `json:"deleted"`
	}
	if err := json.Unmarshal(deleteStdout, &report); err != nil || !report.Committed || report.Deleted.Requests != 1 || report.Deleted.Runs != 2 || report.Deleted.Rounds != ended.Rounds+1 || report.Deleted.Results != ended.Rounds+1 {
		t.Fatalf("post-writer deletion report=%+v err=%v stdout=%q", report, err, deleteStdout)
	}
	if ctx.Err() != nil {
		t.Fatalf("live writer scenario exceeded its deadline: %v", ctx.Err())
	}
	t.Log("CNMON_NATIVE_COMMAND_HISTORY_MANAGEMENT live-writer=protected stale-display=lost pid=alive mixed-request=blocked-then-deleted stopped-pid=dead persisted-round=verified")
}

func forceNativeManagementHeartbeatStale(t *testing.T, database, runID string) {
	t.Helper()
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(1000)")
	db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: database, RawQuery: query.Encode()}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "UPDATE run SET heartbeat=? WHERE id=?", time.Now().Add(-24*time.Hour).Unix(), runID); err != nil {
		t.Fatalf("make live writer heartbeat stale: %v", err)
	}
}

func mustManagementHostname(t *testing.T) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return hostname
}
