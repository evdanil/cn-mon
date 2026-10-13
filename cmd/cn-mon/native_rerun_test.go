// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build native

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestNativeCommandRerunSavedBatch(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "rerun.log")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build loopback batch: request=%#v rejected=%#v err=%v", request, rejected, err)
	}
	inputFile := filepath.Join(root, "original-targets.txt")
	if err := os.WriteFile(inputFile, []byte("127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedCode, seedStdout, seedStderr := runNativeRerunProcess(t, binary, root, database, logPath,
		"--file", inputFile, "--tcp", fmt.Sprint(port), "--format", "json")
	seedReport := decodeNativeRerunReport(t, seedStdout, port)
	verifyNativeRerunProbeOutcome(t, seedCode, seedReport.Latest[0].Result, string(seedStderr))
	if seedReport.Latest[0].TCP != "open" {
		t.Fatalf("seed command TCP=%q, want open", seedReport.Latest[0].TCP)
	}
	seedState := captureNativeRerunSource(t, database, request.Key)
	if seedState.run.Status != "finished" || seedState.run.Rounds != 1 || seedState.run.Source == nil || *seedState.run.Source != filepath.Base(inputFile) {
		t.Fatalf("ordinary command did not create expected source run: %+v", seedState.run)
	}
	if err := os.Remove(inputFile); err != nil {
		t.Fatalf("remove original batch input after command seeding: %v", err)
	}

	listCode, listOutput, listStderr := runNativeRerunProcess(t, binary, root, database, logPath, "--list", "--format", "json")
	if listCode != 0 {
		t.Fatalf("native --list exit=%d stderr=%q", listCode, listStderr)
	}
	var listed struct {
		Requests []struct {
			ID string `json:"id"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(listOutput, &listed); err != nil || len(listed.Requests) != 1 || len(listed.Requests[0].ID) < 4 || !strings.HasPrefix(request.Key, listed.Requests[0].ID) {
		t.Fatalf("discover saved batch from --list: requests=%#v err=%v output=%s", listed.Requests, err, listOutput)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"--rerun", listed.Requests[0].ID, "--every", "10s", "--for", "10s", "--since", "0m", "--history-file", database, "--log-file", logPath, "--format", "json"}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = nativeCommandEnv(t, root, database, logPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run native rerun: %v", err)
		}
		code = exit.ExitCode()
	}
	report := decodeNativeRerunReport(t, stdout.Bytes(), port)
	verifyNativeRerunProbeOutcome(t, code, report.Latest[0].Result, stderr.String())
	if report.Latest[0].TCP != "open" || len(report.Summary) != 1 || report.Summary[0].Rounds != 1 || report.Summary[0].EarlierRounds != 0 {
		t.Fatalf("rerun report disagrees with expected one current TCP-open round: %+v", report)
	}
	verifyNativeRerun(t, database, request, seedState, report.Latest[0])
	if _, err := exec.Command(binary, "--rerun", "deadbeef", "--history-file", filepath.Join(root, "missing.db"), "--log-file", filepath.Join(root, "invalid.log")).Output(); err == nil {
		t.Fatal("missing native rerun unexpectedly succeeded")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("missing native rerun exit=%v, want 2", err)
		}
	}
	if _, err := filepath.Glob(filepath.Join(root, "missing.db*")); err != nil {
		t.Fatal(err)
	} else if matches, _ := filepath.Glob(filepath.Join(root, "missing.db*")); len(matches) != 0 {
		t.Fatalf("invalid selection created history files: %v", matches)
	}
	if _, err := filepath.Glob(filepath.Join(root, "invalid.log")); err != nil {
		t.Fatal(err)
	} else if matches, _ := filepath.Glob(filepath.Join(root, "invalid.log")); len(matches) != 0 {
		t.Fatalf("invalid selection opened logger: %v", matches)
	}
	t.Log("CNMON_NATIVE_COMMAND_RERUN actual-command-seed=list-discovery=pass removed-input=replayed tcp=open overrides=10s,10s json-history=agree source-results=preserved invalid-selection=exit2-no-write")
}

func runNativeRerunProcess(t *testing.T, binary, root, database, logPath string, args ...string) (int, []byte, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full := append(append([]string(nil), args...), "--history-file", database, "--log-file", logPath)
	cmd := exec.CommandContext(ctx, binary, full...)
	cmd.Env = nativeCommandEnv(t, root, database, logPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("native rerun helper %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	return code, stdout.Bytes(), stderr.Bytes()
}

type nativeRerunReport struct {
	Latest []struct {
		Host   string `json:"host"`
		Result string `json:"result"`
		TCP    string `json:"tcp_RESULT"`
	} `json:"latest"`
	Summary []struct {
		Rounds        int `json:"rounds"`
		EarlierRounds int `json:"earlier_rounds"`
	} `json:"summary"`
}

func decodeNativeRerunReport(t *testing.T, encoded []byte, port int) nativeRerunReport {
	t.Helper()
	var wire struct {
		Latest  []map[string]json.RawMessage `json:"latest"`
		Summary []struct {
			Rounds        int `json:"rounds"`
			EarlierRounds int `json:"earlier_rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode native rerun report: %v; output=%s", err, encoded)
	}
	if len(wire.Latest) != 1 {
		t.Fatalf("native rerun latest rows=%d, want one: %s", len(wire.Latest), encoded)
	}
	var result nativeRerunReport
	result.Summary = wire.Summary
	result.Latest = make([]struct {
		Host   string `json:"host"`
		Result string `json:"result"`
		TCP    string `json:"tcp_RESULT"`
	}, 1)
	for name, target := range map[string]*string{
		"host":                      &result.Latest[0].Host,
		"result":                    &result.Latest[0].Result,
		fmt.Sprintf("tcp_%d", port): &result.Latest[0].TCP,
	} {
		if err := json.Unmarshal(wire.Latest[0][name], target); err != nil {
			t.Fatalf("decode native rerun latest.%s: %v; output=%s", name, err, encoded)
		}
	}
	return result
}

type nativeRerunSource struct {
	run  history.RunInfo
	rows []history.ResultRow
}

func captureNativeRerunSource(t *testing.T, database, key string) nativeRerunSource {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var saved nativeRerunSource
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info == nil || info.NewestRun == nil {
			return fmt.Errorf("source request has no run")
		}
		saved.run = *info.NewestRun
		pages, err := snapshot.Rounds(info.ID, history.RoundFilter{Limit: 20})
		if err != nil {
			return err
		}
		for _, page := range pages.Items {
			if page.RunID == saved.run.ID {
				saved.rows = append(saved.rows, page.Rows...)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func verifyNativeRerun(t *testing.T, database string, request target.Request, source nativeRerunSource, report struct {
	Host   string `json:"host"`
	Result string `json:"result"`
	TCP    string `json:"tcp_RESULT"`
}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Runs != 2 || info.NewestRun == nil || info.NewestRun.ID == source.run.ID {
			return fmt.Errorf("native rerun did not append a distinct run: %#v", info)
		}
		if info.Key != request.Key || fmt.Sprint(info.Targets) != fmt.Sprint(request.Tokens) || fmt.Sprint(info.TCPPorts) != fmt.Sprint(request.Ports) || fmt.Sprint(info.Hosts) != fmt.Sprint(request.Hosts) {
			return fmt.Errorf("native rerun changed saved request metadata: %#v", info)
		}
		if info.NewestRun.Interval == nil || *info.NewestRun.Interval != 10*time.Second || info.NewestRun.Until == nil || info.NewestRun.Until.Sub(info.NewestRun.Started) < 9*time.Second || info.NewestRun.Until.Sub(info.NewestRun.Started) > 11*time.Second {
			return fmt.Errorf("native rerun schedule = %#v", info.NewestRun)
		}
		if source.run.Source == nil || info.NewestRun.Source == nil || *info.NewestRun.Source != *source.run.Source {
			return fmt.Errorf("native rerun source provenance = %#v", info.NewestRun.Source)
		}
		if source.run.Status != "finished" || info.NewestRun.Rounds != 1 || info.NewestRun.Status != "finished" {
			return fmt.Errorf("source/new run status/rounds changed: source=%#v new=%#v", source.run, info.NewestRun)
		}
		pages, err := snapshot.Rounds(info.ID, history.RoundFilter{Limit: 20})
		if err != nil {
			return err
		}
		runs, err := snapshot.Runs(info.ID, false, nil, 20)
		if err != nil {
			return err
		}
		foundOriginalRun := false
		for _, run := range runs.Items {
			if run.ID == source.run.ID {
				foundOriginalRun = run.Status == source.run.Status && run.Rounds == source.run.Rounds
			}
		}
		foundSource, foundNew := false, false
		for _, round := range pages.Items {
			if round.RunID == source.run.ID {
				foundSource = fmt.Sprint(round.Rows) == fmt.Sprint(source.rows)
			}
			if round.RunID == info.NewestRun.ID && len(round.Rows) == 1 {
				row := round.Rows[0]
				foundNew = row.Host == report.Host && row.Result == report.Result && row.TCP[request.Ports[0]] == report.TCP
			}
		}
		if !foundOriginalRun || !foundSource || !foundNew {
			return fmt.Errorf("report/history agreement original-run-preserved=%t source-results-preserved=%t new-round-agrees=%t", foundOriginalRun, foundSource, foundNew)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify native rerun: %v", err)
	}
}
