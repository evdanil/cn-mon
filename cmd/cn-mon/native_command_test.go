// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildNativeCommand builds the actual command binary once for a native test.
// Keep the build bounded so a broken CI toolchain cannot hang the native gate.
func buildNativeCommand(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("CNMON_NATIVE_COMMAND_BINARY"); configured != "" {
		path, err := filepath.Abs(configured)
		if err != nil {
			t.Fatalf("resolve prebuilt native command: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("prebuilt native command is unavailable: %s (%v)", path, err)
		}
		return path
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve native test source path")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	name := "cn-mon"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	ctx, cancel := contextWithBuildTimeout(t)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/cn-mon")
	command.Dir = repo
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GO111MODULE=on", "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build native cn-mon: %v\n%s", err, boundedNativeOutput(output))
	}
	return path
}

// contextWithBuildTimeout avoids coupling the native command build to a
// particular CI runner's wall-clock allowance.
func contextWithBuildTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func boundedNativeOutput(output []byte) string {
	const limit = 6000
	if len(output) > limit {
		output = output[len(output)-limit:]
	}
	return strings.TrimSpace(string(output))
}

// nativeCommandEnv isolates user configuration and history from the host
// account while retaining the platform's ordinary process environment.
func nativeCommandEnv(t *testing.T, dir, history, log string) []string {
	t.Helper()
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve native temp directory: %v", err)
	}
	history, err = filepath.Abs(history)
	if err != nil {
		t.Fatalf("resolve native history path: %v", err)
	}
	log, err = filepath.Abs(log)
	if err != nil {
		t.Fatalf("resolve native log path: %v", err)
	}
	env := os.Environ()
	filtered := make([]string, 0, len(env)+6)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if upper == "HOME" || upper == "USERPROFILE" || upper == "APPDATA" || upper == "LOCALAPPDATA" || strings.HasPrefix(upper, "CNMON_") {
			continue
		}
		filtered = append(filtered, item)
	}
	filtered = append(filtered,
		"HOME="+dir,
		"USERPROFILE="+dir,
		"APPDATA="+dir,
		"LOCALAPPDATA="+dir,
		"CNMON_NATIVE_HISTORY="+history,
		"CNMON_NATIVE_LOG="+log,
	)
	return filtered
}

func replaceNativeCommandEnv(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func requireNativeCommand(t *testing.T) {
	t.Helper()
	if os.Getenv("CNMON_NATIVE_COMMAND") != "1" {
		t.Skip("native command integration is an explicit platform CI gate")
	}
}
