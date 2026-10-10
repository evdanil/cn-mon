// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/probe"
	"github.com/evdanil/gotest/internal/target"
)

type commandTestProber struct {
	resolveErr error
	results    []probe.Result
	probeErr   error
}

func (p commandTestProber) Resolve(_ context.Context, hosts []string) ([]probe.Resolved, error) {
	if p.resolveErr != nil {
		return nil, p.resolveErr
	}
	resolved := make([]probe.Resolved, 0, len(hosts))
	for index, host := range hosts {
		address, err := netip.ParseAddr(host)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, probe.Resolved{Index: index, Host: host, Address: address})
	}
	return resolved, nil
}

func (p commandTestProber) ProbeBatch(_ context.Context, targets []probe.Resolved, ports []int) ([]probe.Result, error) {
	if p.probeErr != nil {
		return nil, p.probeErr
	}
	if p.results != nil {
		return append([]probe.Result(nil), p.results...), nil
	}
	results := make([]probe.Result, 0, len(targets))
	for _, destination := range targets {
		tcp := make(map[int]probe.TCPResult, len(ports))
		for _, port := range ports {
			tcp[port] = probe.TCPResult{State: "open"}
		}
		results = append(results, probe.Result{Target: destination,
			ICMP: probe.ICMPResult{State: "OK", Sent: 1, Received: 1}, TCP: tcp})
	}
	return results, nil
}

func TestCommandUnexpectedProbeFailureKeepsHistoricalJSONAndLogsError(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	request, _, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedCommandHistory(t, database, request)

	var stdout, stderr bytes.Buffer
	runtime := commandTestRuntime(root, logPath, monitor.RunDeps{
		Prober: commandTestProber{resolveErr: errors.New("unexpected resolver failure")},
		OpenHistory: func(ctx context.Context, path string) (monitor.History, error) {
			return history.OpenWriter(ctx, path)
		},
	})
	code := runCLIWithRuntime([]string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--log-level", "ERROR", "127.0.0.1"}, nil, &stdout, &stderr, runtime)
	if code != 3 {
		t.Fatalf("unexpected resolver failure exit=%d stderr=%q", code, stderr.String())
	}
	var report struct {
		Summary []struct {
			EarlierRounds int `json:"earlier_rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode retained report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if len(report.Summary) != 1 || report.Summary[0].EarlierRounds != 1 {
		t.Fatalf("unexpected failure discarded valid history outcome: %+v", report.Summary)
	}
	log := readCommandLog(t, logPath)
	if !strings.Contains(log, "ERROR - target run failed") || strings.Contains(log, "INFO - target run finished") {
		t.Fatalf("unexpected run failure log at ERROR level: %q", log)
	}
}

func TestCommandHistoryOpenFailureKeepsCompletedJSONAndLogsError(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "unavailable.db"), filepath.Join(root, "cn.log")
	var stdout, stderr bytes.Buffer
	runtime := commandTestRuntime(root, logPath, monitor.RunDeps{
		Prober: commandTestProber{},
		OpenHistory: func(context.Context, string) (monitor.History, error) {
			return nil, errors.New("history is unavailable")
		},
	})
	code := runCLIWithRuntime([]string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--log-level", "INFO", "--tcp", "443", "127.0.0.1"}, nil, &stdout, &stderr, runtime)
	if code != 3 {
		t.Fatalf("history open failure exit=%d stderr=%q", code, stderr.String())
	}
	var report struct {
		Latest  []map[string]any `json:"latest"`
		Summary []struct {
			Rounds int `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode completed in-memory report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if len(report.Latest) != 1 || report.Latest[0]["tcp_443"] != "open" || len(report.Summary) != 1 || report.Summary[0].Rounds != 1 {
		t.Fatalf("history open failure discarded completed in-memory output: %+v", report)
	}
	log := readCommandLog(t, logPath)
	if !strings.Contains(log, "ERROR - history operation failed") || !strings.Contains(log, "INFO - target run finished") {
		t.Fatalf("run-end/history failure logs missing at INFO level: %q", log)
	}
}

func TestCommandCompletedProbeErrorLogsAtErrorAndNormalRunEndAtInfo(t *testing.T) {
	for _, tc := range []struct {
		name, level string
		prober      commandTestProber
		wantLog     string
		wantAbsent  string
	}{
		{
			name: "completed probe error", level: "ERROR",
			prober: commandTestProber{results: []probe.Result{{
				Target: probe.Resolved{Index: 0, Host: "127.0.0.1", Address: netip.MustParseAddr("127.0.0.1")},
				ICMP:   probe.ICMPResult{State: "ERROR", Err: errors.New("raw socket denied")},
			}}},
			wantLog: "ERROR - target probe failed", wantAbsent: "INFO - target run finished",
		},
		{
			name: "normal run end", level: "INFO", prober: commandTestProber{},
			wantLog: "INFO - target run finished", wantAbsent: "ERROR - target probe failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "cn.log")
			var stdout, stderr bytes.Buffer
			runtime := commandTestRuntime(root, logPath, monitor.RunDeps{
				Prober: tc.prober,
				OpenHistory: func(ctx context.Context, path string) (monitor.History, error) {
					return history.OpenWriter(ctx, path)
				},
			})
			code := runCLIWithRuntime([]string{"--format", "json", "--history-file", filepath.Join(root, "history.db"),
				"--log-file", logPath, "--log-level", tc.level, "127.0.0.1"}, nil, &stdout, &stderr, runtime)
			if (tc.name == "completed probe error" && code != 3) || (tc.name == "normal run end" && code != 0) {
				t.Fatalf("command exit=%d stderr=%q", code, stderr.String())
			}
			var report map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("decode JSON report: %v; stdout=%q", err, stdout.String())
			}
			log := readCommandLog(t, logPath)
			if !strings.Contains(log, tc.wantLog) || strings.Contains(log, tc.wantAbsent) {
				t.Fatalf("logger output=%q; want %q and omit %q", log, tc.wantLog, tc.wantAbsent)
			}
			if tc.name == "completed probe error" {
				for _, diagnostic := range []string{"127.0.0.1", "ICMP", "raw socket denied"} {
					if !strings.Contains(stderr.String(), diagnostic) {
						t.Fatalf("completed probe failure diagnostic %q missing from stderr %q", diagnostic, stderr.String())
					}
				}
			}
		})
	}
}

func commandTestRuntime(home, logPath string, deps monitor.RunDeps) commandRuntime {
	return commandRuntime{
		environment: func() (configEnvironment, error) {
			return configEnvironment{HomeDir: home, WorkingDir: home}, nil
		},
		runDeps: deps,
	}
}

func seedCommandHistory(t *testing.T, database string, request target.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := history.OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open command fixture history: %v", err)
	}
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	const id = "command-history-seed"
	if _, err := store.BeginRun(ctx, history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports},
		Hosts:       request.Hosts,
	}, history.RunStart{ID: id, Started: started, Host: "command-test", PID: 1, Heartbeat: started}); err != nil {
		_ = store.Close()
		t.Fatalf("begin command fixture history: %v", err)
	}
	if _, err := store.RecordRound(ctx, id, history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: "127.0.0.1", Result: "OK", Time: started, TCP: map[int]string{},
	}}}); err != nil {
		_ = store.Close()
		t.Fatalf("record command fixture history: %v", err)
	}
	if err := store.EndRun(ctx, id, started.Add(time.Second), "finished"); err != nil {
		_ = store.Close()
		t.Fatalf("finish command fixture history: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close command fixture history: %v", err)
	}
}

func readCommandLog(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read command logger output: %v", err)
	}
	return string(content)
}
