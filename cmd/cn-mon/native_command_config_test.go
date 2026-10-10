// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

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
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/target"
)

func TestNativeCommandINIHistoryRetention(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database, logPath := filepath.Join(root, "selected-history.db"), filepath.Join(root, "selected.log")
	configPath := filepath.Join(root, "selected.cn")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	request, _, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil {
		t.Fatal(err)
	}
	seedOldNativeRound(t, database, request, port)
	config := fmt.Sprintf("[ping]\nhistory_file = %s\nhistory_days = 90\n[logging]\nlogfile = %s\nlevel = INFO\n", filepath.ToSlash(database), filepath.ToSlash(logPath))
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	runNativeConfigCommand(t, binary, root, database, logPath, configPath, port, nil, 1)
	verifyNativeRetention(t, database, request.Key, true)

	thirty := "30"
	runNativeConfigCommand(t, binary, root, database, logPath, configPath, port, &thirty, 1)
	verifyNativeRetention(t, database, request.Key, false)
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("INI-selected log file was not created: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_CONFIG ini90=retained cli30=pruned-old-seed recent-round-retained history-path=ini log-path=ini")
}

func runNativeConfigCommand(t *testing.T, binary, root, database, logPath, configPath string, port int, days *string, earlier int) {
	t.Helper()
	args := []string{"--format", "json", "--config", configPath, "--tcp", fmt.Sprint(port)}
	if days != nil {
		args = append(args, "--history-days", *days)
	}
	args = append(args, "127.0.0.1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = nativeCommandEnv(t, root, database, logPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			t.Fatalf("run native config command: %v", runErr)
		}
		code = exit.ExitCode()
	}
	var report struct {
		Summary []struct {
			EarlierRounds int `json:"earlier_rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode native config report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if len(report.Summary) != 1 || report.Summary[0].EarlierRounds != earlier {
		t.Fatalf("native command config retention: summary=%+v want earlier=%d stderr=%q", report.Summary, earlier, stderr.String())
	}
	if code != 0 && code != 3 {
		t.Fatalf("native config command exit=%d stderr=%q", code, stderr.String())
	}
}

func seedOldNativeRound(t *testing.T, database string, request target.Request, port int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := history.OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open native retention fixture store: %v", err)
	}
	started := time.Now().UTC().Add(-45 * 24 * time.Hour).Truncate(time.Second)
	seedRequest := history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports},
		Hosts:       request.Hosts,
	}
	if _, err := store.BeginRun(ctx, seedRequest, history.RunStart{
		ID: "native-retention-seed", Started: started, Host: "native-fixture", PID: 1, Heartbeat: started,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("seed native retention run: %v", err)
	}
	if _, err := store.RecordRound(ctx, "native-retention-seed", history.RoundInput{
		Started: started, Observations: "!", Rows: []history.ResultRow{{
			Host: request.Hosts[0], Address: "127.0.0.1", Result: "OK", Time: started, TCP: map[int]string{port: "open"},
		}},
	}); err != nil {
		_ = store.Close()
		t.Fatalf("seed native retention round: %v", err)
	}
	if err := store.EndRun(ctx, "native-retention-seed", started.Add(time.Second), "finished"); err != nil {
		_ = store.Close()
		t.Fatalf("finish native retention seed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close native retention seed: %v", err)
	}
}

func verifyNativeRoundCount(t *testing.T, database, key string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil || reader == nil {
		t.Fatalf("open native retention history: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info == nil || info.Rounds != int64(want) {
			return fmt.Errorf("stored rounds=%v, want %d", info, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify native retention history: %v", err)
	}
}

func verifyNativeRetention(t *testing.T, database, key string, seedExpected bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil || reader == nil {
		t.Fatalf("open native retention history: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		request, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if request == nil || request.Rounds != 2 {
			return fmt.Errorf("stored rounds=%v, want the two recent command rounds", request)
		}
		page, err := snapshot.Runs(request.ID, false, nil, 10)
		if err != nil {
			return err
		}
		if len(page.Items) != 2 {
			return fmt.Errorf("stored run count=%d, want two recent command runs", len(page.Items))
		}
		seedFound := false
		for _, run := range page.Items {
			if run.ID == "native-retention-seed" {
				seedFound = true
			}
			if run.Status != "finished" || run.Rounds != 1 {
				return fmt.Errorf("recent command run was not retained as complete: %+v", run)
			}
		}
		if seedFound != seedExpected {
			return fmt.Errorf("45-day seed present=%t, want %t; all runs=%+v", seedFound, seedExpected, page.Items)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify native retention behavior: %v", err)
	}
}
