// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

func TestCLIListMissingHistoryReturnsEmptyReportWithoutCreatingHistory(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "missing", "history.db")
	logPath := filepath.Join(dir, "cn.log")
	args, err := json.Marshal([]string{"--list", "--format", "json", "--history-file", database, "--log-file", logPath})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCLIProcessHelper$")
	command.Dir = dir
	command.Env = envWithout(os.Environ(), "HOME", "USERPROFILE", "CNMON_TEST_ARGS")
	command.Env = append(command.Env, "HOME="+dir, "USERPROFILE="+dir, "CNMON_TEST_ARGS="+string(args))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if got := childExitCode(err); got != 1 {
		t.Fatalf("list exit=%d, want 1; err=%v stderr=%q", got, err, stderr.String())
	}
	var report struct {
		Requests []json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode empty list report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if report.Requests == nil || len(report.Requests) != 0 {
		t.Fatalf("missing history list report = %+v, want empty request array", report)
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only history was created: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("reader should initialize logging after valid input/options: %v", err)
	}
}

func TestCLIShowMissingHistoryReturnsEmptySectionsAndExitTwo(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "missing.db")
	args, err := json.Marshal([]string{"--show", "abcd", "--format", "json", "--history-file", database})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCLIProcessHelper$")
	command.Dir = dir
	command.Env = envWithout(os.Environ(), "HOME", "USERPROFILE", "CNMON_TEST_ARGS")
	command.Env = append(command.Env, "HOME="+dir, "USERPROFILE="+dir, "CNMON_TEST_ARGS="+string(args))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if got := childExitCode(err); got != 2 {
		t.Fatalf("show exit=%d, want 2; err=%v stderr=%q", got, err, stderr.String())
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode empty show report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	for _, key := range []string{"summary", "seen", "latest"} {
		if value, ok := report[key]; !ok || string(value) != "[]" {
			t.Fatalf("show section %q = %s, want []", key, value)
		}
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only history was created: %v", err)
	}
}

func childExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func TestCLIListAndShowRenderEveryFormat(t *testing.T) {
	for _, format := range []string{"", "json", "table", "csv", "md"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			database := filepath.Join(dir, "history.db")
			logPath := filepath.Join(dir, "cn.log")
			request, _, err := target.Build([]string{"127.0.0.1"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			seedCommandHistory(t, database, request)
			runtime := commandTestRuntime(dir, logPath, monitor.RunDeps{})
			runtime.isTerminal = func(io.Writer) bool { return true }
			for _, mode := range [][]string{{"--list"}, {"--show", request.Key[:8]}, {"--follow", request.Key[:8]}} {
				args := append([]string(nil), mode...)
				if format != "" {
					args = append(args, "--format", format)
				}
				args = append(args, "--history-file", database)
				var stdout, stderr bytes.Buffer
				code := runCLIWithRuntime(args, nil, &stdout, &stderr, runtime)
				if code != 0 {
					t.Fatalf("%v exit=%d stdout=%q stderr=%q", mode, code, stdout.String(), stderr.String())
				}
				if format == "json" {
					var report map[string]json.RawMessage
					if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
						t.Fatalf("%v JSON: %v; stdout=%q", mode, err, stdout.String())
					}
					wantSection := "seen"
					if mode[0] == "--list" {
						wantSection = "requests"
					}
					if value, ok := report[wantSection]; !ok || !strings.Contains(string(value), "127.0.0.1") {
						t.Fatalf("%v JSON section %q=%s, want host data", mode, wantSection, value)
					}
				} else if !strings.Contains(stdout.String(), "127.0.0.1") {
					t.Fatalf("%v %s output omitted request host: %q", mode, format, stdout.String())
				}
			}
		})
	}
}

func TestCLIRunRendersEveryFormat(t *testing.T) {
	for _, format := range []string{"", "json", "table", "csv", "md"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "cn.log")
			runtime := commandTestRuntime(dir, logPath, monitor.RunDeps{Prober: commandTestProber{}})
			args := []string{"--history-file", filepath.Join(dir, "history.db"), "127.0.0.1"}
			if format != "" {
				args = append([]string{"--format", format}, args...)
			}
			var stdout, stderr bytes.Buffer
			code := runCLIWithRuntime(args, nil, &stdout, &stderr, runtime)
			if code != 0 {
				t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if format == "json" {
				var report map[string]json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatalf("run JSON: %v; stdout=%q", err, stdout.String())
				}
				if !strings.Contains(string(report["latest"]), "127.0.0.1") {
					t.Fatalf("run JSON latest=%s, want target row", report["latest"])
				}
			} else if !strings.Contains(stdout.String(), "127.0.0.1") {
				t.Fatalf("run %s output omitted target: %q", format, stdout.String())
			}
		})
	}
}

func TestCLIFollowNonTerminalHasEmptySectionsAndDoesNotOpenHistory(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "missing.db")
	logPath := filepath.Join(dir, "cn.log")
	runtime := commandTestRuntime(dir, logPath, monitor.RunDeps{})
	runtime.isTerminal = func(io.Writer) bool { return false }
	var stdout, stderr bytes.Buffer
	code := runCLIWithRuntime([]string{"--follow", "abcd", "--format", "json", "--history-file", database}, nil, &stdout, &stderr, runtime)
	if code != 2 || !strings.Contains(stderr.String(), "requires stderr to be a terminal") {
		t.Fatalf("follow without terminal exit=%d stderr=%q, want 2 diagnostic", code, stderr.String())
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode empty follow report: %v; stdout=%q", err, stdout.String())
	}
	for _, key := range []string{"summary", "seen", "latest"} {
		if value, ok := report[key]; !ok || string(value) != "[]" {
			t.Fatalf("empty follow section %q=%s, want []", key, value)
		}
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("follow without terminal touched history: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("follow should initialize logger before checking terminal: %v", err)
	}
}

func TestCLIReaderErrorsEmitEmptyReports(t *testing.T) {
	for _, mode := range []string{"list", "show"} {
		for _, stage := range []string{"open", "view", "close"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				dir := t.TempDir()
				failure := errors.New("injected " + stage + " failure")
				runtime := commandTestRuntime(dir, filepath.Join(dir, "cn.log"), monitor.RunDeps{})
				runtime.readDeps.OpenHistory = func(context.Context, string) (monitor.Reader, error) {
					if stage == "open" {
						return nil, failure
					}
					return failingReadReader{stage: stage, err: failure}, nil
				}
				args := []string{"--list", "--format", "json"}
				if mode == "show" {
					args = []string{"--show", "abcd", "--format", "json"}
				}
				var stdout, stderr bytes.Buffer
				if code := runCLIWithRuntime(args, nil, &stdout, &stderr, runtime); code != 3 {
					t.Fatalf("exit=%d, want 3; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
				}
				var report map[string]json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatalf("decode empty report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
				if mode == "list" {
					if got := string(report["requests"]); got != "[]" {
						t.Fatalf("requests=%s, want []", got)
					}
				} else {
					for _, key := range []string{"summary", "seen", "latest"} {
						if got := string(report[key]); got != "[]" {
							t.Fatalf("%s=%s, want []", key, got)
						}
					}
				}
				if !strings.Contains(stderr.String(), failure.Error()) {
					t.Fatalf("stderr=%q does not report %v", stderr.String(), failure)
				}
			})
		}
	}
}

type failingReadReader struct {
	stage string
	err   error
}

func (r failingReadReader) View(context.Context, func(*history.Snapshot) error) error {
	if r.stage == "view" {
		return r.err
	}
	return nil
}

func (r failingReadReader) Close() error {
	if r.stage == "close" {
		return r.err
	}
	return nil
}

func TestCLIRunReportOptionsPreserveNOColorPresence(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	options := commandReportOptionsFor(commandRuntime{}, &bytes.Buffer{}, "table", "pastel")
	if !options.NoColor || options.Terminal {
		t.Fatalf("redirected NO_COLOR report options=%+v, want NoColor and no terminal", options)
	}
}
