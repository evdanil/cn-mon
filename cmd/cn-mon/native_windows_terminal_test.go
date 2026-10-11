//go:build native && windows

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/target"
	"golang.org/x/sys/windows"
)

var (
	nativeWriteConsoleInput = windowsKernel32.NewProc("WriteConsoleInputW")
	nativeGetConsoleCursor  = windowsKernel32.NewProc("GetConsoleCursorInfo")
	nativeSetConsoleCursor  = windowsKernel32.NewProc("SetConsoleCursorInfo")
	nativeReadConsoleOutput = windowsKernel32.NewProc("ReadConsoleOutputCharacterW")
	nativeReadConsoleAttrs  = windowsKernel32.NewProc("ReadConsoleOutputAttribute")
	nativeSetConsoleWindow  = windowsKernel32.NewProc("SetConsoleWindowInfo")
)

const nativeWindowsEnableVT = 0x0004
const nativeWindowsCursorFixtureSize = 37

type nativeConsoleCursorInfo struct {
	Size    uint32
	Visible uint32
}

type nativeConsoleInputRecord struct {
	EventType uint16
	Padding   uint16
	Event     [16]byte
}

type nativeConsoleKeyEvent struct {
	KeyDown         uint32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	UnicodeChar     uint16
	ControlKeyState uint32
}

func TestNativeCommandWindowsPrivateConsoleQRestoresConsole(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console.db")
	logPath := filepath.Join(directory, "private-console.log")
	stdoutPath := filepath.Join(directory, "product.stdout.json")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build private-console request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath,
		map[string]string{
			"CNMON_WINDOWS_CONSOLE_BOOTSTRAP":      "1",
			"CNMON_WINDOWS_PRODUCT_BINARY":         binary,
			"CNMON_WINDOWS_PRODUCT_ARGS":           string(encodedArgs),
			"CNMON_WINDOWS_PRODUCT_STDOUT":         stdoutPath,
			"CNMON_WINDOWS_CONSOLE_CURSOR_FIXTURE": "hidden-custom",
		})
	defer control.Close()

	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse bootstrap readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	if baselineCursor || baselineOutput&nativeWindowsEnableVT != 0 {
		t.Fatalf("custom private console baseline is cursor_visible=%t output_mode=%#x; want hidden cursor and VT disabled", baselineCursor, baselineOutput)
	}
	assertNativeWindowsCursor(t, ready, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool {
		return run.Status == "running" && run.Rounds >= 1
	}); err != nil {
		t.Fatalf("wait for completed loopback round: %v", err)
	}
	active := waitNativeWindowsConsoleActive(t, control, output, "command")
	var inputMode, outputMode uint32
	var cursorVisible bool
	if _, err := fmt.Sscanf(active, "CNMON_WINDOWS_CONSOLE_STATE state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursorVisible); err != nil {
		t.Fatalf("parse active console marker %q: %v", active, err)
	}
	if inputMode&windows.ENABLE_PROCESSED_INPUT == 0 || inputMode&(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) != 0 {
		t.Fatalf("active input console mode %#x does not preserve processed input while disabling line/echo", inputMode)
	}
	if outputMode&nativeWindowsEnableVT == 0 {
		t.Fatalf("active output console mode %#x lacks VT processing", outputMode)
	}
	if cursorVisible {
		t.Fatalf("active live frame left cursor visible (mode %#x)", outputMode)
	}
	initialTable := strings.Contains(active, "table_header=true")
	for _, command := range []string{"keyup-q", "alt-q", "unicode"} {
		if _, err := io.WriteString(control, command+"\n"); err != nil {
			t.Fatalf("send %s key event: %v", command, err)
		}
		_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
		state := requestNativeWindowsConsoleState(t, control, output)
		if strings.Contains(state, "table_header=true") != initialTable {
			t.Fatalf("ignored %s event changed table/grid view: before=%q after=%q", command, active, state)
		}
	}
	if _, err := io.WriteString(control, "lower-v\n"); err != nil {
		t.Fatalf("send lower-case v key: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	time.Sleep(750 * time.Millisecond)
	lowerState := requestNativeWindowsConsoleState(t, control, output)
	if strings.Contains(lowerState, "table_header=true") == initialTable {
		t.Fatalf("lower-case v did not toggle table/grid view: before=%q after=%q", active, lowerState)
	}
	if _, err := io.WriteString(control, "upper-V\n"); err != nil {
		t.Fatalf("send upper-case V key: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	time.Sleep(750 * time.Millisecond)
	upperState := requestNativeWindowsConsoleState(t, control, output)
	if strings.Contains(upperState, "table_header=true") != initialTable {
		t.Fatalf("upper-case V did not toggle view back: initial=%q lower=%q upper=%q", active, lowerState, upperState)
	}
	if _, err := io.WriteString(control, "tiny\n"); err != nil {
		t.Fatalf("resize private console to tiny dimensions: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	tiny := requestNativeWindowsConsoleState(t, control, output)
	if !strings.Contains(tiny, "size=3x2") {
		t.Fatalf("private console did not reach 3x2 tiny size: %q", tiny)
	}
	time.Sleep(750 * time.Millisecond)
	if _, err := io.WriteString(control, "restore-size\n"); err != nil {
		t.Fatalf("restore private console dimensions: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	if _, err := io.WriteString(control, "quit-q\n"); err != nil {
		t.Fatalf("send q key: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second)
	if !strings.Contains(finished, "exit=0") {
		t.Fatalf("elevated private-console command did not succeed: %q", finished)
	}
	after := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var restoredInput, restoredOutput uint32
	var restoredCursor bool
	if _, err := fmt.Sscanf(after, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &restoredInput, &restoredOutput, &restoredCursor); err != nil {
		t.Fatalf("parse restored console marker %q: %v", after, err)
	}
	if restoredInput != baselineInput || restoredOutput != baselineOutput || restoredCursor != baselineCursor {
		t.Fatalf("console restoration=%q; want initial modes %#x/%#x cursor_visible=%t", after, baselineInput, baselineOutput, baselineCursor)
	}
	assertNativeWindowsCursor(t, after, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	result, err := done()
	if err != nil {
		t.Fatalf("private-console bootstrap: %v; output=%s", err, result)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("read structured report from child stdout: %v", err)
	}
	var report struct {
		Latest  []map[string]any `json:"latest"`
		Summary []struct {
			Rounds int `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode private-console JSON: %v; stdout=%q", err, data)
	}
	if len(report.Latest) != 1 || report.Latest[0]["tcp_"+strconv.Itoa(port)] != "open" || len(report.Summary) != 1 || report.Summary[0].Rounds < 1 {
		t.Fatalf("private-console report=%+v, want open TCP and completed history", report)
	}
	storedRounds, err := assertNativeWindowsStoppedHistory(database, request.Key, "127.0.0.1", "127.0.0.1", port, "OK")
	if err != nil {
		t.Fatalf("private-console command history: %v", err)
	}
	if report.Summary[0].Rounds != storedRounds {
		t.Fatalf("JSON report rounds=%d, stored stopped rounds=%d", report.Summary[0].Rounds, storedRounds)
	}
	role, err := currentWindowsTokenFacts()
	if err != nil {
		t.Fatalf("query private-console test role: %v", err)
	}
	t.Logf("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE q=graceful report=complete input=restored output=restored cursor=restored history=stopped sid=%s", role.sid)
}

func TestNativeCommandWindowsPrivateConsoleQLeavesWriterActive(t *testing.T) {
	requireNativeCommand(t)
	writer, database, _, requestKey, listener := startWindowsRepeatingCommand(t, "console-follower-writer")
	defer listener.Close()
	defer writer.killIfRunning()
	if _, err := waitWindowsRun(t, database, requestKey, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > 0 }); err != nil {
		t.Fatalf("wait for writer's first round: %v", err)
	}
	directory := filepath.Dir(database)
	followerLog := filepath.Join(directory, "console-follower.log")
	followerStdout := filepath.Join(directory, "console-follower.stdout.json")
	followerArgs := []string{"--follow", requestKey[:8], "--format", "json", "--history-file", database, "--log-file", followerLog}
	encodedArgs, err := json.Marshal(followerArgs)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, followerLog, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP": "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":    buildNativeCommand(t),
		"CNMON_WINDOWS_PRODUCT_ARGS":      string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":    followerStdout,
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var followerPID int
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t", &followerPID, &baselineInput, &baselineOutput, &baselineCursor); err != nil || followerPID <= 0 {
		t.Fatalf("parse follower readiness %q: pid=%d err=%v", ready, followerPID, err)
	}
	active := waitNativeWindowsConsoleActive(t, control, output, "follower")
	var inputMode, outputMode uint32
	var cursor bool
	if _, err := fmt.Sscanf(active, "CNMON_WINDOWS_CONSOLE_STATE state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursor); err != nil {
		t.Fatalf("parse active follower console state %q: %v", active, err)
	}
	if inputMode&windows.ENABLE_PROCESSED_INPUT == 0 || inputMode&(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) != 0 || outputMode&nativeWindowsEnableVT == 0 || cursor {
		t.Fatalf("follower terminal modes are not active: %q", active)
	}
	if _, err := io.WriteString(control, "quit-q\n"); err != nil {
		t.Fatalf("send q to follower: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second)
	if !strings.Contains(finished, "exit=0") {
		t.Fatalf("follower q exit marker=%q", finished)
	}
	restored := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var restoredInput, restoredOutput uint32
	var restoredCursor bool
	if _, err := fmt.Sscanf(restored, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &restoredInput, &restoredOutput, &restoredCursor); err != nil {
		t.Fatalf("parse follower restoration %q: %v", restored, err)
	}
	if restoredInput != baselineInput || restoredOutput != baselineOutput || restoredCursor != baselineCursor {
		t.Fatalf("follower restoration=%q, want baseline", restored)
	}
	select {
	case <-writer.done:
		t.Fatalf("writer exited when follower received q: %v", writer.waitErr)
	default:
	}
	if _, err := waitWindowsRun(t, database, requestKey, func(run history.RunInfo) bool { return run.Status == "running" }); err != nil {
		t.Fatalf("writer history did not remain active after follower q: %v", err)
	}
	if _, err := done(); err != nil {
		t.Fatalf("follower bootstrap: %v", err)
	}
	if err := sendPrivateConsoleCtrlC(writer.cmd.Process.Pid); err != nil {
		t.Fatalf("stop writer after follower verification: %v", err)
	}
	if _, err := writer.waitFor(8 * time.Second); err != nil {
		t.Fatalf("writer graceful stop: %v", err)
	}
	role, err := currentWindowsTokenFacts()
	if err != nil {
		t.Fatalf("query follower test role: %v", err)
	}
	t.Logf("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE q=follower-only writer=still-running restored=true sid=%s", role.sid)
}

func TestNativeCommandWindowsPrivateConsoleHistoryErrorRestoresConsole(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	blockedParent := filepath.Join(directory, "blocked-parent")
	blockedParentContents := []byte("regular-file-blocking-history-directory")
	if err := os.WriteFile(blockedParent, blockedParentContents, 0o600); err != nil {
		t.Fatalf("create regular-file history parent blocker: %v", err)
	}
	database := filepath.Join(blockedParent, "history.db")
	logPath := filepath.Join(directory, "history-error.log")
	stdoutPath := filepath.Join(directory, "history-error.stdout.json")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP":      "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":         binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":           string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":         stdoutPath,
		"CNMON_WINDOWS_CONSOLE_CURSOR_FIXTURE": "hidden-custom",
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse bootstrap readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	assertNativeWindowsCursor(t, ready, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 30*time.Second)
	if !strings.Contains(finished, "exit=3") {
		t.Fatalf("history-error product exit marker=%q, want 3", finished)
	}
	restored := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var inputMode, outputMode uint32
	var cursor bool
	if _, err := fmt.Sscanf(restored, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursor); err != nil {
		t.Fatalf("parse history-error restoration %q: %v", restored, err)
	}
	if inputMode != baselineInput || outputMode != baselineOutput || cursor != baselineCursor {
		t.Fatalf("history-error restoration=%q; want baseline modes/cursor", restored)
	}
	assertNativeWindowsCursor(t, restored, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	if _, err := done(); err != nil {
		t.Fatalf("history-error bootstrap: %v", err)
	}
	blocker, err := os.ReadFile(blockedParent)
	if err != nil || !bytes.Equal(blocker, blockedParentContents) {
		t.Fatalf("history-open error changed file blocking parent creation: contents=%q err=%v", blocker, err)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read history-error log: %v", err)
	}
	if !bytes.Contains(logBytes, []byte("history disabled after storage error")) || !bytes.Contains(logBytes, []byte(blockedParent)) {
		t.Fatalf("history-error log does not preserve actual blocked-parent cause: %q", logBytes)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE error=history-open exit=3 input=restored output=restored cursor=restored blocked-parent=preserved cause=logged")
}

func TestNativeCommandWindowsPrivateConsoleFirstCtrlCRestoresConsole(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console-first-ctrlc.db")
	logPath := filepath.Join(directory, "private-console-first-ctrlc.log")
	stdoutPath := filepath.Join(directory, "private-console-first-ctrlc.stdout.json")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build private-console Ctrl+C request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP":      "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":         binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":           string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":         stdoutPath,
		"CNMON_WINDOWS_CONSOLE_CURSOR_FIXTURE": "hidden-custom",
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse bootstrap readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	assertNativeWindowsCursor(t, ready, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > 0 }); err != nil {
		t.Fatalf("wait for active round before first Ctrl+C: %v", err)
	}
	if err := sendPrivateConsoleCtrlC(childPID); err != nil {
		t.Fatalf("send first Ctrl+C: %v", err)
	}
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second)
	if !strings.Contains(finished, "exit=0") {
		t.Fatalf("first Ctrl+C exit marker=%q, want graceful exit 0", finished)
	}
	restored := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var inputMode, outputMode uint32
	var cursor bool
	if _, err := fmt.Sscanf(restored, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursor); err != nil {
		t.Fatalf("parse first-Ctrl+C restoration %q: %v", restored, err)
	}
	if inputMode != baselineInput || outputMode != baselineOutput || cursor != baselineCursor {
		t.Fatalf("first Ctrl+C restoration=%q; want baseline modes/cursor", restored)
	}
	assertNativeWindowsCursor(t, restored, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	if _, err := done(); err != nil {
		t.Fatalf("first-Ctrl+C bootstrap: %v", err)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "stopped" && run.Rounds > 0 }); err != nil {
		t.Fatalf("first Ctrl+C did not persist stopped history: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE ctrlc=first-graceful exit=0 history=stopped input=restored output=restored cursor=restored")
}

func TestNativeCommandWindowsPrivateConsoleUppercaseQRestoresConsole(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console-Q.db")
	logPath := filepath.Join(directory, "private-console-Q.log")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build uppercase-Q request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "table", "--theme", "dark", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP": "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":    binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":      string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":    "CONOUT$",
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse bootstrap readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	if !baselineCursor || baselineOutput&nativeWindowsEnableVT != 0 {
		t.Fatalf("table-report fixture baseline cursor_visible=%t output_mode=%#x; want visible cursor and VT disabled", baselineCursor, baselineOutput)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > 0 }); err != nil {
		t.Fatalf("wait for active round before Q: %v", err)
	}
	if _, err := io.WriteString(control, "quit-Q\n"); err != nil {
		t.Fatalf("send Q key: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second)
	if !strings.Contains(finished, "exit=0") {
		t.Fatalf("uppercase-Q private-console command did not succeed: %q", finished)
	}
	state := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var inputMode, outputMode uint32
	var cursor bool
	if _, err := fmt.Sscanf(state, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursor); err != nil {
		t.Fatalf("parse restored state %q: %v", state, err)
	}
	if inputMode != baselineInput || outputMode != baselineOutput || cursor != baselineCursor {
		t.Fatalf("uppercase-Q console restoration=%q, want baseline modes/cursor", state)
	}
	if !strings.Contains(state, "report_header=true") || !strings.Contains(state, "literal_escape=false") {
		t.Fatalf("final table/theme report was not rendered through the restored console: %q", state)
	}
	var headerAttr uint16
	var headerFound bool
	if _, err := fmt.Sscanf(state, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t fingerprint=%s size=%dx%d table_header=%t stop_note=%t report_header=%t literal_escape=%t header_attr=%x header_found=%t", new(uint32), new(uint32), new(bool), new(string), new(int), new(int), new(bool), new(bool), new(bool), new(bool), &headerAttr, &headerFound); err != nil || !headerFound {
		t.Fatalf("parse table header attribute from %q: %v", state, err)
	}
	_, noColorSet := os.LookupEnv("NO_COLOR")
	if noColorSet && headerAttr&0x0007 != baselineAttr&0x0007 {
		t.Fatalf("NO_COLOR was set but report header foreground changed: baseline=%#x header=%#x", baselineAttr, headerAttr)
	}
	if !noColorSet && headerAttr&0x0007 == baselineAttr&0x0007 {
		t.Fatalf("dark theme did not set a report header foreground: baseline=%#x header=%#x", baselineAttr, headerAttr)
	}
	if _, err := done(); err != nil {
		t.Fatalf("uppercase-Q bootstrap: %v", err)
	}
	if _, err := assertNativeWindowsStoppedHistory(database, request.Key, "127.0.0.1", "127.0.0.1", port, "OK"); err != nil {
		t.Fatalf("uppercase-Q history verification failed: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE key=Q exit=graceful report=table-theme console=rendered literal-escape=false input=restored output=restored cursor=restored history=stopped")
}

func TestNativeCommandWindowsPrivateConsoleNoColorQRestoresConsole(t *testing.T) {
	requireNativeCommand(t)
	t.Setenv("NO_COLOR", "")
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console-no-color.db")
	logPath := filepath.Join(directory, "private-console-no-color.log")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build NO_COLOR request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "table", "--theme", "dark", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP": "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":    binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":      string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":    "CONOUT$",
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse NO_COLOR bootstrap readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	if !baselineCursor || baselineOutput&nativeWindowsEnableVT != 0 {
		t.Fatalf("NO_COLOR table fixture baseline cursor_visible=%t output_mode=%#x; want visible cursor and VT disabled", baselineCursor, baselineOutput)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > 0 }); err != nil {
		t.Fatalf("wait for active NO_COLOR round: %v", err)
	}
	if _, err := io.WriteString(control, "quit-Q\n"); err != nil {
		t.Fatalf("send Q to NO_COLOR run: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	if finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second); !strings.Contains(finished, "exit=0") {
		t.Fatalf("NO_COLOR Q exit marker=%q", finished)
	}
	state := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var restoredInput, restoredOutput uint32
	var restoredCursor bool
	if _, err := fmt.Sscanf(state, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &restoredInput, &restoredOutput, &restoredCursor); err != nil {
		t.Fatalf("parse NO_COLOR restoration %q: %v", state, err)
	}
	if restoredInput != baselineInput || restoredOutput != baselineOutput || restoredCursor != baselineCursor {
		t.Fatalf("NO_COLOR console restoration=%q, want exact fixture modes/cursor", state)
	}
	if !strings.Contains(state, "report_header=true") || !strings.Contains(state, "literal_escape=false") {
		t.Fatalf("NO_COLOR final report was not rendered cleanly on console: %q", state)
	}
	var headerAttr uint16
	var headerFound bool
	if _, err := fmt.Sscanf(state, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t fingerprint=%s size=%dx%d table_header=%t stop_note=%t report_header=%t literal_escape=%t header_attr=%x header_found=%t", new(uint32), new(uint32), new(bool), new(string), new(int), new(int), new(bool), new(bool), new(bool), new(bool), &headerAttr, &headerFound); err != nil || !headerFound {
		t.Fatalf("parse NO_COLOR report header attribute from %q: %v", state, err)
	}
	if headerAttr&0x0007 != baselineAttr&0x0007 {
		t.Fatalf("empty NO_COLOR did not preserve console report foreground: baseline=%#x header=%#x", baselineAttr, headerAttr)
	}
	if _, err := done(); err != nil {
		t.Fatalf("NO_COLOR bootstrap: %v", err)
	}
	if _, err := assertNativeWindowsStoppedHistory(database, request.Key, "127.0.0.1", "127.0.0.1", port, "OK"); err != nil {
		t.Fatalf("NO_COLOR stopped history: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE key=Q report=NO_COLOR-empty console=rendered literal-escape=false input=restored output=restored cursor=restored history=stopped")
}

func TestNativeCommandWindowsPrivateConsoleOutputHandleLossPreservesRun(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console-output-loss.db")
	logPath := filepath.Join(directory, "private-console-output-loss.log")
	stdoutPath := filepath.Join(directory, "private-console-output-loss.stdout.json")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build output-handle-loss request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP":   "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":      binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":        string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":      stdoutPath,
		"CNMON_WINDOWS_PRODUCT_RAW_HANDLES": "1",
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse output-loss readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	if !baselineCursor || baselineOutput&nativeWindowsEnableVT != 0 {
		t.Fatalf("output-loss fixture baseline cursor_visible=%t output_mode=%#x; want visible cursor and VT disabled", baselineCursor, baselineOutput)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds >= 1 }); err != nil {
		t.Fatalf("wait for first round before output-handle loss: %v", err)
	}
	active := waitNativeWindowsConsoleActive(t, control, output, "output-loss command")
	if !strings.Contains(active, "state_ok=true") {
		t.Fatalf("product was not attached to a live console before handle loss: %q", active)
	}
	if _, err := io.WriteString(control, "close-stderr\n"); err != nil {
		t.Fatalf("request product stderr handle loss: %v", err)
	}
	closed := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_OUTPUT_HANDLE_LOST", 5*time.Second)
	if !strings.Contains(closed, "source_invalid=true") {
		t.Fatalf("stderr handle close was not confirmed: %q", closed)
	}
	postLossBaseline, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds >= 1 })
	if err != nil {
		t.Fatalf("read history baseline immediately after output-handle loss: %v", err)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > postLossBaseline.Rounds }); err != nil {
		t.Fatalf("monitor did not complete useful work after console output-handle loss: %v", err)
	}
	if _, err := io.WriteString(control, "quit-q\n"); err != nil {
		t.Fatalf("send q through still-attached real console input: %v", err)
	}
	_ = readNativeWindowsMarker(t, output, "CNMON_WINDOWS_KEY_INJECTED", 5*time.Second)
	if finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 20*time.Second); !strings.Contains(finished, "exit=0") {
		t.Fatalf("output-loss child exit marker=%q", finished)
	}
	lostState := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_OUTPUT_LOST_STATE", 5*time.Second)
	var afterInput, afterOutput uint32
	var afterCursor bool
	if _, err := fmt.Sscanf(lostState, "CNMON_WINDOWS_CONSOLE_OUTPUT_LOST_STATE state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &afterInput, &afterOutput, &afterCursor); err != nil {
		t.Fatalf("parse output-loss state %q: %v", lostState, err)
	}
	if afterInput != baselineInput {
		t.Fatalf("product did not restore surviving console input mode: got %#x want %#x", afterInput, baselineInput)
	}
	fixture := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_FIXTURE_RESTORED", 5*time.Second)
	if !strings.Contains(fixture, "mode_restored=true cursor_restored=true") {
		t.Fatalf("test bootstrap did not restore private console fixture after output loss: %q", fixture)
	}
	if _, err := done(); err != nil {
		t.Fatalf("output-loss bootstrap: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("read report captured independently of lost console output: %v", err)
	}
	var report struct {
		Latest  []map[string]any `json:"latest"`
		Summary []struct {
			Rounds int `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode post-loss report: %v; stdout=%q", err, data)
	}
	if len(report.Latest) != 1 || report.Latest[0]["tcp_"+strconv.Itoa(port)] != "open" || len(report.Summary) != 1 {
		t.Fatalf("post-loss report=%+v, want latest TCP-open row and summary", report)
	}
	storedRounds, err := assertNativeWindowsStoppedHistory(database, request.Key, "127.0.0.1", "127.0.0.1", port, "OK")
	if err != nil {
		t.Fatalf("post-loss stopped history: %v", err)
	}
	if report.Summary[0].Rounds != storedRounds || storedRounds < 2 {
		t.Fatalf("post-loss report rounds=%d, stored rounds=%d; want matching work including post-loss round", report.Summary[0].Rounds, storedRounds)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE output-loss=product-stderr-only useful-rounds>=2 q=input-alive report=complete history=stopped input=restored")
}

func TestNativeCommandWindowsPrivateConsoleSecondCtrlCWhileDisplayBlocked(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console-display-block.db")
	logPath := filepath.Join(directory, "private-console-display-block.log")
	stdoutPath := filepath.Join(directory, "private-console-display-block.stdout.json")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build blocked-display request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP":      "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":         binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":           string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":         stdoutPath,
		"CNMON_WINDOWS_CONSOLE_CURSOR_FIXTURE": "hidden-custom",
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse blocked-display readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	if baselineCursor || baselineOutput&nativeWindowsEnableVT != 0 {
		t.Fatalf("blocked-display baseline cursor_visible=%t output_mode=%#x; want hidden cursor and VT disabled", baselineCursor, baselineOutput)
	}
	assertNativeWindowsCursor(t, ready, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > 0 }); err != nil {
		t.Fatalf("wait for completed round before blocked display: %v", err)
	}
	lockDB, err := sql.Open("sqlite3", windowsSQLiteURI(database))
	if err != nil {
		t.Fatalf("open finalization lock connection: %v", err)
	}
	lockDB.SetMaxOpenConns(1)
	defer lockDB.Close()
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire finalization lock connection: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("hold SQLite finalization lock: %v", err)
	}
	if err := sendPrivateConsoleCtrlC(childPID); err != nil {
		t.Fatalf("send first real Ctrl+C: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := requestNativeWindowsConsoleState(t, control, output)
		if strings.Contains(state, "stop_note=true") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first Ctrl+C stop note not drawn before display block: %q", state)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := io.WriteString(control, "begin-pause-block\n"); err != nil {
		t.Fatalf("request actual console output suspension: %v", err)
	}
	blocked := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_WRITE_BLOCKED", 5*time.Second)
	var pauseRecordsWritten uint32
	_, pauseRecordErr := fmt.Sscanf(blocked, "CNMON_WINDOWS_CONSOLE_WRITE_BLOCKED pause_requested=true pause_records_written=%d", &pauseRecordsWritten)
	if !strings.Contains(blocked, "pause_requested=true") ||
		!strings.Contains(blocked, "write_pending=true") ||
		!strings.Contains(blocked, "metadata=true") ||
		!strings.Contains(blocked, "input_mode_restored=true") ||
		pauseRecordErr != nil || pauseRecordsWritten > 1 {
		t.Fatalf("console output suspension was not witnessed: %q", blocked)
	}
	if _, err := lockConn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatalf("release SQLite barrier while display remains blocked: %v", err)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "stopped" && run.Rounds > 0 }); err != nil {
		t.Fatalf("first interrupt did not persist stopped history after releasing DB barrier: %v", err)
	}
	if !nativeWindowsProcessIsRunning(uint32(childPID)) {
		t.Fatalf("product exited after persisted stop despite blocked final display")
	}
	for start := time.Now(); time.Since(start) < 750*time.Millisecond; {
		info, err := os.Stat(stdoutPath)
		if err != nil {
			t.Fatalf("stat independently captured report while display blocked: %v", err)
		}
		if info.Size() != 0 {
			t.Fatalf("final report escaped while LiveEnded display was blocked: %d bytes", info.Size())
		}
		if !nativeWindowsProcessIsRunning(uint32(childPID)) {
			t.Fatalf("product exited before second Ctrl+C while final display was blocked")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := sendPrivateConsoleCtrlC(childPID); err != nil {
		t.Fatalf("send second real Ctrl+C during blocked final display: %v", err)
	}
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 10*time.Second)
	if !strings.Contains(finished, "exit=130") {
		t.Fatalf("second Ctrl+C during blocked display exit marker=%q", finished)
	}
	restored := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var restoredInput, restoredOutput uint32
	var restoredCursor bool
	if _, err := fmt.Sscanf(restored, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &restoredInput, &restoredOutput, &restoredCursor); err != nil {
		t.Fatalf("parse blocked-display emergency restore: %q: %v", restored, err)
	}
	if restoredInput != baselineInput || restoredOutput != baselineOutput || restoredCursor != baselineCursor {
		t.Fatalf("blocked-display emergency restore=%q, want baseline modes/cursor", restored)
	}
	assertNativeWindowsCursor(t, restored, nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0})
	if _, err := done(); err != nil {
		t.Fatalf("blocked-display bootstrap: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE display-output=blocked console-suspend=pause write=pending metadata=responsive db=stopped second-ctrl-c=130 input=restored output=restored cursor=restored")
}

func TestNativeCommandWindowsPrivateConsoleSecondCtrlCRestoresDuringBlockedFinalization(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "private-console-second-ctrlc.db")
	logPath := filepath.Join(directory, "private-console-second-ctrlc.log")
	stdoutPath := filepath.Join(directory, "product-second-ctrlc.stdout.json")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build private-console Ctrl+C request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1"}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	control, output, done := startNativeWindowsConsoleBootstrap(t, directory, database, logPath, map[string]string{
		"CNMON_WINDOWS_CONSOLE_BOOTSTRAP": "1",
		"CNMON_WINDOWS_PRODUCT_BINARY":    binary,
		"CNMON_WINDOWS_PRODUCT_ARGS":      string(encodedArgs),
		"CNMON_WINDOWS_PRODUCT_STDOUT":    stdoutPath,
	})
	defer control.Close()
	ready := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_READY", 20*time.Second)
	var baselineInput, baselineOutput uint32
	var baselineCursor bool
	var childPID int
	var baselineAttr uint16
	if _, err := fmt.Sscanf(ready, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%x output_mode=%x cursor_visible=%t default_attr=%x", &childPID, &baselineInput, &baselineOutput, &baselineCursor, &baselineAttr); err != nil || childPID <= 0 {
		t.Fatalf("parse bootstrap readiness %q: pid=%d err=%v", ready, childPID, err)
	}
	if !baselineCursor || baselineOutput&nativeWindowsEnableVT != 0 {
		t.Fatalf("fresh private console baseline is cursor_visible=%t output_mode=%#x; want visible cursor and VT disabled", baselineCursor, baselineOutput)
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool { return run.Status == "running" && run.Rounds > 0 }); err != nil {
		t.Fatalf("wait for active round before Ctrl+C: %v", err)
	}
	lockDB, err := sql.Open("sqlite3", windowsSQLiteURI(database))
	if err != nil {
		t.Fatalf("open independent SQLite lock connection: %v", err)
	}
	lockDB.SetMaxOpenConns(1)
	defer lockDB.Close()
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire independent SQLite connection: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("hold database write lock: %v", err)
	}
	defer lockConn.ExecContext(context.Background(), "ROLLBACK")
	if err := sendPrivateConsoleCtrlC(childPID); err != nil {
		t.Fatalf("send first Ctrl+C: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := requestNativeWindowsConsoleState(t, control, output)
		if strings.Contains(state, "stop_note=true") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("graceful stop note not visible before second Ctrl+C; last state=%q", state)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := sendPrivateConsoleCtrlC(childPID); err != nil {
		t.Fatalf("send second Ctrl+C while finalization is blocked: %v", err)
	}
	finished := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CHILD_DONE", 10*time.Second)
	if !strings.Contains(finished, "exit=130") {
		t.Fatalf("second Ctrl+C exit marker=%q, want 130", finished)
	}
	restored := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_RESTORED", 5*time.Second)
	var inputMode, outputMode uint32
	var cursor bool
	if _, err := fmt.Sscanf(restored, "CNMON_WINDOWS_CONSOLE_RESTORED state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursor); err != nil {
		t.Fatalf("parse emergency restoration marker %q: %v", restored, err)
	}
	if inputMode != baselineInput || outputMode != baselineOutput || cursor != baselineCursor {
		t.Fatalf("emergency restoration=%q; want baseline modes/cursor", restored)
	}
	if _, err := done(); err != nil {
		t.Fatalf("second-Ctrl+C bootstrap: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE ctrlc=first-graceful second=hard-blocked-finalization=pass input=restored output=restored cursor=restored")
}

// TestNativeWindowsConsoleBootstrap is invoked only as an isolated test process
// created with CREATE_NEW_CONSOLE. Its non-nil process-local Ctrl+C handler is
// registered after that console transition; no inherited ignore attribute is
// installed before it launches the actual product child.
func TestNativeWindowsConsoleBootstrap(t *testing.T) {
	if os.Getenv("CNMON_WINDOWS_CONSOLE_BOOTSTRAP") != "1" {
		return
	}
	input, err := openNativeWindowsConsole("CONIN$", windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		t.Fatalf("open private console input: %v", err)
	}
	defer windows.CloseHandle(input)
	consoleOut, err := openNativeWindowsConsole("CONOUT$", windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		t.Fatalf("open private console output: %v", err)
	}
	defer windows.CloseHandle(consoleOut)
	var inputModeBeforeBootstrap, outputModeBeforeBootstrap uint32
	if err := windows.GetConsoleMode(input, &inputModeBeforeBootstrap); err != nil {
		t.Fatalf("read initial console input mode: %v", err)
	}
	if err := windows.GetConsoleMode(consoleOut, &outputModeBeforeBootstrap); err != nil {
		t.Fatalf("read initial console output mode: %v", err)
	}
	// CREATE_NEW_CONSOLE inherits runner console capabilities on hosted Windows
	// images. Normalize the private console fixture to VT-off, then record that
	// exact baseline so product cleanup must restore the controlled state.
	if outputModeBeforeBootstrap&nativeWindowsEnableVT != 0 {
		if err := windows.SetConsoleMode(consoleOut, outputModeBeforeBootstrap&^nativeWindowsEnableVT); err != nil {
			t.Fatalf("disable inherited VT mode in private console fixture: %v", err)
		}
	}
	if err := windows.GetConsoleMode(consoleOut, &outputModeBeforeBootstrap); err != nil {
		t.Fatalf("read normalized private console output mode: %v", err)
	}
	if outputModeBeforeBootstrap&nativeWindowsEnableVT != 0 {
		t.Fatalf("private console fixture retained VT mode after normalization: %#x", outputModeBeforeBootstrap)
	}
	var baselineCursor nativeConsoleCursorInfo
	cursorResult, _, cursorErr := nativeGetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&baselineCursor)))
	if cursorResult == 0 {
		if cursorErr == syscall.Errno(0) {
			cursorErr = errors.New("GetConsoleCursorInfo failed")
		}
		t.Fatalf("read private console cursor baseline: %v", cursorErr)
	}
	customHiddenCursor := os.Getenv("CNMON_WINDOWS_CONSOLE_CURSOR_FIXTURE") == "hidden-custom"
	if customHiddenCursor {
		baselineCursor = nativeConsoleCursorInfo{Size: nativeWindowsCursorFixtureSize, Visible: 0}
		cursorResult, _, cursorErr = nativeSetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&baselineCursor)))
		if cursorResult == 0 {
			if cursorErr == syscall.Errno(0) {
				cursorErr = errors.New("SetConsoleCursorInfo failed")
			}
			t.Fatalf("set hidden custom-size private console cursor fixture: %v", cursorErr)
		}
	} else if baselineCursor.Visible == 0 {
		baselineCursor.Visible = 1
		cursorResult, _, cursorErr = nativeSetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&baselineCursor)))
		if cursorResult == 0 {
			if cursorErr == syscall.Errno(0) {
				cursorErr = errors.New("SetConsoleCursorInfo failed")
			}
			t.Fatalf("normalize private console cursor baseline: %v", cursorErr)
		}
	}
	var observedCursor nativeConsoleCursorInfo
	cursorResult, _, cursorErr = nativeGetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&observedCursor)))
	if cursorResult == 0 || observedCursor != baselineCursor {
		t.Fatalf("private console cursor fixture=%+v, want %+v: %v", observedCursor, baselineCursor, cursorErr)
	}
	var initialScreen windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(consoleOut, &initialScreen); err != nil {
		t.Fatalf("read initial console dimensions: %v", err)
	}
	callback := syscall.NewCallback(func(event uint32) uintptr {
		if event == windowsCtrlCEvent {
			return 1
		}
		return 0
	})
	if err := callWindowsBool(windowsSetConsoleCtrlHandler, callback, 1); err != nil {
		t.Fatalf("register bootstrap's process-local Ctrl+C handler: %v", err)
	}
	defer callWindowsBool(windowsSetConsoleCtrlHandler, callback, 0)

	args := []string{}
	if err := json.Unmarshal([]byte(os.Getenv("CNMON_WINDOWS_PRODUCT_ARGS")), &args); err != nil {
		t.Fatalf("decode product args: %v", err)
	}
	var stdoutFile *os.File
	if os.Getenv("CNMON_WINDOWS_PRODUCT_STDOUT") == "CONOUT$" {
		stdoutHandle, err := duplicateNativeWindowsHandle(consoleOut)
		if err != nil {
			t.Fatalf("duplicate console handle for product stdout: %v", err)
		}
		stdoutFile = os.NewFile(uintptr(stdoutHandle), "CONOUT$")
	} else {
		stdoutFile, err = os.Create(os.Getenv("CNMON_WINDOWS_PRODUCT_STDOUT"))
		if err != nil {
			t.Fatalf("create product stdout capture: %v", err)
		}
	}
	stderrHandle, err := duplicateNativeWindowsHandle(consoleOut)
	if err != nil {
		stdoutFile.Close()
		t.Fatalf("duplicate console handle for product stderr: %v", err)
	}
	stdinHandle, err := duplicateNativeWindowsHandle(input)
	if err != nil {
		stdoutFile.Close()
		windows.CloseHandle(stderrHandle)
		t.Fatalf("duplicate console handle for product stdin: %v", err)
	}
	privateDirectory := os.Getenv("HOME")
	if privateDirectory == "" {
		stdoutFile.Close()
		windows.CloseHandle(stderrHandle)
		windows.CloseHandle(stdinHandle)
		t.Fatal("bootstrap has no private HOME directory")
	}
	stdinFile := os.NewFile(uintptr(stdinHandle), "CONIN$")
	stderrFile := os.NewFile(uintptr(stderrHandle), "CONOUT$")
	productEnv := nativeCommandEnv(t, privateDirectory,
		os.Getenv("CNMON_NATIVE_HISTORY"), os.Getenv("CNMON_NATIVE_LOG"))
	rawProduct := os.Getenv("CNMON_WINDOWS_PRODUCT_RAW_HANDLES") == "1"
	var childPID int
	var childWait func() error
	var childKill func() error
	var childRelease func() error
	var rawStderrHandle windows.Handle
	if rawProduct {
		process, err := startNativeWindowsRawProduct(t, os.Getenv("CNMON_WINDOWS_PRODUCT_BINARY"), args, productEnv, privateDirectory,
			stdinHandle, windows.Handle(stdoutFile.Fd()), stderrHandle)
		if err != nil {
			stdinFile.Close()
			stdoutFile.Close()
			stderrFile.Close()
			t.Fatalf("start raw-handle product in private console: %v", err)
		}
		childPID = int(process.ProcessId)
		rawStderrHandle = stderrHandle
		_ = windows.CloseHandle(process.Thread)
		childWait = func() error {
			if _, err := windows.WaitForSingleObject(process.Process, windows.INFINITE); err != nil {
				return fmt.Errorf("wait for product process: %w", err)
			}
			var code uint32
			if err := windows.GetExitCodeProcess(process.Process, &code); err != nil {
				return fmt.Errorf("read product exit code: %w", err)
			}
			if code != 0 {
				return nativeWindowsProcessExitError(code)
			}
			return nil
		}
		childKill = func() error { return windows.TerminateProcess(process.Process, 1) }
		childRelease = func() error { return windows.CloseHandle(process.Process) }
	} else {
		product := exec.Command(os.Getenv("CNMON_WINDOWS_PRODUCT_BINARY"), args...)
		product.Dir = privateDirectory
		product.Env = productEnv
		product.Stdin = stdinFile
		product.Stdout = stdoutFile
		product.Stderr = stderrFile
		if err := product.Start(); err != nil {
			stdinFile.Close()
			stdoutFile.Close()
			stderrFile.Close()
			t.Fatalf("start product in private console: %v", err)
		}
		childPID = product.Process.Pid
		childWait = product.Wait
		childKill = product.Process.Kill
		childRelease = product.Process.Release
	}
	_ = stdinFile.Close()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	fmt.Fprintf(os.Stdout, "CNMON_WINDOWS_CONSOLE_READY pid=%d private_console=true input_mode=%08x output_mode=%08x cursor_visible=%t default_attr=%04x cursor_size=%d\n", childPID, inputModeBeforeBootstrap, outputModeBeforeBootstrap, baselineCursor.Visible != 0, initialScreen.Attributes, baselineCursor.Size)
	childDone := make(chan error, 1)
	go func() { childDone <- childWait() }()
	childFinished := false
	outputHandleLost := false
	consoleOutputMayBeSuspended := false
	var consoleWriteDone chan error
	defer func() {
		if !childFinished {
			_ = childKill()
		}
		if consoleOutputMayBeSuspended {
			_ = resumeNativeWindowsConsoleOutput(input)
			consoleOutputMayBeSuspended = false
		}
		if consoleWriteDone != nil {
			select {
			case <-consoleWriteDone:
			case <-time.After(2 * time.Second):
			}
		}
		if !childFinished {
			select {
			case <-childDone:
			case <-time.After(5 * time.Second):
				_ = childRelease()
			}
		}
	}()
	commands := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			commands <- scanner.Text()
		}
		close(commands)
	}()
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case err := <-childDone:
			childFinished = true
			_ = childRelease()
			if consoleOutputMayBeSuspended {
				if err := resumeNativeWindowsConsoleOutput(input); err != nil {
					t.Fatalf("resume private console after child exit: %v", err)
				}
				consoleOutputMayBeSuspended = false
			}
			if consoleWriteDone != nil {
				select {
				case writeErr := <-consoleWriteDone:
					if writeErr != nil {
						t.Fatalf("drain console write after output resume: %v", writeErr)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("console write remained blocked after resuming output")
				}
			}
			stdoutFile.Close()
			exit := 0
			if err != nil {
				var processExit *exec.ExitError
				if errors.As(err, &processExit) {
					exit = processExit.ExitCode()
				} else if processExit, ok := err.(nativeWindowsProcessExitError); ok {
					exit = int(processExit)
				} else {
					t.Fatalf("wait for product: %v", err)
				}
			}
			fmt.Fprintf(os.Stdout, "CNMON_WINDOWS_CHILD_DONE exit=%d\n", exit)
			if outputHandleLost {
				printNativeWindowsConsoleState("CNMON_WINDOWS_CONSOLE_OUTPUT_LOST_STATE", input, consoleOut)
				if err := restoreNativeWindowsConsoleFixture(consoleOut, outputModeBeforeBootstrap, baselineCursor); err != nil {
					t.Fatalf("restore private console fixture after output-handle loss: %v", err)
				}
				fmt.Fprintln(os.Stdout, "CNMON_WINDOWS_CONSOLE_FIXTURE_RESTORED mode_restored=true cursor_restored=true")
			} else {
				printNativeWindowsConsoleState("CNMON_WINDOWS_CONSOLE_RESTORED", input, consoleOut)
			}
			return
		case command, ok := <-commands:
			if !ok {
				_ = childKill()
				t.Fatal("private-console controller closed before product finished")
			}
			switch command {
			case "close-stderr":
				if rawStderrHandle == 0 {
					t.Fatal("close-stderr requires the raw explicit-handle product launcher")
				}
				if err := closeNativeWindowsChildHandle(uint32(childPID), rawStderrHandle); err != nil {
					t.Fatalf("close and verify product stderr handle: %v", err)
				}
				outputHandleLost = true
				fmt.Fprintln(os.Stdout, "CNMON_WINDOWS_OUTPUT_HANDLE_LOST stream=stderr source_invalid=true")
			case "begin-pause-block":
				if consoleOutputMayBeSuspended {
					t.Fatal("console output suspension is already active")
				}
				pauseWritten, suspended, err := suspendNativeWindowsConsoleOutput(input)
				if suspended {
					consoleOutputMayBeSuspended = true
				}
				if err != nil {
					t.Fatalf("suspend actual console output with VK_PAUSE: %v", err)
				}
				consoleWriteDone = make(chan error, 1)
				go func() { consoleWriteDone <- writeNativeWindowsConsoleProbe(consoleOut) }()
				select {
				case writeErr := <-consoleWriteDone:
					t.Fatalf("real console write completed despite VK_PAUSE suspension: %v", writeErr)
				case <-time.After(500 * time.Millisecond):
					var inputMode, outputMode uint32
					var cursor nativeConsoleCursorInfo
					if err := windows.GetConsoleMode(input, &inputMode); err != nil {
						t.Fatalf("query input metadata while WriteConsole is blocked: %v", err)
					}
					if err := windows.GetConsoleMode(consoleOut, &outputMode); err != nil {
						t.Fatalf("query output metadata while WriteConsole is blocked: %v", err)
					}
					cursorResult, _, cursorErr := nativeGetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&cursor)))
					if cursorResult == 0 {
						t.Fatalf("query cursor metadata while WriteConsole is blocked: %v", cursorErr)
					}
					if inputMode&windows.ENABLE_LINE_INPUT != 0 {
						t.Fatalf("fixture did not restore active input mode while console output was suspended: %#x", inputMode)
					}
					fmt.Fprintf(os.Stdout, "CNMON_WINDOWS_CONSOLE_WRITE_BLOCKED pause_requested=true pause_records_written=%d write_pending=true metadata=true input_mode_restored=true input_mode=%08x output_mode=%08x cursor_visible=%t\n", pauseWritten, inputMode, outputMode, cursor.Visible != 0)
				}
			case "q":
				if err := writeNativeWindowsConsoleKey(input, 'q', 'Q', true); err != nil {
					t.Fatalf("inject q key-down: %v", err)
				}
				if err := writeNativeWindowsConsoleKey(input, 'q', 'Q', false); err != nil {
					t.Fatalf("inject q key-up: %v", err)
				}
			case "keyup-q":
				if err := writeNativeWindowsConsoleKey(input, 'q', 'Q', false); err != nil {
					t.Fatalf("inject q key-up: %v", err)
				}
			case "alt-q":
				if err := writeNativeWindowsConsoleKeyState(input, 'q', 'Q', true, 0x0003); err != nil {
					t.Fatalf("inject Alt+q key-down: %v", err)
				}
				if err := writeNativeWindowsConsoleKeyState(input, 'q', 'Q', false, 0x0003); err != nil {
					t.Fatalf("inject Alt+q key-up: %v", err)
				}
			case "unicode":
				if err := writeNativeWindowsConsoleKey(input, 'Ω', 'O', true); err != nil {
					t.Fatalf("inject Unicode key-down: %v", err)
				}
				if err := writeNativeWindowsConsoleKey(input, 'Ω', 'O', false); err != nil {
					t.Fatalf("inject Unicode key-up: %v", err)
				}
			case "lower-v", "upper-V":
				char, vk := rune('v'), uint16('V')
				if command == "upper-V" {
					char, vk = 'V', 'V'
				}
				if err := writeNativeWindowsConsoleKey(input, char, vk, true); err != nil {
					t.Fatalf("inject %s key-down: %v", command, err)
				}
				if err := writeNativeWindowsConsoleKey(input, char, vk, false); err != nil {
					t.Fatalf("inject %s key-up: %v", command, err)
				}
			case "tiny":
				window := windows.SmallRect{Left: initialScreen.Window.Left, Top: initialScreen.Window.Top, Right: initialScreen.Window.Left + 2, Bottom: initialScreen.Window.Top + 1}
				if err := setNativeWindowsConsoleWindow(consoleOut, window); err != nil {
					t.Fatalf("set tiny 3x2 console window: %v", err)
				}
			case "restore-size":
				if err := setNativeWindowsConsoleWindow(consoleOut, initialScreen.Window); err != nil {
					t.Fatalf("restore console window dimensions: %v", err)
				}
			case "quit-q":
				if err := writeNativeWindowsConsoleKey(input, 'q', 'Q', true); err != nil {
					t.Fatalf("inject q key-down: %v", err)
				}
				if err := writeNativeWindowsConsoleKey(input, 'q', 'Q', false); err != nil {
					t.Fatalf("inject q key-up: %v", err)
				}
			case "quit-Q":
				if err := writeNativeWindowsConsoleKey(input, 'Q', 'Q', true); err != nil {
					t.Fatalf("inject Q key-down: %v", err)
				}
				if err := writeNativeWindowsConsoleKey(input, 'Q', 'Q', false); err != nil {
					t.Fatalf("inject Q key-up: %v", err)
				}
			case "state":
				printNativeWindowsConsoleState("CNMON_WINDOWS_CONSOLE_STATE", input, consoleOut)
			default:
				t.Fatalf("unknown bootstrap command %q", command)
			}
			if command != "state" {
				fmt.Fprintf(os.Stdout, "CNMON_WINDOWS_KEY_INJECTED key=%s down_up=true\n", command)
			}
		case <-timer.C:
			_ = childKill()
			t.Fatal("product exceeded private-console timeout")
		}
	}
}

func assertNativeWindowsStoppedHistory(filename, key, host, address string, port int, icmpState string) (int, error) {
	store, err := history.OpenReader(context.Background(), filename)
	if err != nil {
		return 0, fmt.Errorf("open read-only stopped history: %w", err)
	}
	if store == nil {
		return 0, errors.New("read-only stopped history is missing")
	}
	var count int
	viewErr := store.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info == nil || info.NewestRun == nil || info.NewestRun.Status != "stopped" || info.NewestRun.Rounds < 1 || info.Rounds != info.NewestRun.Rounds {
			return fmt.Errorf("persisted stopped request/run = %+v, want a stopped run with at least one matching round", info)
		}
		if info.NewestRun.RequestID != info.ID {
			return fmt.Errorf("stopped run request ID=%d, want %d", info.NewestRun.RequestID, info.ID)
		}
		var after *history.RoundCursor
		for {
			page, err := snapshot.Rounds(info.ID, history.RoundFilter{After: after, Limit: 50})
			if err != nil {
				return err
			}
			for _, round := range page.Items {
				if round.RunID != info.NewestRun.ID || len(round.Rows) != 1 {
					return fmt.Errorf("stopped round %d has run=%q rows=%d, want run=%q with one canonical row", round.ID, round.RunID, len(round.Rows), info.NewestRun.ID)
				}
				row := round.Rows[0]
				if row.Host != host || row.Address != address || row.Result != icmpState || row.TCP[port] != "open" {
					return fmt.Errorf("stopped round %d row=%+v, want host=%q address=%q ICMP=%q TCP=open", round.ID, row, host, address, icmpState)
				}
				count++
			}
			if page.Next == nil {
				break
			}
			after = page.Next
		}
		if int64(count) != info.NewestRun.Rounds {
			return fmt.Errorf("read %d stopped rounds, run reports %d", count, info.NewestRun.Rounds)
		}
		return nil
	})
	closeErr := store.Close()
	if viewErr != nil {
		return 0, viewErr
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close read-only stopped history: %w", closeErr)
	}
	return count, nil
}

type nativeWindowsProcessExitError uint32

func (e nativeWindowsProcessExitError) Error() string {
	return fmt.Sprintf("product exited with code %d", uint32(e))
}

func startNativeWindowsRawProduct(t *testing.T, binary string, args, env []string, directory string, stdin, stdout, stderr windows.Handle) (windows.ProcessInformation, error) {
	t.Helper()
	handles := []windows.Handle{stdin, stdout, stderr}
	for _, handle := range handles {
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return windows.ProcessInformation{}, fmt.Errorf("mark standard handle inheritable: %w", err)
		}
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("allocate process handle allowlist: %w", err)
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return windows.ProcessInformation{}, fmt.Errorf("set process standard-handle allowlist: %w", err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{
		Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES,
		StdInput: stdin, StdOutput: stdout, StdErr: stderr,
	}, ProcThreadAttributeList: attributes.List()}
	application, err := windows.UTF16PtrFromString(binary)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{binary}, args...)))
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	workingDirectory, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	orderedEnv := append([]string(nil), env...)
	sort.Slice(orderedEnv, func(i, j int) bool { return strings.ToUpper(orderedEnv[i]) < strings.ToUpper(orderedEnv[j]) })
	envText := strings.Join(orderedEnv, "\x00") + "\x00\x00"
	envBlock := utf16.Encode([]rune(envText))
	var envPointer *uint16
	if len(envBlock) != 0 {
		envPointer = &envBlock[0]
	}
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(application, commandLine, nil, nil, true, flags, envPointer, workingDirectory, &startup.StartupInfo, &process); err != nil {
		return windows.ProcessInformation{}, err
	}
	return process, nil
}

func closeNativeWindowsChildHandle(pid uint32, handle windows.Handle) error {
	process, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, pid)
	if err != nil {
		return fmt.Errorf("open product for handle close: %w", err)
	}
	defer windows.CloseHandle(process)
	var duplicate windows.Handle
	closeErr := windows.DuplicateHandle(process, handle, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_CLOSE_SOURCE)
	if duplicate != 0 {
		_ = windows.CloseHandle(duplicate)
	}
	var probe windows.Handle
	probeErr := windows.DuplicateHandle(process, handle, windows.CurrentProcess(), &probe, 0, false, 0)
	if probeErr == nil {
		_ = windows.CloseHandle(probe)
		return errors.New("product stderr handle remained duplicable after close-source request")
	}
	if !errors.Is(probeErr, windows.ERROR_INVALID_HANDLE) {
		return fmt.Errorf("product stderr handle is not valid after close-source request, but probe failed unexpectedly: close=%v probe=%w", closeErr, probeErr)
	}
	return nil
}

func nativeWindowsProcessIsRunning(pid uint32) bool {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	var code uint32
	return windows.GetExitCodeProcess(process, &code) == nil && code == 259 // STILL_ACTIVE
}

func suspendNativeWindowsConsoleOutput(input windows.Handle) (uint32, bool, error) {
	var activeMode uint32
	if err := windows.GetConsoleMode(input, &activeMode); err != nil {
		return 0, false, fmt.Errorf("read active console input mode: %w", err)
	}
	if activeMode&windows.ENABLE_LINE_INPUT != 0 {
		return 0, false, fmt.Errorf("expected active cbreak input mode, got %#x", activeMode)
	}
	if err := windows.SetConsoleMode(input, activeMode|windows.ENABLE_LINE_INPUT); err != nil {
		return 0, false, fmt.Errorf("temporarily enable line input for VK_PAUSE: %w", err)
	}
	written, writeErr := writeNativeWindowsConsoleControlKey(input, 0x13) // VK_PAUSE is intercepted by conhost while line input is enabled.
	suspended := writeErr == nil
	restoreErr := restoreNativeWindowsConsoleInputMode(input, activeMode)
	if writeErr != nil {
		return written, suspended, writeErr
	}
	if restoreErr != nil {
		return written, suspended, restoreErr
	}
	if written > 1 {
		return written, suspended, fmt.Errorf("WriteConsoleInputW reported more records than requested for VK_PAUSE: written=%d", written)
	}
	return written, suspended, nil
}

func resumeNativeWindowsConsoleOutput(input windows.Handle) error {
	var activeMode uint32
	if err := windows.GetConsoleMode(input, &activeMode); err != nil {
		return fmt.Errorf("read console input mode before resume: %w", err)
	}
	if err := windows.SetConsoleMode(input, activeMode|windows.ENABLE_LINE_INPUT); err != nil {
		return fmt.Errorf("temporarily enable line input for resume key: %w", err)
	}
	_, writeErr := writeNativeWindowsConsoleControlKey(input, 0x20) // Space is consumed by conhost to clear CONSOLE_SUSPENDED.
	restoreErr := restoreNativeWindowsConsoleInputMode(input, activeMode)
	if writeErr != nil {
		return writeErr
	}
	if restoreErr != nil {
		return restoreErr
	}
	return nil
}

func restoreNativeWindowsConsoleInputMode(input windows.Handle, mode uint32) error {
	if err := windows.SetConsoleMode(input, mode); err != nil {
		return fmt.Errorf("restore console input mode: %w", err)
	}
	var observed uint32
	if err := windows.GetConsoleMode(input, &observed); err != nil || observed != mode {
		return fmt.Errorf("console input mode after restore=%#x want %#x: %v", observed, mode, err)
	}
	return nil
}

func writeNativeWindowsConsoleControlKey(input windows.Handle, virtualKey uint16) (uint32, error) {
	var event nativeConsoleInputRecord
	event.EventType = 0x0001 // KEY_EVENT
	key := (*nativeConsoleKeyEvent)(unsafe.Pointer(&event.Event[0]))
	key.KeyDown = 1
	key.RepeatCount = 1
	key.VirtualKeyCode = virtualKey
	written := uint32(0)
	result, _, callErr := nativeWriteConsoleInput.Call(uintptr(input), uintptr(unsafe.Pointer(&event)), 1, uintptr(unsafe.Pointer(&written)))
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("WriteConsoleInputW failed for conhost control key")
		}
		return written, callErr
	}
	if written > 1 {
		return written, fmt.Errorf("WriteConsoleInputW reported %d records for one conhost control key", written)
	}
	return written, nil
}

func writeNativeWindowsConsoleProbe(consoleOut windows.Handle) error {
	text := strings.Repeat("DISPLAY-BLOCK-PROBE ", 128)
	encoded, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	var written uint32
	return windows.WriteConsole(consoleOut, &encoded[0], uint32(len(encoded)-1), &written, nil)
}

func restoreNativeWindowsConsoleFixture(consoleOut windows.Handle, outputMode uint32, cursor nativeConsoleCursorInfo) error {
	if err := windows.SetConsoleMode(consoleOut, outputMode); err != nil {
		return fmt.Errorf("set output mode: %w", err)
	}
	var observedMode uint32
	if err := windows.GetConsoleMode(consoleOut, &observedMode); err != nil || observedMode != outputMode {
		return fmt.Errorf("output mode after restore=%#x want %#x: %v", observedMode, outputMode, err)
	}
	result, _, callErr := nativeSetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&cursor)))
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("SetConsoleCursorInfo failed")
		}
		return fmt.Errorf("set cursor visibility: %w", callErr)
	}
	var observedCursor nativeConsoleCursorInfo
	result, _, callErr = nativeGetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&observedCursor)))
	if result == 0 || observedCursor.Visible != cursor.Visible || observedCursor.Size != cursor.Size {
		return fmt.Errorf("cursor after restore=%+v want %+v: %v", observedCursor, cursor, callErr)
	}
	return nil
}

func startNativeWindowsConsoleBootstrap(t *testing.T, directory, database, logPath string, extra map[string]string) (io.WriteCloser, chan []byte, func() ([]byte, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeWindowsConsoleBootstrap$")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsCreateNewConsole}
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	for key, value := range extra {
		command.Env = append(command.Env, key+"="+value)
	}
	control, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatalf("open private-console bootstrap command pipe: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("open private-console bootstrap result pipe: %v", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		t.Fatalf("open private-console bootstrap error pipe: %v", err)
	}
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start private-console bootstrap: %v", err)
	}
	lines := make(chan []byte, 32)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
	}()
	var stderrBytes bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		defer readers.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			_, _ = stderrBytes.Write(line)
			_, _ = stderrBytes.Write([]byte{'\n'})
			// Diagnostics are opportunistic so they can never back-pressure the
			// bootstrap's stdout protocol or cleanup path.
			diagnostic := append([]byte("BOOTSTRAP STDERR: "), line...)
			select {
			case lines <- diagnostic:
			default:
			}
		}
	}()
	go func() {
		readers.Wait()
		close(lines)
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	collected := false
	var result []byte
	var waitErr error
	collect := func(err error) {
		waitErr = err
		collected = true
		cancel()
		<-stderrDone
		result = append([]byte(nil), stderrBytes.Bytes()...)
	}
	done := func() ([]byte, error) {
		_ = control.Close()
		collect(<-waitDone)
		return result, waitErr
	}
	t.Cleanup(func() {
		_ = control.Close()
		if collected {
			return
		}
		select {
		case err := <-waitDone:
			collect(err)
		case <-time.After(5 * time.Second):
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			select {
			case err := <-waitDone:
				collect(err)
			case <-time.After(5 * time.Second):
				cancel()
				t.Errorf("private-console bootstrap did not exit after controller EOF and forced termination")
			}
		}
	})
	return control, lines, done
}

func readNativeWindowsMarker(t *testing.T, lines <-chan []byte, marker string, timeout time.Duration) string {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	recent := make([]string, 0, 12)
	remember := func(line []byte) {
		const maxLine = 512
		if len(line) > maxLine {
			line = append(append([]byte(nil), line[:maxLine]...), []byte("...[truncated]")...)
		}
		recent = append(recent, string(line))
		if len(recent) > 12 {
			recent = recent[len(recent)-12:]
		}
	}
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("bootstrap exited before marker %q; recent helper output: %q", marker, recent)
			}
			if bytes.HasPrefix(line, []byte(marker)) {
				return string(line)
			}
			remember(line)
		case <-timer.C:
			t.Fatalf("timed out waiting for private-console marker %q; recent helper output: %q", marker, recent)
		}
	}
}

func openNativeWindowsConsole(name string, access uint32) (windows.Handle, error) {
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(namePointer, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
}

func duplicateNativeWindowsHandle(handle windows.Handle) (windows.Handle, error) {
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &duplicate, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return windows.InvalidHandle, err
	}
	return duplicate, nil
}

func printNativeWindowsConsoleState(marker string, input, consoleOut windows.Handle) {
	var inputMode, outputMode uint32
	var cursor nativeConsoleCursorInfo
	inputErr := windows.GetConsoleMode(input, &inputMode)
	outputErr := windows.GetConsoleMode(consoleOut, &outputMode)
	cursorResult, _, cursorErr := nativeGetConsoleCursor.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&cursor)))
	fingerprint, width, height, text, headerAttr, headerFound, fingerprintErr := nativeWindowsConsoleFingerprint(consoleOut)
	valid := inputErr == nil && outputErr == nil && cursorResult != 0 && fingerprintErr == nil
	fmt.Fprintf(os.Stdout, "%s state_ok=%t input_mode=%08x output_mode=%08x cursor_visible=%t fingerprint=%016x size=%dx%d table_header=%t stop_note=%t report_header=%t literal_escape=%t header_attr=%04x header_found=%t cursor_size=%d\n", marker, valid, inputMode, outputMode, cursor.Visible != 0, fingerprint, width, height, strings.Contains(text, "Latest"), strings.Contains(text, "stopping; preparing the final summary"), strings.Contains(text, "Host"), strings.ContainsRune(text, '\x1b'), headerAttr, headerFound, cursor.Size)
	_ = cursorErr
}

func assertNativeWindowsCursor(t *testing.T, marker string, want nativeConsoleCursorInfo) {
	t.Helper()
	if !strings.Contains(marker, fmt.Sprintf("cursor_visible=%t", want.Visible != 0)) {
		t.Fatalf("cursor visibility in %q does not match want %+v", marker, want)
	}
	var size uint32
	found := false
	for _, field := range strings.Fields(marker) {
		if strings.HasPrefix(field, "cursor_size=") {
			parsed, err := strconv.ParseUint(strings.TrimPrefix(field, "cursor_size="), 10, 32)
			if err != nil {
				t.Fatalf("parse cursor size field %q: %v", field, err)
			}
			size = uint32(parsed)
			found = true
			break
		}
	}
	if !found || size != want.Size {
		t.Fatalf("cursor metadata in %q has size=%d found=%t, want %+v", marker, size, found, want)
	}
}

func writeNativeWindowsConsoleKey(input windows.Handle, char rune, virtualKey uint16, keyDown bool) error {
	return writeNativeWindowsConsoleKeyState(input, char, virtualKey, keyDown, 0)
}

func writeNativeWindowsConsoleKeyState(input windows.Handle, char rune, virtualKey uint16, keyDown bool, controlState uint32) error {
	var event nativeConsoleInputRecord
	event.EventType = 0x0001 // KEY_EVENT
	key := (*nativeConsoleKeyEvent)(unsafe.Pointer(&event.Event[0]))
	if keyDown {
		key.KeyDown = 1
	}
	key.RepeatCount = 1
	key.VirtualKeyCode = virtualKey
	key.UnicodeChar = uint16(char)
	key.ControlKeyState = controlState
	written := uint32(0)
	result, _, callErr := nativeWriteConsoleInput.Call(uintptr(input), uintptr(unsafe.Pointer(&event)), 1, uintptr(unsafe.Pointer(&written)))
	if result == 0 || written != 1 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("WriteConsoleInputW wrote %d records", written)
	}
	return nil
}

func requestNativeWindowsConsoleState(t *testing.T, control io.Writer, output <-chan []byte) string {
	t.Helper()
	if _, err := io.WriteString(control, "state\n"); err != nil {
		t.Fatalf("request active console state: %v", err)
	}
	state := readNativeWindowsMarker(t, output, "CNMON_WINDOWS_CONSOLE_STATE", 5*time.Second)
	if !strings.Contains(state, "state_ok=true") {
		t.Fatalf("console inspection failed: %q", state)
	}
	return state
}

func waitNativeWindowsConsoleActive(t *testing.T, control io.Writer, output <-chan []byte, name string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		state := requestNativeWindowsConsoleState(t, control, output)
		var inputMode, outputMode uint32
		var cursor bool
		if _, err := fmt.Sscanf(state, "CNMON_WINDOWS_CONSOLE_STATE state_ok=true input_mode=%x output_mode=%x cursor_visible=%t", &inputMode, &outputMode, &cursor); err != nil {
			t.Fatalf("parse active %s console state %q: %v", name, state, err)
		}
		if inputMode&windows.ENABLE_PROCESSED_INPUT != 0 && inputMode&(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) == 0 && outputMode&nativeWindowsEnableVT != 0 && !cursor {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not enter active terminal state within 15 seconds: %q", name, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func nativeWindowsConsoleFingerprint(consoleOut windows.Handle) (uint64, int, int, string, uint16, bool, error) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(consoleOut, &info); err != nil {
		return 0, 0, 0, "", 0, false, err
	}
	width := int(info.Window.Right-info.Window.Left) + 1
	height := int(info.Window.Bottom-info.Window.Top) + 1
	if width < 1 || height < 1 {
		return 0, width, height, "", 0, false, fmt.Errorf("invalid visible console size %dx%d", width, height)
	}
	hasher := fnv.New64a()
	chars := make([]uint16, width)
	attrs := make([]uint16, width)
	text := make([]rune, 0, width*height)
	var headerAttr uint16
	headerFound := false
	for row := 0; row < height; row++ {
		x, y := info.Window.Left, info.Window.Top+int16(row)
		coord := uint32(uint16(x)) | uint32(uint16(y))<<16
		var read uint32
		result, _, callErr := nativeReadConsoleOutput.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&chars[0])), uintptr(width), uintptr(coord), uintptr(unsafe.Pointer(&read)))
		if result == 0 || read != uint32(width) {
			if callErr == syscall.Errno(0) {
				callErr = fmt.Errorf("read %d of %d console cells", read, width)
			}
			return 0, width, height, "", 0, false, callErr
		}
		var attrsRead uint32
		result, _, callErr = nativeReadConsoleAttrs.Call(uintptr(consoleOut), uintptr(unsafe.Pointer(&attrs[0])), uintptr(width), uintptr(coord), uintptr(unsafe.Pointer(&attrsRead)))
		if result == 0 || attrsRead != uint32(width) {
			if callErr == syscall.Errno(0) {
				callErr = fmt.Errorf("read %d of %d console attributes", attrsRead, width)
			}
			return 0, width, height, "", 0, false, callErr
		}
		for i := 0; i+3 < len(chars); i++ {
			if chars[i] == 'H' && chars[i+1] == 'o' && chars[i+2] == 's' && chars[i+3] == 't' {
				headerAttr, headerFound = attrs[i], true
				break
			}
		}
		for _, char := range chars {
			var encoded [2]byte
			binary.LittleEndian.PutUint16(encoded[:], char)
			_, _ = hasher.Write(encoded[:])
			text = append(text, rune(char))
		}
		text = append(text, '\n')
	}
	return hasher.Sum64(), width, height, string(text), headerAttr, headerFound, nil
}

func setNativeWindowsConsoleWindow(consoleOut windows.Handle, window windows.SmallRect) error {
	result, _, callErr := nativeSetConsoleWindow.Call(uintptr(consoleOut), 1, uintptr(unsafe.Pointer(&window)))
	if result != 0 {
		return nil
	}
	if callErr == syscall.Errno(0) {
		return errors.New("SetConsoleWindowInfo failed")
	}
	return callErr
}
