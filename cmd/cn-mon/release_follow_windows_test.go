// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows && native

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

// TestNativeReleaseEndedFollowAllFormats verifies that the extracted binary
// opens a real private console for --follow and renders an already-ended
// request in every supported semantic format without changing its history.
func TestNativeReleaseEndedFollowAllFormats(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "release ended follow.db")
	logPath := filepath.Join(directory, "release ended follow.log")
	request, rejected, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build ended-follow fixture request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	seedCommandHistory(t, database, request)
	for _, format := range []string{"json", "table", "csv", "md"} {
		t.Run(format, func(t *testing.T) {
			stdoutPath := filepath.Join(directory, "ended-follow-"+format+".stdout")
			args, err := json.Marshal([]string{"--follow", request.Key[:8], "--format", format,
				"--history-file", database, "--log-file", logPath})
			if err != nil {
				t.Fatal(err)
			}
			control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
				"CNMON_WINDOWS_CONSOLE_BOOTSTRAP": "1",
				"CNMON_WINDOWS_PRODUCT_BINARY":    binary,
				"CNMON_WINDOWS_PRODUCT_ARGS":      string(args),
				"CNMON_WINDOWS_PRODUCT_STDOUT":    stdoutPath,
			})
			defer control.Close()
			ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
			var baselineInput, baselineOutput uint32
			var baselineCursor bool
			var pid int
			if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t", &pid, &baselineInput, &baselineOutput, &baselineCursor); err != nil || pid <= 0 {
				t.Fatalf("parse ended-follow console readiness %q: pid=%d err=%v", ready, pid, err)
			}
			finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second)
			if !strings.Contains(finished, "exit=0") {
				t.Fatalf("ended-follow child result=%q", finished)
			}
			restored := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
			var restoredInput, restoredOutput uint32
			var restoredCursor bool
			if _, err := fmt.Sscanf(restored, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &restoredInput, &restoredOutput, &restoredCursor); err != nil {
				t.Fatalf("parse ended-follow console restoration %q: %v", restored, err)
			}
			if restoredInput != baselineInput || restoredOutput != baselineOutput || restoredCursor != baselineCursor {
				t.Fatalf("ended-follow console restoration=%q, want input=%08x output=%08x cursor=%t", restored, baselineInput, baselineOutput, baselineCursor)
			}
			if _, err := done(); err != nil {
				t.Fatalf("private-console bootstrap: %v", err)
			}
			stdout, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatalf("read extracted ended-follow stdout: %v", err)
			}
			if !bytes.Contains(stdout, []byte("127.0.0.1")) {
				t.Fatalf("extracted ended-follow format=%s omitted seeded host: %q", format, stdout)
			}
			switch format {
			case "json":
				var report map[string][]json.RawMessage
				if err := json.Unmarshal(stdout, &report); err != nil {
					t.Fatalf("decode ended-follow JSON: %v stdout=%q", err, stdout)
				}
				for _, section := range []string{"seen", "latest", "summary"} {
					if len(report[section]) != 1 {
						t.Fatalf("ended-follow JSON section %s has %d rows, want one", section, len(report[section]))
					}
				}
			case "table":
				for _, section := range []string{"SEEN", "LATEST", "SUMMARY", "Host", "Result"} {
					if !bytes.Contains(stdout, []byte(section)) {
						t.Fatalf("ended-follow table omitted %q: %q", section, stdout)
					}
				}
			case "csv":
				records, err := csv.NewReader(bytes.NewReader(stdout)).ReadAll()
				if err != nil || len(records) < 4 || records[0][0] != "section" || !nativeReleaseCSVHasSection(records, "seen") || !nativeReleaseCSVHasSection(records, "latest") || !nativeReleaseCSVHasSection(records, "summary") {
					t.Fatalf("ended-follow CSV sections/header malformed: records=%v err=%v", records, err)
				}
			case "md":
				for _, section := range []string{"## SEEN", "## LATEST", "## SUMMARY", "| Host | Address | Result |"} {
					if !bytes.Contains(stdout, []byte(section)) {
						t.Fatalf("ended-follow Markdown omitted %q: %q", section, stdout)
					}
				}
			}
		})
	}
	store, err := history.OpenReader(context.Background(), database)
	if err != nil {
		t.Fatalf("open ended-follow history after formats: %v", err)
	}
	err = store.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Runs != 1 || info.Rounds != 1 || info.NewestRun == nil || info.NewestRun.Status != "finished" {
			return fmt.Errorf("ended-follow modified seeded history: %+v", info)
		}
		return nil
	})
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("verify ended-follow history unchanged: view=%v close=%v", err, closeErr)
	}
	t.Log("CNMON_NATIVE_RELEASE_FOLLOW ended=read-only formats=json,table,csv,md exit=0 history=unchanged private-console=restored")
}

func nativeReleaseCSVHasSection(records [][]string, section string) bool {
	for _, record := range records[1:] {
		if len(record) > 0 && record[0] == section {
			return true
		}
	}
	return false
}
