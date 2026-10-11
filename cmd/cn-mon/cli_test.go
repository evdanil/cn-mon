package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/output"
)

func TestCommandNoteFallsBackWhenScreenUnavailable(t *testing.T) {
	var stderr bytes.Buffer
	commandNote(nil, &stderr, "visible diagnostic")
	if got := stderr.String(); got != "cn-mon: visible diagnostic\n" {
		t.Fatalf("diagnostic without screen=%q, want ordinary stderr output", got)
	}
}

func TestCommandNoteFallsBackWhenScreenHasNoOutput(t *testing.T) {
	screen := output.OpenScreen(nil, nil, "default", true, output.KeyHandlers{})
	defer screen.Close()
	state := &commandScreenState{screen: screen}
	var stderr bytes.Buffer
	commandNote(state, &stderr, "visible diagnostic")
	if got := stderr.String(); got != "cn-mon: visible diagnostic\n" {
		t.Fatalf("no-output screen fallback=%q, want ordinary stderr output", got)
	}
}

func TestCommandNoteUsesDisabledScreenSink(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	screen := output.OpenScreen(nil, writer, "default", true, output.KeyHandlers{})
	defer screen.Close()
	if screen.Enabled() {
		t.Fatal("pipe-backed screen unexpectedly enabled live drawing")
	}
	state := &commandScreenState{screen: screen}
	var stderr bytes.Buffer
	commandNote(state, &stderr, "screen-owned diagnostic")
	screen.Close()
	_ = writer.Close()
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "cn-mon: screen-owned diagnostic\n" {
		t.Fatalf("disabled screen received %q, want its own diagnostic line", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("disabled screen diagnostic bypassed its writer: %q", stderr.String())
	}
}

func TestParseCLIOptionsAndAliases(t *testing.T) {
	got, err := parseCLI([]string{
		"--config", "first.cn", "--config", "", "-t", "dark", "--theme", "pastel",
		"-l", "first.log", "--log-file", "", "--log-level", "WARNING",
		"--history-file", "history.db", "--history-days", "45", "10.0.0.1",
		"--file", "first.txt", "-f", "second.txt", "--tcp", "443,22,443",
		"--every", "30s", "--for", "2h", "--since", "0m", "--format", "json",
	}, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if got.Mode != modeRun || got.Overrides.ConfigFile != "" || got.Overrides.Theme != "pastel" || got.Overrides.LogFile != "" || got.Overrides.LogLevel != "WARNING" {
		t.Fatalf("last scalar values not retained: %#v", got)
	}
	if got.Overrides.HistoryFile == nil || *got.Overrides.HistoryFile != "history.db" || got.Overrides.HistoryDays == nil || got.Overrides.HistoryDays.String() != "45" {
		t.Fatalf("history overrides lost: %#v", got)
	}
	if len(got.Objects) != 1 || got.Objects[0] != "10.0.0.1" || got.File == nil || *got.File != "second.txt" {
		t.Fatalf("targets/file mismatch: %#v", got)
	}
	if len(got.Ports) != 2 || got.Ports[0] != 22 || got.Ports[1] != 443 || got.Every == nil || *got.Every != 30*time.Second || got.For == nil || *got.For != 2*time.Hour || got.Format != "json" {
		t.Fatalf("validated options mismatch: %#v", got)
	}
	if got.Since == nil || !got.Since.Equal(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)) || !got.OwnRunOnly {
		t.Fatalf("since 0m cutoff = %v", got.Since)
	}
}

func TestRetainedShortOptionAliases(t *testing.T) {
	got, err := parseCLI([]string{"-c", "site.cn", "10.0.0.1"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.Overrides.ConfigFile != "site.cn" {
		t.Fatalf("-c did not set config file: %#v", got.Overrides)
	}
	for _, arg := range []string{"-h", "--help", "-v", "--version"} {
		got, err := parseCLI([]string{arg}, time.Now().UTC())
		if err != nil {
			t.Errorf("parseCLI(%q): %v", arg, err)
			continue
		}
		if (arg == "-h" || arg == "--help") && got.Mode != modeHelp {
			t.Errorf("%s mode = %v, want help", arg, got.Mode)
		}
		if (arg == "-v" || arg == "--version") && got.Mode != modeVersion {
			t.Errorf("%s mode = %v, want version", arg, got.Mode)
		}
	}
}

func TestParseCLIRerunSelectionAndOverrides(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	got, err := parseCLI([]string{"--rerun", "AbCd1234", "--every", "20s", "--for", "1m", "--since", "0m", "--format", "json"}, start)
	if err != nil {
		t.Fatalf("parse rerun: %v", err)
	}
	if got.Mode != modeRerun || got.ID != "AbCd1234" || got.Every == nil || *got.Every != 20*time.Second || got.For == nil || *got.For != time.Minute || got.Since == nil || !got.OwnRunOnly || got.Format != "json" {
		t.Fatalf("rerun options = %#v", got)
	}
	got, err = parseCLI([]string{"--rerun", "abcd1234", "--rerun", "FEDC9876"}, start)
	if err != nil || got.Mode != modeRerun || got.ID != "FEDC9876" {
		t.Fatalf("repeated rerun should use last value: options=%#v err=%v", got, err)
	}
}

func TestParseCLIRerunRejectsInvalidSelectionAndConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"--rerun", "abc"},
		{"--rerun", "xyz12345"},
		{"--rerun", "abcd1234", "10.0.0.1"},
		{"--rerun", "abcd1234", "-"},
		{"--rerun", "abcd1234", "--file", "targets.txt"},
		{"--rerun", "abcd1234", "--tcp", "443"},
		{"--rerun", "abcd1234", "--show", "abcd1234"},
		{"--rerun", "abcd1234", "--list"},
	} {
		if _, err := parseCLI(args, time.Now().UTC()); err == nil {
			t.Errorf("parseCLI(%q) unexpectedly succeeded", args)
		}
	}
}

func TestCLIProcessHelper(t *testing.T) {
	encoded := os.Getenv("CNMON_TEST_ARGS")
	if encoded == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		os.Exit(99)
	}
	os.Exit(runCLI(args, os.Stdin, os.Stdout, os.Stderr))
}

func TestCLIProcessExitCodesAndNoStartupSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		code    int
		wantOut string
		wantErr string
	}{
		{name: "help", args: []string{"--help"}, code: 0, wantOut: "Usage:", wantErr: ""},
		{name: "help after unknown", args: []string{"--unknown", "--help"}, code: 0, wantOut: "Usage:", wantErr: ""},
		{name: "version after unknown", args: []string{"--unknown", "--version"}, code: 0, wantOut: "cn-mon dev", wantErr: ""},
		{name: "help after semantic error", args: []string{"--show", "bad", "--help"}, code: 0, wantOut: "Usage:", wantErr: ""},
		{name: "help after short interval", args: []string{"--every", "5s", "--help"}, code: 0, wantOut: "Usage:", wantErr: ""},
		{name: "help after malformed interval", args: []string{"--every", "bad", "--help"}, code: 2, wantOut: "", wantErr: "invalid --every duration"},
		{name: "help after invalid theme", args: []string{"--theme", "bad", "--help"}, code: 2, wantOut: "", wantErr: "invalid theme"},
		{name: "version", args: []string{"-v"}, code: 0, wantOut: "cn-mon dev", wantErr: ""},
		{name: "no arguments", args: []string{}, code: 2, wantOut: "", wantErr: "no arguments"},
		{name: "invalid", args: []string{"--history-file", "", "127.0.0.1"}, code: 2, wantOut: "", wantErr: "--history-file must not be empty"},
		{name: "empty theme", args: []string{"--theme", "", "127.0.0.1"}, code: 2, wantOut: "", wantErr: "invalid theme"},
		{name: "empty log level", args: []string{"--log-level", "", "127.0.0.1"}, code: 2, wantOut: "", wantErr: "invalid log level"},
		{name: "default table input error precedes startup", args: []string{"--file", "missing-targets.txt"}, code: 2, wantOut: "", wantErr: "read target file"},
		{name: "JSON input error precedes startup", args: []string{"--format", "json", "--file", "missing-targets.txt"}, code: 2, wantOut: "", wantErr: "read target file"},
		{name: "CSV input error precedes startup", args: []string{"--format", "csv", "--file", "missing-targets.txt"}, code: 2, wantOut: "", wantErr: "read target file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			encoded, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCLIProcessHelper$")
			cmd.Dir = dir
			cmd.Env = envWithout(os.Environ(), "HOME", "USERPROFILE", "CNMON_TEST_ARGS")
			cmd.Env = append(cmd.Env, "HOME="+dir, "USERPROFILE="+dir, "CNMON_TEST_ARGS="+string(encoded))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("process start: %v", err)
				}
				code = exitErr.ExitCode()
			}
			if code != tc.code {
				t.Errorf("exit = %d, want %d (stderr %q)", code, tc.code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.wantOut) {
				t.Errorf("stdout %q lacks %q", stdout.String(), tc.wantOut)
			}
			if !strings.Contains(stderr.String(), tc.wantErr) {
				t.Errorf("stderr %q lacks %q", stderr.String(), tc.wantErr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				names := make([]string, len(entries))
				for i, entry := range entries {
					names[i] = filepath.Base(entry.Name())
				}
				t.Errorf("created files on startup path: %v", names)
			}
		})
	}
}

func TestAllRejectedTargetEmitsEmptyJSONWithoutOpeningHistory(t *testing.T) {
	dir := t.TempDir()
	args, err := json.Marshal([]string{"--format", "json", "--history-file", "must-not-open.db", "999.1"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIProcessHelper$")
	cmd.Dir = dir
	cmd.Env = envWithout(os.Environ(), "HOME", "USERPROFILE", "CNMON_TEST_ARGS")
	cmd.Env = append(cmd.Env, "HOME="+dir, "USERPROFILE="+dir, "CNMON_TEST_ARGS="+string(args))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("process start: %v", err)
		}
		code = exitErr.ExitCode()
	}
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr.String())
	}
	var report struct {
		Seen     []json.RawMessage `json:"seen"`
		Latest   []json.RawMessage `json:"latest"`
		Summary  []json.RawMessage `json:"summary"`
		NotFound []struct {
			Object string `json:"object"`
		} `json:"not_found"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v; output=%q", err, stdout.String())
	}
	if report.Seen == nil || report.Latest == nil || report.Summary == nil || report.NotFound == nil {
		t.Fatalf("empty report sections must be arrays: %+v", report)
	}
	if len(report.Seen) != 0 || len(report.Latest) != 0 || len(report.Summary) != 0 || len(report.NotFound) != 1 || report.NotFound[0].Object != "999.1" {
		t.Fatalf("all-rejected report = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "must-not-open.db")); !os.IsNotExist(err) {
		t.Fatalf("all-rejected input opened history: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cn.log")); err != nil {
		t.Fatalf("logger should be initialized before canonical target rejection: %v", err)
	}
}

func TestLoggingInitializationFailurePrecedesHistory(t *testing.T) {
	root := t.TempDir()
	homeFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(homeFile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal([]string{"--format", "json", "--history-file", "must-not-open.db", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIProcessHelper$")
	cmd.Dir = root
	cmd.Env = envWithout(os.Environ(), "HOME", "USERPROFILE", "CNMON_TEST_ARGS")
	cmd.Env = append(cmd.Env, "HOME="+homeFile, "USERPROFILE="+homeFile, "CNMON_TEST_ARGS="+string(args))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("process start: %v", err)
		}
		code = exitErr.ExitCode()
	}
	if code != 3 || !strings.Contains(stderr.String(), "cannot initialize logging") {
		t.Fatalf("exit=%d stderr=%q, want logging initialization failure/3", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("logger failure wrote report: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "must-not-open.db")); !os.IsNotExist(err) {
		t.Fatalf("logger initialization failure opened history: %v", err)
	}
}

func envWithout(env []string, names ...string) []string {
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		keep := true
		for _, name := range names {
			if strings.HasPrefix(item, name+"=") {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func TestParseCLIReaderModesAndAliases(t *testing.T) {
	for _, tc := range []struct {
		args []string
		mode commandMode
	}{
		{[]string{"--list"}, modeList},
		{[]string{"--show", "AbCd"}, modeShow},
		{[]string{"--follow", "ffff"}, modeFollow},
	} {
		got, err := parseCLI(tc.args, time.Now().UTC())
		if err != nil {
			t.Errorf("parseCLI(%v): %v", tc.args, err)
			continue
		}
		if got.Mode != tc.mode {
			t.Errorf("parseCLI(%v).Mode = %v, want %v", tc.args, got.Mode, tc.mode)
		}
	}
	for _, args := range [][]string{
		{"--list", "-f", "input.txt"}, {"--show", "a1b2", "--file", "-"},
		{"--follow", "a1b2", "--since", "2h", "-"}, {"--list", "--since", "2h"},
		{"--list", "--show", "a1b2"}, {"--follow", "a1b2", "--every", "30s"},
		{"--list", "--history-days", "30"},
		{"--history-days", "0", "10.0.0.1"}, {"--theme", "Dark", "10.0.0.1"},
		{"--report", "10.0.0.1"}, {"-nc", "10.0.0.1"}, {"-g", "secret", "10.0.0.1"},
		{"--tcp", "0", "10.0.0.1"},
		{"--every", "9s", "10.0.0.1"}, {"--for", "30s", "--every", "60s", "10.0.0.1"},
	} {
		if _, err := parseCLI(args, time.Now().UTC()); err == nil {
			t.Errorf("parseCLI(%v) unexpectedly succeeded", args)
		}
	}
}

func TestRepeatedReaderSelectorLastIDWins(t *testing.T) {
	got, err := parseCLI([]string{"--show", "a1b2", "--show", "C3D4"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != modeShow || got.ID != "C3D4" {
		t.Fatalf("repeated --show did not use last value: %#v", got)
	}
	if _, err := parseCLI([]string{"--list", "--list"}, time.Now().UTC()); err != nil {
		t.Fatalf("repeated --list: %v", err)
	}
}

func TestMalformedBooleanAndInterruptedOptionValues(t *testing.T) {
	for _, args := range [][]string{
		{"--help=false"}, {"--version=true"}, {"--list=garbage"},
		{"--file", "--list"}, {"--config", "--log-file", "run.log", "127.0.0.1"},
	} {
		if _, err := parseCLI(args, time.Now().UTC()); err == nil {
			t.Errorf("parseCLI(%v) unexpectedly accepted a malformed option", args)
		}
	}
}

func TestUnknownOptionsAreDeferredUntilHelpOrVersionResolution(t *testing.T) {
	for _, args := range [][]string{{"--unknown", "--help"}, {"--unknown", "--version"}} {
		got, err := parseCLI(args, time.Now().UTC())
		if err != nil {
			t.Errorf("parseCLI(%v): %v", args, err)
			continue
		}
		if got.Mode != modeHelp && got.Mode != modeVersion {
			t.Errorf("parseCLI(%v).Mode = %v", args, got.Mode)
		}
	}
	if _, err := parseCLI([]string{"--unknown", "127.0.0.1"}, time.Now().UTC()); err == nil {
		t.Fatal("unknown option at end of parsing was accepted")
	}
}

func TestHelpBypassesOnlyPostParseSemanticValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--list", "--show", "a1b2", "--help"},
		{"--show", "bad", "--help"},
		{"--list", "-f", "-", "--help"},
		{"--every", "5s", "--help"},
	} {
		got, err := parseCLI(args, time.Now().UTC())
		if err != nil || got.Mode != modeHelp {
			t.Errorf("parseCLI(%v) = %#v, %v; want help", args, got, err)
		}
	}
	for _, args := range [][]string{
		{"--every", "nonsense", "--help"},
		{"--tcp", "0", "--help"},
		{"--theme", "unknown", "--help"},
		{"--file", "--help"},
	} {
		if _, err := parseCLI(args, time.Now().UTC()); err == nil {
			t.Errorf("parseCLI(%v) should fail before help", args)
		}
	}
	got, err := parseCLI([]string{"--show", "bad", "--show", "a1b2"}, time.Now().UTC())
	if err != nil || got.Mode != modeShow || got.ID != "a1b2" {
		t.Errorf("repeated --show with valid final ID = %#v, %v", got, err)
	}
}

func TestSinceAcceptsPythonFromISOFormatForms(t *testing.T) {
	start := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		input string
		want  time.Time
	}{
		{"2026-10-10T12:30Z", time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)},
		{"2026-10-10T12:30+02:00", time.Date(2026, 10, 10, 10, 30, 0, 0, time.UTC)},
		{"2026-10-10T12", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		{"2026-10-10T12:30:45.123456", time.Date(2026, 10, 10, 12, 30, 45, 123456000, time.UTC)},
		{"2026-10-10T12,5", time.Date(2026, 10, 10, 12, 0, 0, 500000000, time.UTC)},
		{"2026-10-10T1230.5", time.Date(2026, 10, 10, 12, 30, 0, 500000000, time.UTC)},
		{"20261010", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)},
		{"20261010T1230", time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)},
		{"2026-W41-6", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)},
		{"2026-W41-6T12:30", time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)},
		{"2026-W41", time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)},
		{"2026W41", time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)},
		{"2026-W41T12", time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)},
		{"2026W416T12", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		{"2026-W41-01", time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)},
		{"2026-W41-11", time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC)},
		{"2026W41-01", time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)},
		{"2026-W41X01", time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)},
		{"2026-W41-6X01", time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)},
		{"2026-10-10T12:30+02", time.Date(2026, 10, 10, 10, 30, 0, 0, time.UTC)},
		{"2026-10-10T12:30+0200", time.Date(2026, 10, 10, 10, 30, 0, 0, time.UTC)},
		{"2026-10-10T12:30:45+02:00:30.5", time.Date(2026, 10, 10, 10, 30, 14, 500000000, time.UTC)},
		{"2026-10-10T12:30:45,123456789", time.Date(2026, 10, 10, 12, 30, 45, 123456000, time.UTC)},
		{"2026-10-10T12:30,5", time.Date(2026, 10, 10, 12, 30, 0, 500000000, time.UTC)},
		{"2026-10-10T12:30+02.5", time.Date(2026, 10, 10, 10, 29, 59, 500000000, time.UTC)},
		{"2026-10-10T12:30+02:00.5", time.Date(2026, 10, 10, 10, 29, 59, 500000000, time.UTC)},
		{"2026-10-10T12:30+01:30.5", time.Date(2026, 10, 10, 10, 59, 59, 500000000, time.UTC)},
		{"2026-10-10T12:30+0130.5", time.Date(2026, 10, 10, 10, 59, 59, 500000000, time.UTC)},
		{"2026-10-10␟12:30", time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)},
		{"2026-10-10\uFFFD12:30", time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)},
		{"2026-10-10T12:30:45.123456789+020000.123456789", time.Date(2026, 10, 10, 10, 30, 45, 0, time.UTC)},
	} {
		got, ownRunOnly, err := parseSince(tc.input, start)
		if err != nil {
			t.Errorf("parseSince(%q): %v", tc.input, err)
			continue
		}
		if !got.Equal(tc.want) || ownRunOnly {
			t.Errorf("parseSince(%q) = %v, ownRunOnly=%t; want %v, false", tc.input, got, ownRunOnly, tc.want)
		}
	}
	for _, value := range []string{"2026", "2026-10", "2026-283", "12:30", "0000-01-01", "2026-13-01", "2026-10-10T12:60", "2026-10-10T24:00", "2026-10-10T12:30 UTC", "0001-01-01T00:00+01:00", "9999-12-31T23:00-02:00", "2026-10-10T12:30+23:59:60", "2026-10-10T12:30+24:00"} {
		if _, _, err := parseSince(value, time.Now().UTC()); err == nil {
			t.Errorf("parseSince(%q) accepted a form datetime.fromisoformat rejects", value)
		}
	}
	if _, _, err := parseSince("2026-10-10\xff12:30", time.Now().UTC()); err == nil {
		t.Error("parseSince accepted malformed UTF-8 in a Go datetime input")
	}
}

func TestEnumValuesAreValidatedWhenSupplied(t *testing.T) {
	for _, args := range [][]string{
		{"--theme", "", "127.0.0.1"},
		{"--log-level", "", "127.0.0.1"},
		{"--theme", "unknown", "--theme", "dark", "127.0.0.1"},
		{"--log-level", "TRACE", "--log-level", "INFO", "127.0.0.1"},
		{"--theme", "unknown", "--help"},
	} {
		if _, err := parseCLI(args, time.Now().UTC()); err == nil {
			t.Errorf("parseCLI(%v) unexpectedly accepted an invalid enum", args)
		}
	}
	got, err := parseCLI([]string{"--help", "--theme", ""}, time.Now().UTC())
	if err != nil || got.Mode != modeHelp {
		t.Errorf("help before later invalid option = %#v, %v; want help success", got, err)
	}
}

func TestParseCLIDoubleDashAndSpecialModes(t *testing.T) {
	got, err := parseCLI([]string{"--", "-literal", "--version"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("parse after --: %v", err)
	}
	if len(got.Objects) != 2 || got.Objects[0] != "-literal" || got.Objects[1] != "--version" {
		t.Fatalf("unexpected positional targets: %#v", got.Objects)
	}
	for _, arg := range []string{"-h", "--help", "-v", "--version"} {
		got, err := parseCLI([]string{arg}, time.Now().UTC())
		if err != nil || !(got.Mode == modeHelp || got.Mode == modeVersion) {
			t.Errorf("parseCLI(%q) = %#v, %v", arg, got, err)
		}
	}
	for _, args := range [][]string{{}, {"--unknown"}, {"--show", "abc"}, {"--show", "a1g2"}, {"--format", "yaml"}} {
		if _, err := parseCLI(args, time.Now().UTC()); err == nil {
			t.Errorf("parseCLI(%v) unexpectedly succeeded", args)
		}
	}
}

func TestSinceMatchesPythonWeekDateAndOffsetNormalization(t *testing.T) {
	start := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	for _, input := range []string{"2026-W416", "2026W41-6", "2026-W416T12:30", "2026W41-6T12:30", "9999-W52-7"} {
		if _, _, err := parseSince(input, start); err == nil {
			t.Errorf("parseSince(%q) accepted a week date rejected by datetime.fromisoformat", input)
		}
	}
	for _, tc := range []struct {
		input string
		want  time.Time
	}{
		{"2026-10-10T12:30+01:60", time.Date(2026, 10, 10, 10, 30, 0, 0, time.UTC)},
		{"2026-10-10T12:30+01:00:60", time.Date(2026, 10, 10, 11, 29, 0, 0, time.UTC)},
		{"2026-10-10T12:30+00:00:00.5", time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)},
		{"2026-10-10T12:30+02:00:30.5", time.Date(2026, 10, 10, 10, 29, 29, 500000000, time.UTC)},
	} {
		got, _, err := parseSince(tc.input, start)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", tc.input, got, err, tc.want)
		}
	}
}

func TestAttachedShortOptionValuesMayContainEquals(t *testing.T) {
	got, err := parseCLI([]string{"-csite=prod.cn", "-flist=prod.txt", "-lrun=prod.log", "127.0.0.1"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("parse attached short option values: %v", err)
	}
	if got.Overrides.ConfigFile != "site=prod.cn" || got.File == nil || *got.File != "list=prod.txt" || got.Overrides.LogFile != "run=prod.log" {
		t.Fatalf("attached short option values parsed incorrectly: %#v", got)
	}
	got, err = parseCLI([]string{"-c=value", "127.0.0.1"}, time.Now().UTC())
	if err != nil || got.Overrides.ConfigFile != "value" {
		t.Fatalf("short option followed by '=' = %#v, %v; want config value", got, err)
	}
}
