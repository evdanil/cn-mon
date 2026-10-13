package main

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
)

// This composition test proves that the path selected by the existing M1
// configuration layer is the path the M3 history API actually opens.
func TestConfiguredHistoryPathOpensSelectedStore(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(root, "cn-mon.ini")
	if err := os.WriteFile(configFile, []byte("[ping]\nhistory_file = selected/archive.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, warnings, err := loadSettings(configEnvironment{
		Executable: filepath.Join(work, "cn-mon"), HomeDir: home, WorkingDir: work,
	}, configOverrides{ConfigFile: configFile})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected config warnings: %q", warnings)
	}
	wantPath := filepath.Join(work, "selected", "archive.db")
	if settings.HistoryFile != wantPath {
		t.Fatalf("resolved history path=%q, want %q", settings.HistoryFile, wantPath)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	writer, err := history.OpenWriter(ctx, settings.HistoryFile)
	if err != nil {
		t.Fatalf("open configured history writer: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close configured history writer: %v", err)
	}
	reader, err := history.OpenReader(ctx, wantPath)
	if err != nil {
		t.Fatalf("open configured history reader: %v", err)
	}
	if reader == nil {
		t.Fatal("configured history database was not readable after writer closed")
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close configured history reader: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cn-ping-history.db")); !os.IsNotExist(err) {
		t.Fatalf("default history file should remain absent, stat error=%v", err)
	}
}

func TestConfiguredHistoryPathFallbackExecutableAndHomeConfigLayers(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	bin := filepath.Join(work, "bin")
	for _, dir := range []string{home, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := configEnvironment{Executable: filepath.Join(bin, "cn-mon"), HomeDir: home, WorkingDir: work}
	defaultSettings, warnings, err := loadSettings(env, configOverrides{})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load fallback settings: settings=%+v warnings=%q err=%v", defaultSettings, warnings, err)
	}
	defaultPath := filepath.Join(home, ".cn-ping-history.db")
	if defaultSettings.HistoryFile != defaultPath || defaultSettings.HistoryDays.Cmp(big.NewInt(30)) != 0 {
		t.Fatalf("built-in history defaults = path %q / %s days", defaultSettings.HistoryFile, defaultSettings.HistoryDays)
	}
	reader, err := history.OpenReader(context.Background(), defaultSettings.HistoryFile)
	if err != nil || reader != nil {
		t.Fatalf("missing default reader = %v, %v", reader, err)
	}
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Fatalf("reading missing default created a file: %v", err)
	}

	fallbackCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fallbackWriter, err := history.OpenWriter(fallbackCtx, defaultSettings.HistoryFile)
	if err != nil {
		t.Fatalf("open resolved fallback Store: %v", err)
	}
	fallbackKey := strings.Repeat("d", 64)
	historyConfigAppendRound(t, fallbackCtx, fallbackWriter, "fallback-default", fallbackKey, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if err := fallbackWriter.Close(); err != nil {
		t.Fatal(err)
	}
	fallbackReader, err := history.OpenReader(fallbackCtx, defaultSettings.HistoryFile)
	if err != nil || fallbackReader == nil {
		t.Fatalf("reopen resolved fallback Store: reader=%v err=%v", fallbackReader, err)
	}
	historyConfigAssertRoundCount(t, fallbackCtx, fallbackReader, fallbackKey, 1)
	if err := fallbackReader.Close(); err != nil {
		t.Fatal(err)
	}

	writeConfig(t, filepath.Join(bin, ".cn"), "[ping]\nhistory_file = executable.db\nhistory_days = 45\n")
	executableSettings, warnings, err := loadSettings(env, configOverrides{})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load executable .cn settings: %+v warnings=%q err=%v", executableSettings, warnings, err)
	}
	if want := filepath.Join(work, "executable.db"); executableSettings.HistoryFile != want || executableSettings.HistoryDays.Cmp(big.NewInt(45)) != 0 {
		t.Fatalf("executable .cn was not applied: path=%q days=%s", executableSettings.HistoryFile, executableSettings.HistoryDays)
	}

	writeConfig(t, filepath.Join(home, ".cn"), "[ping]\nhistory_file = home.db\nhistory_days = 60\n")
	homeSettings, warnings, err := loadSettings(env, configOverrides{})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load home .cn settings: %+v warnings=%q err=%v", homeSettings, warnings, err)
	}
	if want := filepath.Join(work, "home.db"); homeSettings.HistoryFile != want || homeSettings.HistoryDays.Cmp(big.NewInt(60)) != 0 {
		t.Fatalf("home .cn did not override executable .cn: path=%q days=%s", homeSettings.HistoryFile, homeSettings.HistoryDays)
	}
	store, err := history.OpenWriter(context.Background(), homeSettings.HistoryFile)
	if err != nil {
		t.Fatalf("open path selected by layered .cn files: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{homeSettings.HistoryFile, filepath.Join(work, "executable.db")} {
		_, err := os.Stat(path)
		if path == homeSettings.HistoryFile && err != nil {
			t.Fatalf("selected home-configured store missing at %q: %v", path, err)
		}
		if path != homeSettings.HistoryFile && err == nil {
			t.Fatalf("executable-configured store was opened despite home override: %q", path)
		}
	}
}

func TestCLIHistoryOverridesSelectStoreAndRetentionAcrossReopen(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "settings.cn")
	writeConfig(t, configPath, "[ping]\nhistory_file = configured/history.db\nhistory_days = 90\n")
	env := configEnvironment{Executable: filepath.Join(work, "cn-mon"), HomeDir: home, WorkingDir: work}
	configOnly, warnings, err := loadSettings(env, configOverrides{ConfigFile: configPath})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load configured retention: %+v warnings=%q err=%v", configOnly, warnings, err)
	}
	if configOnly.HistoryFile != filepath.Join(work, "configured", "history.db") || configOnly.HistoryDays.Cmp(big.NewInt(90)) != 0 {
		t.Fatalf("config settings = path %q / %s days", configOnly.HistoryFile, configOnly.HistoryDays)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-45 * 24 * time.Hour)
	configWriter, err := history.OpenWriter(ctx, configOnly.HistoryFile)
	if err != nil {
		t.Fatal(err)
	}
	historyConfigAppendRound(t, ctx, configWriter, "config-90-day", strings.Repeat("a", 64), old)
	deleted, err := configWriter.Prune(ctx, now, configOnly.HistoryDays)
	if err != nil || deleted != 0 {
		t.Fatalf("configured 90-day retention deleted=%d err=%v", deleted, err)
	}
	historyConfigAssertRoundCount(t, ctx, configWriter, strings.Repeat("a", 64), 1)
	if err := configWriter.Close(); err != nil {
		t.Fatal(err)
	}

	cliConfigPath := filepath.Join(work, "cli-settings.cn")
	writeConfig(t, cliConfigPath, "[ping]\nhistory_file = unused-config/history.db\nhistory_days = 90\n")
	relativeConfigCLI, err := parseCLI([]string{"-c", "cli-settings.cn", "127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("parse relative -c option: %v", err)
	}
	relativeSettings, warnings, err := loadSettings(env, relativeConfigCLI.Overrides)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load relative -c settings: %+v warnings=%q err=%v", relativeSettings, warnings, err)
	}
	if relativeSettings.HistoryFile != filepath.Join(work, "unused-config", "history.db") || relativeSettings.HistoryDays.Cmp(big.NewInt(90)) != 0 {
		t.Fatalf("relative -c did not select the work-directory config: path=%q days=%s", relativeSettings.HistoryFile, relativeSettings.HistoryDays)
	}
	parsedCLI, err := parseCLI([]string{"-c", "cli-settings.cn", "--history-file", "cli/override.db", "--history-days", "30", "127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("parse configuration and history overrides: %v", err)
	}
	selected, warnings, err := loadSettings(env, parsedCLI.Overrides)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load CLI-overridden settings: %+v warnings=%q err=%v", selected, warnings, err)
	}
	selectedPath := filepath.Join(work, "cli", "override.db")
	if selected.HistoryFile != selectedPath || selected.HistoryDays.Cmp(big.NewInt(30)) != 0 {
		t.Fatalf("CLI overrides did not win over config: path=%q days=%s", selected.HistoryFile, selected.HistoryDays)
	}
	cliWriter, err := history.OpenWriter(ctx, selected.HistoryFile)
	if err != nil {
		t.Fatalf("open CLI-selected Store: %v", err)
	}
	key := strings.Repeat("b", 64)
	historyConfigAppendRound(t, ctx, cliWriter, "cli-30-day", key, old)
	deleted, err = cliWriter.Prune(ctx, now, selected.HistoryDays)
	if err != nil || deleted != 1 {
		t.Fatalf("CLI 30-day retention deleted=%d err=%v", deleted, err)
	}
	historyConfigAssertRoundCount(t, ctx, cliWriter, key, 0)
	newKey := strings.Repeat("c", 64)
	historyConfigAppendRound(t, ctx, cliWriter, "cli-append", newKey, now.Add(-time.Hour))
	if err := cliWriter.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := history.OpenReader(ctx, selected.HistoryFile)
	if err != nil || reopened == nil {
		t.Fatalf("reopen CLI-selected Store: reader=%v err=%v", reopened, err)
	}
	historyConfigAssertRoundCount(t, ctx, reopened, newKey, 1)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	for _, unused := range []string{filepath.Join(home, ".cn-ping-history.db"), filepath.Join(work, "unused-config", "history.db")} {
		if _, err := os.Stat(unused); err == nil {
			t.Fatalf("CLI override unexpectedly created unused history path %q", unused)
		}
	}
}

func historyConfigAppendRound(t *testing.T, ctx context.Context, store *history.Store, runID, key string, at time.Time) {
	t.Helper()
	request := history.Request{RequestMeta: history.RequestMeta{Key: key}, Hosts: []string{"host"}}
	if _, err := store.BeginRun(ctx, request, history.RunStart{ID: runID, Started: at, Host: "test", PID: 1, Heartbeat: at}); err != nil {
		t.Fatalf("BeginRun %s: %v", runID, err)
	}
	if _, err := store.RecordRound(ctx, runID, history.RoundInput{
		Started: at, Observations: "!",
		Rows: []history.ResultRow{{Host: "host", Address: "192.0.2.1", Result: "OK", Time: at}},
	}); err != nil {
		t.Fatalf("RecordRound %s: %v", runID, err)
	}
	if err := store.EndRun(ctx, runID, at, "finished"); err != nil {
		t.Fatalf("EndRun %s: %v", runID, err)
	}
}

func historyConfigAssertRoundCount(t *testing.T, ctx context.Context, store *history.Store, key string, want int64) {
	t.Helper()
	err := store.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if want == 0 {
			if info != nil {
				t.Errorf("request survived retention with %d rounds", info.Rounds)
				return nil
			}
			return nil
		}
		if info == nil || info.Rounds != want {
			t.Errorf("stored request round count=%v, want %d", info, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read history request %s: %v", key, err)
	}
}
