// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/target"
)

const (
	releaseQualifyEnv  = "CNMON_RELEASE_QUALIFY"
	releaseVersionEnv  = "CNMON_RELEASE_VERSION"
	releaseRevisionEnv = "CNMON_RELEASE_REVISION"
	releaseSHA256Env   = "CNMON_RELEASE_EXPECTED_SHA256"
)

var releaseRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var releaseSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestNativeReleaseBinaryQualification(t *testing.T) {
	if os.Getenv(releaseQualifyEnv) != "1" {
		t.Skip("extracted release qualification is an explicit native CI gate")
	}
	if os.Getenv("CNMON_NATIVE_COMMAND") != "1" {
		t.Fatal("release qualification requires CNMON_NATIVE_COMMAND=1")
	}

	selected := os.Getenv("CNMON_NATIVE_COMMAND_BINARY")
	if !filepath.IsAbs(selected) {
		t.Fatalf("qualified command path must be absolute: %q", selected)
	}
	resolved := buildNativeCommand(t)
	if filepath.Clean(resolved) != filepath.Clean(selected) {
		t.Fatalf("native test selected %q, want explicit extracted binary %q", resolved, selected)
	}
	info, err := os.Lstat(selected)
	if err != nil {
		t.Fatalf("stat qualified command: %q: %v", selected, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("qualified command is not a regular file: %q (mode=%v)", selected, info.Mode())
	}

	version := os.Getenv(releaseVersionEnv)
	revision := os.Getenv(releaseRevisionEnv)
	wantDigest := os.Getenv(releaseSHA256Env)
	if version != "0.3.0" {
		t.Fatalf("qualified version=%q, want 0.3.0", version)
	}
	if !releaseRevisionPattern.MatchString(revision) {
		t.Fatalf("qualified revision must be 40 lowercase hex characters: %q", revision)
	}
	if !releaseSHA256Pattern.MatchString(wantDigest) {
		t.Fatalf("qualified binary SHA-256 must be 64 lowercase hex characters: %q", wantDigest)
	}

	before := releaseBinarySHA256(t, selected)
	if before != wantDigest {
		t.Fatalf("extracted binary SHA-256=%s, want %s", before, wantDigest)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, selected, "--version")
	command.Env = nativeReleaseChildEnv(t, t.TempDir(), "", "")
	stdout, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("extracted --version exceeded 5s: %v", ctx.Err())
		}
		t.Fatalf("run extracted --version: %v", err)
	}
	wantVersion := fmt.Sprintf("cn-mon %s (%s)\n", version, revision)
	if string(stdout) != wantVersion {
		t.Fatalf("extracted --version=%q, want exact %q", stdout, wantVersion)
	}
	after := releaseBinarySHA256(t, selected)
	if after != before {
		t.Fatalf("extracted binary changed during version check: before=%s after=%s", before, after)
	}
	t.Logf("CNMON_NATIVE_RELEASE_BINARY version=%s revision=%s sha256=%s path=absolute version=exact unchanged=true", version, revision, after)
}

func TestNativeReleaseExtractedReportsAllFormats(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "seeded history.db")
	logPath := filepath.Join(directory, "release command.log")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for extracted target report: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	request, rejected, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build seeded request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	seedCommandHistory(t, database, request)
	formats := []string{"", "json", "table", "csv", "md"}
	for _, format := range formats {
		formatArgs := []string{}
		if format != "" {
			formatArgs = []string{"--format", format}
		}
		targetArgs := append([]string(nil), formatArgs...)
		targetArgs = append(targetArgs, "--tcp", strconv.Itoa(port), "--history-file", database, "--log-file", logPath, "127.0.0.1")
		targetCode, targetStdout, targetStderr := runNativeReleaseCommand(t, binary, directory, database, logPath, targetArgs...)
		if targetCode != 0 && targetCode != 3 {
			t.Fatalf("extracted target format=%q exit=%d stderr=%q stdout=%q", format, targetCode, targetStderr, targetStdout)
		}
		if format == "json" {
			var report struct {
				Latest []map[string]json.RawMessage `json:"latest"`
			}
			if err := json.Unmarshal(targetStdout, &report); err != nil || len(report.Latest) != 1 {
				t.Fatalf("extracted target JSON rows=%d err=%v stdout=%q stderr=%q", len(report.Latest), err, targetStdout, targetStderr)
			}
			var tcp, result string
			if err := json.Unmarshal(report.Latest[0][fmt.Sprintf("tcp_%d", port)], &tcp); err != nil {
				t.Fatalf("decode target TCP field: %v; report=%v", err, report.Latest[0])
			}
			if err := json.Unmarshal(report.Latest[0]["result"], &result); err != nil {
				t.Fatalf("decode target ICMP field: %v; report=%v", err, report.Latest[0])
			}
			if tcp != "open" || (targetCode == 0 && result == "ERROR") || (targetCode == 3 && result != "ERROR") {
				t.Fatalf("extracted target result/TCP=%q/%q exit=%d stderr=%q", result, tcp, targetCode, targetStderr)
			}
		} else if !bytes.Contains(targetStdout, []byte("127.0.0.1")) {
			t.Fatalf("extracted target format=%q omitted host: %q", format, targetStdout)
		}
		if targetCode == 3 && !bytes.Contains(targetStdout, []byte("ERROR")) {
			t.Fatalf("extracted target format=%q exited 3 without an ICMP execution error in its report: stdout=%q stderr=%q", format, targetStdout, targetStderr)
		}
		if targetCode == 0 && bytes.Contains(targetStdout, []byte("ERROR")) {
			t.Fatalf("extracted target format=%q reported an ICMP execution error with exit 0: stdout=%q stderr=%q", format, targetStdout, targetStderr)
		}
		for _, mode := range []string{"list", "show"} {
			args := append([]string(nil), formatArgs...)
			if mode == "list" {
				args = append(args, "--list")
			} else {
				args = append(args, "--show", request.Key[:8])
			}
			args = append(args, "--history-file", database, "--log-file", logPath)
			code, stdout, stderr := runNativeReleaseCommand(t, binary, directory, database, logPath, args...)
			if code != 0 {
				t.Fatalf("extracted %s format=%q exit=%d stderr=%q stdout=%q", mode, format, code, stderr, stdout)
			}
			if format == "json" {
				var report map[string]json.RawMessage
				if err := json.Unmarshal(stdout, &report); err != nil {
					t.Fatalf("extracted %s JSON format: %v; stdout=%q stderr=%q", mode, err, stdout, stderr)
				}
				section := "seen"
				if mode == "list" {
					section = "requests"
				}
				if value, ok := report[section]; !ok || !bytes.Contains(value, []byte("127.0.0.1")) {
					t.Fatalf("extracted %s JSON section %q=%s, want seeded host", mode, section, value)
				}
			} else if !bytes.Contains(stdout, []byte("127.0.0.1")) {
				t.Fatalf("extracted %s format=%q omitted seeded host: %q", mode, format, stdout)
			}
		}
	}
	t.Log("CNMON_NATIVE_RELEASE_REPORTS target+list+show formats=default,json,table,csv,md tcp=open history=seeded output=verified")
}

func TestNativeReleaseProbeWithoutPingPath(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	pathDirectory := filepath.Join(directory, "empty child PATH")
	if err := os.Mkdir(pathDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(pathDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("child-only restricted PATH contents=%v err=%v, want empty", entries, err)
	}
	listener4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on IPv4 loopback: %v", err)
	}
	defer listener4.Close()
	listener6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen on IPv6 loopback: %v", err)
	}
	defer listener6.Close()
	port4 := listener4.Addr().(*net.TCPAddr).Port
	port6 := listener6.Addr().(*net.TCPAddr).Port
	database := filepath.Join(directory, "no-ping history.db")
	logPath := filepath.Join(directory, "no-ping command.log")
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port4) + "," + strconv.Itoa(port6), "127.0.0.1", "::1"}
	code, stdout, stderr := runNativeReleaseCommandWithPath(t, binary, directory, database, logPath, pathDirectory, args...)
	if code != 0 && code != 3 {
		t.Fatalf("extracted no-ping command exit=%d, want 0 or native permission error 3; stderr=%q stdout=%q", code, stderr, stdout)
	}
	var report struct {
		Latest []map[string]json.RawMessage `json:"latest"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatalf("decode extracted no-ping JSON: %v; stdout=%q stderr=%q", err, stdout, stderr)
	}
	if len(report.Latest) != 2 {
		t.Fatalf("no-ping latest rows=%d, want IPv4 and IPv6 loopback rows; stdout=%q", len(report.Latest), stdout)
	}
	states := make(map[string]string, len(report.Latest))
	for _, row := range report.Latest {
		var host, icmp string
		if err := json.Unmarshal(row["host"], &host); err != nil {
			t.Fatalf("decode host from %s: %v", row, err)
		}
		if err := json.Unmarshal(row["result"], &icmp); err != nil {
			t.Fatalf("decode ICMP result for %s: %v", host, err)
		}
		states[host] = icmp
	}
	for host, port := range map[string]int{"127.0.0.1": port4, "::1": port6} {
		state, ok := states[host]
		if !ok {
			t.Fatalf("extracted restricted-PATH output omitted %s: %+v", host, states)
		}
		if state != "OK" && !strings.HasPrefix(state, "OK (") && state != "ERROR" {
			t.Fatalf("restricted-PATH %s ICMP state=%q, want successful echo or permission ERROR", host, state)
		}
		if runtime.GOOS == "windows" && state != "OK" && !strings.HasPrefix(state, "OK (") {
			t.Fatalf("elevated Windows restricted-PATH %s echo state=%q, want success", host, state)
		}
		var tcp string
		for _, row := range report.Latest {
			var rowHost string
			_ = json.Unmarshal(row["host"], &rowHost)
			if rowHost == host {
				if err := json.Unmarshal(row[fmt.Sprintf("tcp_%d", port)], &tcp); err != nil {
					t.Fatalf("decode %s TCP result: %v; row=%v", host, err, row)
				}
			}
		}
		if tcp != "open" {
			t.Fatalf("restricted-PATH %s TCP state=%q, want open", host, tcp)
		}
	}
	wantExit := 0
	for _, state := range states {
		if state == "ERROR" {
			wantExit = 3
		}
	}
	if code != wantExit {
		t.Fatalf("restricted-PATH result exit=%d, want %d for ICMP states %v", code, wantExit, states)
	}
	if runtime.GOOS == "windows" && code != 0 {
		t.Fatalf("elevated Windows restricted-PATH probe exit=%d, want 0", code)
	}
	t.Logf("CNMON_NATIVE_RELEASE_NO_PING ipv4=%s tcp4=open ipv6=%s tcp6=open child-path=empty product=absolute", states["127.0.0.1"], states["::1"])
}

func TestNativeReleaseInputFileAndStdin(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "input history.db")
	logPath := filepath.Join(directory, "input command.log")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for input command: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	inputFile := filepath.Join(directory, "targets with spaces.txt")
	if err := os.WriteFile(inputFile, []byte("\uFEFF# extracted file input\r\n127.0.0.1\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fileArgs := []string{"--format", "json", "--history-file", database, "--log-file", logPath, "--tcp", strconv.Itoa(port), "--file", inputFile}
	assertNativeReleaseInputRun(t, binary, directory, database, logPath, nil, fileArgs, port)
	stdinArgs := []string{"--format", "json", "--history-file", database, "--log-file", logPath, "--tcp", strconv.Itoa(port), "--file", "-"}
	assertNativeReleaseInputRun(t, binary, directory, database, logPath, strings.NewReader("127.0.0.1\n"), stdinArgs, port)
	t.Log("CNMON_NATIVE_RELEASE_INPUT file=space-bom stdin=requested tcp=open json=pass history=finished")
}

func TestNativeReleaseUnreadableInputHasNoStartupEffects(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "missing-input history.db")
	logPath := filepath.Join(directory, "missing-input command.log")
	missing := filepath.Join(directory, "does not exist.txt")
	args := []string{"--history-file", database, "--log-file", logPath, "--file", missing}
	code, stdout, stderr := runNativeReleaseCommand(t, binary, directory, database, logPath, args...)
	if code != 2 || len(stdout) != 0 || !bytes.Contains(stderr, []byte("does not exist")) {
		t.Fatalf("unreadable extracted input exit=%d stdout=%q stderr=%q, want usage 2 before startup", code, stdout, stderr)
	}
	for _, path := range []string{database, logPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid input created startup file %q: %v", path, err)
		}
	}
	t.Log("CNMON_NATIVE_RELEASE_INPUT missing-file exit=2 stdout=empty startup=none")
}

func assertNativeReleaseInputRun(t *testing.T, binary, directory, database, logPath string, stdin io.Reader, args []string, port int) {
	t.Helper()
	code, stdout, stderr := runNativeReleaseCommandWithInput(t, binary, directory, database, logPath, stdin, args...)
	if code != 0 && code != 3 {
		t.Fatalf("extracted input run exit=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	var report struct {
		Latest []map[string]json.RawMessage `json:"latest"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil || len(report.Latest) != 1 {
		t.Fatalf("decode extracted input JSON rows=%d err=%v stdout=%q stderr=%q", len(report.Latest), err, stdout, stderr)
	}
	var host, result, tcp string
	for key, destination := range map[string]*string{
		"host": &host, "result": &result, fmt.Sprintf("tcp_%d", port): &tcp,
	} {
		if err := json.Unmarshal(report.Latest[0][key], destination); err != nil {
			t.Fatalf("decode extracted input latest.%s: %v; row=%v", key, err, report.Latest[0])
		}
	}
	if host != "127.0.0.1" || tcp != "open" || (code == 0 && result == "ERROR") || (code == 3 && result != "ERROR") {
		t.Fatalf("extracted input host/result/TCP=%q/%q/%q exit=%d stderr=%q", host, result, tcp, code, stderr)
	}
}

func requireNativeReleaseQualification(t *testing.T) {
	t.Helper()
	if os.Getenv(releaseQualifyEnv) != "1" {
		t.Skip("extracted release qualification is an explicit native CI gate")
	}
	if os.Getenv("CNMON_NATIVE_COMMAND") != "1" {
		t.Fatal("release qualification requires CNMON_NATIVE_COMMAND=1")
	}
	if binary := os.Getenv("CNMON_NATIVE_COMMAND_BINARY"); !filepath.IsAbs(binary) {
		t.Fatalf("release qualification requires an explicit absolute command binary path, got %q", binary)
	}
}

func runNativeReleaseCommand(t *testing.T, binary, directory, database, logPath string, args ...string) (int, []byte, []byte) {
	return runNativeReleaseCommandWithInput(t, binary, directory, database, logPath, nil, args...)
}

func runNativeReleaseCommandWithPath(t *testing.T, binary, directory, database, logPath, pathDirectory string, args ...string) (int, []byte, []byte) {
	return runNativeReleaseCommandWithEnvironment(t, binary, directory, database, logPath, nil, pathDirectory, args...)
}

func runNativeReleaseCommandWithInput(t *testing.T, binary, directory, database, logPath string, stdin io.Reader, args ...string) (int, []byte, []byte) {
	return runNativeReleaseCommandWithEnvironment(t, binary, directory, database, logPath, stdin, "", args...)
}

func runNativeReleaseCommandWithEnvironment(t *testing.T, binary, directory, database, logPath string, stdin io.Reader, pathDirectory string, args ...string) (int, []byte, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = directory
	if stdin != nil {
		command.Stdin = stdin
	}
	command.Env = nativeReleaseChildEnv(t, directory, database, logPath)
	if pathDirectory != "" {
		command.Env = nativeReleaseEnvWithPath(command.Env, pathDirectory)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.Bytes(), stderr.Bytes()
	}
	if ctx.Err() != nil {
		t.Fatalf("extracted command timed out after 45s: %v args=%q", ctx.Err(), args)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stdout.Bytes(), stderr.Bytes()
	}
	t.Fatalf("run extracted command: %v", err)
	return -1, nil, nil
}

func nativeReleaseEnvWithPath(environment []string, path string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(key, "PATH") {
			continue
		}
		result = append(result, item)
	}
	return append(result, "PATH="+path)
}

func releaseBinarySHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open qualified binary: %v", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("hash qualified binary: %v", err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func nativeReleaseChildEnv(t *testing.T, directory, history, log string) []string {
	t.Helper()
	env := nativeCommandEnv(t, directory, history, log)
	result := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(key), "CNMON_RELEASE_") {
			continue
		}
		result = append(result, item)
	}
	return result
}
