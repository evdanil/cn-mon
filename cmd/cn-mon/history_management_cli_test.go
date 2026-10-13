// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestCLIClearMissingHistoryIsSuccessfulAndDoesNotCreateDatabase(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "missing history.db")
	logPath := filepath.Join(directory, "management.log")
	code, stdout, stderr := runHistoryManagementProcess(t, directory,
		"--clear-history", "--history-file", database, "--log-file", logPath, "--format", "json")
	if code != 0 {
		t.Fatalf("clear missing history exit=%d, want 0; stderr=%q", code, stderr)
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
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatalf("decode missing-history clear report: %v; stdout=%q", err, stdout)
	}
	if report.Committed || report.Deleted != (struct {
		Requests int64 `json:"requests"`
		Runs     int64 `json:"runs"`
		Rounds   int64 `json:"rounds"`
		Results  int64 `json:"results"`
	}{}) {
		t.Fatalf("missing-history clear result=%+v, want zero counts and committed=false", report)
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear missing history created database: stat error=%v", err)
	}
}

func TestCLIClearMissingHistoryWriteFailureDoesNotClaimCommit(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "missing.db")
	logPath := filepath.Join(directory, "management.log")
	cfg := settings{HistoryFile: database, LogFile: logPath, LogLevel: "INFO", Theme: "default"}
	logger, err := openLogger(cfg, directory)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	logFailure := func(err error) {
		if err != nil {
			commandNote(nil, &stderr, "logging write failed: "+err.Error())
		}
	}
	code := runHistoryManagementCLI(cliOptions{Mode: modeClearHistory, Format: "json"}, failingWriter{}, &stderr,
		cfg, logger, logFailure, context.Background(), nil, commandRuntime{})
	if code != 3 || !strings.Contains(stderr.String(), "write deletion report:") || strings.Contains(stderr.String(), "committed") {
		t.Fatalf("missing clear output failure code=%d stderr=%q, want noncommitted report error", code, stderr.String())
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed missing-clear report created history: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("injected output failure") }

func runHistoryManagementProcess(t *testing.T, directory string, args ...string) (int, []byte, []byte) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCLIProcessHelper$")
	command.Dir = directory
	command.Env = envWithout(os.Environ(), "HOME", "USERPROFILE", "CNMON_TEST_ARGS")
	command.Env = append(command.Env, "HOME="+directory, "USERPROFILE="+directory, "CNMON_TEST_ARGS="+string(encoded))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("start management CLI helper: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return code, stdout.Bytes(), stderr.Bytes()
}

func TestCLIManagementModeRejectsTargetOptionsBeforeStartup(t *testing.T) {
	for _, extra := range [][]string{{"--file", "-"}, {"--tcp", "22"}, {"--every", "10s"}, {"--for", "10s"}, {"--since", "1d"}, {"--history-days", "3"}, {"127.0.0.1"}} {
		t.Run(strings.Join(extra, "_"), func(t *testing.T) {
			directory := t.TempDir()
			database := filepath.Join(directory, "must-not-open.db")
			logPath := filepath.Join(directory, "must-not-open.log")
			args := append([]string{"--clear-history", "--history-file", database, "--log-file", logPath}, extra...)
			code, stdout, stderr := runHistoryManagementProcess(t, directory, args...)
			if code != 2 || len(stdout) != 0 || len(stderr) == 0 {
				t.Fatalf("invalid management combination code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			for _, path := range []string{database, logPath} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("invalid management command created %q: %v", path, err)
				}
			}
		})
	}
}

func TestParseHistoryManagementModesAndIdentifiers(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		mode commandMode
		id   string
		bad  bool
	}{
		{name: "list runs prefix", args: []string{"--list-runs", "aBcD"}, mode: modeListRuns, id: "aBcD"},
		{name: "delete exact unicode run id", args: []string{"--delete-run=Case/Δ"}, mode: modeDeleteRun, id: "Case/Δ"},
		{name: "delete request prefix", args: []string{"--delete-request", strings.Repeat("f", 64)}, mode: modeDeleteRequest, id: strings.Repeat("f", 64)},
		{name: "clear", args: []string{"--clear-history"}, mode: modeClearHistory},
		{name: "empty run id", args: []string{"--delete-run="}, bad: true},
		{name: "invalid UTF8 run id", args: []string{"--delete-run=" + string([]byte{0xff})}, bad: true},
		{name: "short request prefix", args: []string{"--list-runs", "abc"}, bad: true},
		{name: "mixed management modes", args: []string{"--clear-history", "--delete-run", "one"}, bad: true},
		{name: "management and reader mode", args: []string{"--list-runs", "abcd", "--show", "abcd"}, bad: true},
		{name: "management since", args: []string{"--clear-history", "--since", "1d"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCLI(tc.args, time.Now().UTC())
			if tc.bad {
				if err == nil {
					t.Fatalf("parseCLI(%q) unexpectedly succeeded: %+v", tc.args, got)
				}
				return
			}
			if err != nil || got.Mode != tc.mode || got.ID != tc.id {
				t.Fatalf("parseCLI(%q) = mode=%v id=%q err=%v, want mode=%v id=%q", tc.args, got.Mode, got.ID, err, tc.mode, tc.id)
			}
		})
	}
}

func TestHistoryManagementErrorCodesRequirePureSelectionErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "wrapped missing", err: fmt.Errorf("read: %w", history.ErrNotFound), want: 1},
		{name: "wrapped ambiguous", err: fmt.Errorf("select: %w", history.ErrAmbiguousRequest), want: 2},
		{name: "missing plus cleanup", err: errors.Join(history.ErrNotFound, errors.New("rollback failed")), want: 3},
		{name: "ambiguous plus cleanup", err: fmt.Errorf("view: %w", errors.Join(history.ErrAmbiguousRequest, errors.New("close failed"))), want: 3},
		{name: "mixed selections", err: errors.Join(history.ErrNotFound, history.ErrAmbiguousRequest), want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := historyManagementErrorCode(test.err); got != test.want {
				t.Fatalf("historyManagementErrorCode(%v)=%d, want %d", test.err, got, test.want)
			}
		})
	}
}

func TestCLIListAndDeleteRunPreserveExactUTF8RunID(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "management history.db")
	logPath := filepath.Join(directory, "management.log")
	request, runID := seedManagementHistoryRun(t, database, "Case/Δ run")

	code, stdout, stderr := runHistoryManagementProcess(t, directory, "--list-runs", request.Key[:8],
		"--history-file", database, "--log-file", logPath, "--format", "json")
	if code != 0 {
		t.Fatalf("list runs exit=%d, want 0; stderr=%q", code, stderr)
	}
	var list struct {
		Runs []struct {
			ID        string  `json:"id"`
			RequestID string  `json:"request_id"`
			Status    string  `json:"status"`
			State     string  `json:"state"`
			Started   string  `json:"started"`
			Ended     *string `json:"ended"`
			Host      string  `json:"host"`
			PID       int64   `json:"pid"`
			Rounds    int64   `json:"rounds"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		t.Fatalf("decode run list: %v; stdout=%q", err, stdout)
	}
	if len(list.Runs) != 1 {
		t.Fatalf("run list rows=%d, want one: %s", len(list.Runs), stdout)
	}
	row := list.Runs[0]
	if row.ID != runID || row.RequestID != request.Key || row.Status != "finished" || row.State != "finished" || row.Started == "" || row.Ended == nil || row.Host == "" || row.PID <= 0 || row.Rounds != 1 {
		t.Fatalf("run list row=%+v, want exact UTF-8 ID/full request key/completed metadata", row)
	}

	code, stdout, stderr = runHistoryManagementProcess(t, directory, "--delete-run=case/Δ run",
		"--history-file", database, "--log-file", logPath, "--format", "json")
	if code != 1 || len(stdout) != 0 || len(stderr) == 0 {
		t.Fatalf("case-changed run deletion code=%d stdout=%q stderr=%q, want no-match/1 without report", code, stdout, stderr)
	}
	code, stdout, stderr = runHistoryManagementProcess(t, directory, "--delete-run="+runID,
		"--history-file", database, "--log-file", logPath, "--format", "json")
	if code != 0 {
		t.Fatalf("exact run deletion exit=%d, want 0; stderr=%q", code, stderr)
	}
	var deleted struct {
		Committed bool `json:"committed"`
		Deleted   struct {
			Requests int64 `json:"requests"`
			Runs     int64 `json:"runs"`
			Rounds   int64 `json:"rounds"`
			Results  int64 `json:"results"`
		} `json:"deleted"`
	}
	if err := json.Unmarshal(stdout, &deleted); err != nil {
		t.Fatalf("decode delete-run report: %v stdout=%q", err, stdout)
	}
	if !deleted.Committed || deleted.Deleted.Requests != 1 || deleted.Deleted.Runs != 1 || deleted.Deleted.Rounds != 1 || deleted.Deleted.Results != 1 {
		t.Fatalf("delete-run result=%+v, want committed cascade counts 1/1/1/1", deleted)
	}
}

func seedManagementHistoryRun(t *testing.T, database, runID string) (target.Request, string) {
	t.Helper()
	request, rejected, err := target.Build([]string{"192.0.2.77"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build management fixture request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	store, err := history.OpenWriter(context.Background(), database)
	if err != nil {
		t.Fatalf("open management fixture DB: %v", err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("resolve management fixture hostname: %v", err)
	}
	_, err = store.BeginRun(context.Background(), history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports},
		Hosts:       request.Hosts,
	}, history.RunStart{ID: runID, Started: started, Host: hostname, PID: int64(os.Getpid()), Heartbeat: started})
	if err != nil {
		_ = store.Close()
		t.Fatalf("begin management fixture run: %v", err)
	}
	_, err = store.RecordRound(context.Background(), runID, history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: "192.0.2.77", Result: "OK", Time: started, TCP: map[int]string{},
	}}})
	if err != nil {
		_ = store.Close()
		t.Fatalf("record management fixture round: %v", err)
	}
	if err := store.EndRun(context.Background(), runID, started.Add(time.Second), "finished"); err != nil {
		_ = store.Close()
		t.Fatalf("finish management fixture run: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close management fixture DB: %v", err)
	}
	return request, runID
}
