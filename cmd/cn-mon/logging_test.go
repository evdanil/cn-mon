package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenLoggerAppendsUTF8FiltersLevelsAndRecognizesPythonAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cn.log")
	cfg := settings{LogFile: path, LogLevel: "WARN"}
	logger, err := openLogger(cfg, filepath.Join(dir, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Log("INFO", "hidden"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Log("WARNING", "café warning"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logger, err = openLogger(settings{LogFile: path, LogLevel: "FATAL"}, filepath.Join(dir, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Log("ERROR", "hidden too"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Log("CRITICAL", "fatal kept"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "hidden") || strings.Contains(text, "ERROR") || !strings.Contains(text, "café warning") || !strings.Contains(text, "fatal kept") || !strings.Contains(text, "WARNING") || !strings.Contains(text, "CRITICAL") {
		t.Fatalf("unexpected appended log: %q", text)
	}
}

func TestOpenLoggerFallsBackWhenConfiguredParentCannotBePrepared(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	fallback := filepath.Join(home, "cn.log")
	logger, err := openLogger(settings{LogFile: filepath.Join(blocker, "bad.log"), LogLevel: "NOTSET"}, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Log("INFO", "fallback works"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(fallback)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fallback works") {
		t.Fatalf("fallback log missing record: %q", b)
	}
}

func TestOpenLoggerReturnsFallbackOpenFailure(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-is-a-file")
	if err := os.WriteFile(home, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "configured-is-a-file")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := openLogger(settings{LogFile: filepath.Join(blocker, "cn.log"), LogLevel: "INFO"}, home)
	if err == nil {
		t.Fatal("expected failure when both configured log and ~/cn.log cannot be prepared")
	}
}

func TestOpenLoggerResolvesEmptyHomeBeforeFallback(t *testing.T) {
	if os.Getenv("CNMON_TEST_LOG_HOME_HELPER") == "1" {
		work, home := os.Getenv("CNMON_TEST_LOG_WORK"), os.Getenv("CNMON_TEST_LOG_HOME")
		if err := os.Chdir(work); err != nil {
			os.Exit(2)
		}
		logger, err := openLogger(settings{LogFile: filepath.Join(work, "blocked-parent", "bad.log"), LogLevel: "INFO"}, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if err := logger.Log("INFO", "home fallback"); err != nil {
			os.Exit(4)
		}
		if err := logger.Close(); err != nil {
			os.Exit(5)
		}
		if _, err := os.Stat(filepath.Join(home, "cn.log")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(6)
		}
		if _, err := os.Stat(filepath.Join(work, "cn.log")); err == nil {
			fmt.Fprintln(os.Stderr, "created cwd/cn.log")
			os.Exit(7)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "blocked-parent"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	t.Setenv("CNMON_TEST_LOG_HOME_HELPER", "1")
	t.Setenv("CNMON_TEST_LOG_HOME", home)
	t.Setenv("CNMON_TEST_LOG_WORK", work)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenLoggerResolvesEmptyHomeBeforeFallback$")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("empty-home logger helper failed: %v\n%s", err, output)
	}
}
