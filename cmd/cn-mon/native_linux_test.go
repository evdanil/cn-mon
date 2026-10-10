//go:build linux

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/target"
	"golang.org/x/sys/unix"
)

func TestNativeCommandLoopbackHistoryJSON(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database := filepath.Join(root, "history.db")
	logPath := filepath.Join(root, "cn.log")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary,
		"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", fmt.Sprint(port), "127.0.0.1",
	)
	cmd.Env = nativeCommandEnv(t, root, database, logPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			t.Fatalf("start native command: %v", runErr)
		}
		code = exit.ExitCode()
		if code != 3 {
			t.Fatalf("command failed: exit=%d stderr=%q", exit.ExitCode(), stderr.String())
		}
	}

	var report struct {
		Seen    []map[string]any `json:"seen"`
		Latest  []map[string]any `json:"latest"`
		Summary []map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode native command JSON: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if len(report.Seen) != 1 || len(report.Latest) != 1 || len(report.Summary) != 1 {
		t.Fatalf("one completed loopback round expected in JSON, got seen=%d latest=%d summary=%d", len(report.Seen), len(report.Latest), len(report.Summary))
	}
	if got := report.Latest[0]["tcp_"+fmt.Sprint(port)]; got != "open" {
		t.Fatalf("loopback TCP result = %v, want open; stderr=%q", got, stderr.String())
	}
	result, _ := report.Latest[0]["result"].(string)
	if code == 3 && result != "ERROR" {
		t.Fatalf("exit 3 without a probe execution error: result=%q stderr=%q", result, stderr.String())
	}
	if code == 0 && result == "ERROR" {
		t.Fatalf("probe execution error did not affect command exit: result=%q", result)
	}
	if code != 0 && code != 3 {
		t.Fatalf("loopback command exit=%d, want 0 (probe ran) or 3 (native permission/execution error)", code)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("configured log file missing: %v", err)
	}

	request, _, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := history.OpenReader(context.Background(), database)
	if err != nil || reader == nil {
		t.Fatalf("open command-created history: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	err = reader.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Rounds != 1 || info.NewestRun == nil || info.NewestRun.Status != "finished" {
			return fmt.Errorf("stored request summary = %+v", info)
		}
		page, err := snapshot.Rounds(info.ID, history.RoundFilter{Limit: 10})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || len(page.Items[0].Rows) != 1 || page.Items[0].Rows[0].TCP[port] != "open" {
			return fmt.Errorf("stored rounds do not contain the loopback result: %+v", page.Items)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read back native command history: %v", err)
	}
	t.Logf("CNMON_NATIVE_COMMAND_RESULT loopback=127.0.0.1 tcp=open icmp=%q exit=%d json=pass history=pass", result, code)
}

func TestNativeCommandPreInputInterrupt(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database := filepath.Join(root, "must-not-exist.db")
	logPath := filepath.Join(root, "must-not-exist.log")
	child := startNativeChild(t, binary, root, 8*time.Second,
		"--format", "json", "--file", "-", "--history-file", database, "--log-file", logPath)
	waitNativeInputRead(t, child)
	child.signal(t, syscall.SIGINT)
	if code := nativeExitCode(child.wait(t, 3*time.Second)); code != 130 {
		t.Fatalf("startup Ctrl+C exit=%d want 130; stdout=%q stderr=%q", code, child.stdout.String(), child.stderr.String())
	}
	if child.stdout.Len() != 0 || child.stderr.Len() != 0 {
		t.Fatalf("startup interrupt emitted output: stdout=%q stderr=%q", child.stdout.String(), child.stderr.String())
	}
	for _, path := range []string{database, logPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("startup interrupt created %s: %v", path, err)
		}
	}
	t.Log("CNMON_NATIVE_COMMAND_PREINPUT_INTERRUPT exit=130 side-effects=none")
}

func TestNativeCommandRunSignalsAndSecondInterrupt(t *testing.T) {
	requireNativeCommand(t)
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{name: "first Ctrl+C", sig: syscall.SIGINT},
		{name: "SIGTERM", sig: syscall.SIGTERM},
		{name: "SIGHUP", sig: syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := buildNativeCommand(t)
			root := t.TempDir()
			database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
			child := startNativeChild(t, binary, root, 25*time.Second,
				"--format", "json", "--every", "10s", "--for", "30s",
				"--history-file", database, "--log-file", logPath, "127.0.0.1")
			key := nativeRequestKey(t, "127.0.0.1")
			info := waitNativeRounds(t, child.ctx, database, key, 1)
			if info == nil || info.NewestRun == nil {
				t.Fatal("native command did not publish its first history round")
			}
			child.signal(t, tc.sig)
			err := child.wait(t, 8*time.Second)
			code := nativeExitCode(err)
			if code != 0 && code != 1 && code != 3 {
				t.Fatalf("graceful signal exit=%d err=%v stderr=%q", code, err, child.stderr.String())
			}
			finished := nativeHistoryRequest(t, database, key)
			if finished == nil || finished.NewestRun == nil || finished.NewestRun.Status != "stopped" || finished.NewestRun.Rounds < 1 {
				t.Fatalf("graceful signal did not finalize the completed run as stopped: %+v", finished)
			}
			t.Logf("CNMON_NATIVE_COMMAND_RUN_SIGNAL signal=%s completed-rounds=%d exit=%d", tc.sig, finished.NewestRun.Rounds, code)
		})
	}

	t.Run("second Ctrl+C exits while finalization is write-locked", func(t *testing.T) {
		binary := buildNativeCommand(t)
		root := t.TempDir()
		database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
		child := startNativeChild(t, binary, root, 25*time.Second,
			"--format", "json", "--every", "10s", "--for", "30s",
			"--history-file", database, "--log-file", logPath, "127.0.0.1")
		key := nativeRequestKey(t, "127.0.0.1")
		if info := waitNativeRounds(t, child.ctx, database, key, 1); info == nil {
			t.Fatal("native command did not publish its first history round")
		}
		unlock := acquireNativeWriteLock(t, database)
		defer unlock()
		child.signal(t, syscall.SIGINT)
		// The first signal wakes the engine from its interval wait. The held
		// SQLite writer lock makes EndRun remain blocked until the hard signal.
		waitNativeOutput(t, child, "stopping; preparing the final summary", 3*time.Second)
		if child.exited() {
			t.Fatalf("first Ctrl+C exited before the second signal: %v", child.err)
		}
		started := time.Now()
		child.signal(t, syscall.SIGINT)
		err := child.wait(t, 3*time.Second)
		if code := nativeExitCode(err); code != 130 {
			t.Fatalf("second Ctrl+C exit=%d err=%v stderr=%q", code, err, child.stderr.String())
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("second Ctrl+C took %s with a blocked finalizer", elapsed)
		}
		t.Log("CNMON_NATIVE_COMMAND_SECOND_INTERRUPT exit=130 finalizer=write-locked")
	})
}

func TestNativeCommandPreRunTerminationAndInheritedIgnores(t *testing.T) {
	requireNativeCommand(t)
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{name: "default SIGTERM terminates blocked input", sig: syscall.SIGTERM},
		{name: "default SIGHUP terminates blocked input", sig: syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := buildNativeCommand(t)
			root := t.TempDir()
			database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
			child := startNativeChild(t, binary, root, 8*time.Second,
				"--format", "json", "--file", "-", "--history-file", database, "--log-file", logPath)
			waitNativeInputRead(t, child)
			child.signal(t, tc.sig)
			err := child.wait(t, 3*time.Second)
			if !nativeWasSignaled(err, tc.sig) {
				t.Fatalf("pre-run %s was swallowed: err=%v stdout=%q stderr=%q", tc.sig, err, child.stdout.String(), child.stderr.String())
			}
			for _, path := range []string{database, logPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("pre-run %s created %s: %v", tc.sig, path, err)
				}
			}
			t.Logf("CNMON_NATIVE_COMMAND_PRE_RUN_SIGNAL signal=%s disposition=default-propagated", tc.sig)
		})
	}

	for _, tc := range []struct {
		name, ignored string
		sig, finish   syscall.Signal
	}{
		{name: "inherited ignored SIGHUP", ignored: "HUP", sig: syscall.SIGHUP, finish: syscall.SIGINT},
		{name: "inherited ignored SIGINT", ignored: "INT", sig: syscall.SIGINT, finish: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := buildNativeCommand(t)
			root := t.TempDir()
			database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
			child := startNativeIgnoringSignal(t, binary, root, 8*time.Second, tc.ignored,
				"--format", "json", "--file", "-", "--history-file", database, "--log-file", logPath)
			waitNativeInputRead(t, child)
			child.signal(t, tc.sig)
			select {
			case <-child.done:
				t.Fatalf("inherited ignored %s terminated command: %v", tc.sig, child.err)
			case <-time.After(200 * time.Millisecond):
			}
			if child.exited() {
				t.Fatalf("inherited ignored %s terminated command: %v", tc.sig, child.err)
			}
			child.signal(t, tc.finish)
			if err := child.wait(t, 3*time.Second); tc.finish == syscall.SIGINT && nativeExitCode(err) != 130 || tc.finish == syscall.SIGTERM && !nativeWasSignaled(err, syscall.SIGTERM) {
				t.Fatalf("termination after ignored %s was not preserved: finish=%s err=%v stderr=%q", tc.sig, tc.finish, err, child.stderr.String())
			}
			t.Logf("CNMON_NATIVE_COMMAND_INHERITED_IGNORE signal=%s result=preserved", tc.sig)
		})
	}

	t.Run("inherited ignored SIGTERM is reset by the standard Go runtime", func(t *testing.T) {
		binary := buildNativeCommand(t)
		root := t.TempDir()
		database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
		child := startNativeIgnoringSignal(t, binary, root, 8*time.Second, "TERM",
			"--format", "json", "--file", "-", "--history-file", database, "--log-file", logPath)
		waitNativeInputRead(t, child)
		child.signal(t, syscall.SIGTERM)
		if err := child.wait(t, 3*time.Second); !nativeWasSignaled(err, syscall.SIGTERM) {
			t.Fatalf("standard Go runtime inherited-TERM behavior changed: err=%v", err)
		}
		for _, path := range []string{database, logPath} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("pre-input inherited TERM created %s: %v", path, err)
			}
		}
		t.Log("CNMON_NATIVE_COMMAND_INHERITED_TERM pre-input=runtime-default")
	})
	t.Run("inherited ignored SIGTERM during active run is graceful", func(t *testing.T) {
		binary := buildNativeCommand(t)
		root := t.TempDir()
		database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
		child := startNativeIgnoringSignal(t, binary, root, 20*time.Second, "TERM",
			"--format", "json", "--every", "10s", "--for", "30s",
			"--history-file", database, "--log-file", logPath, "127.0.0.1")
		key := nativeRequestKey(t, "127.0.0.1")
		if waitNativeRounds(t, child.ctx, database, key, 1) == nil {
			t.Fatal("inherited-TERM command did not complete a round")
		}
		child.signal(t, syscall.SIGTERM)
		if code := nativeExitCode(child.wait(t, 8*time.Second)); code != 0 && code != 1 && code != 3 {
			t.Fatalf("active inherited-TERM exit=%d stderr=%q", code, child.stderr.String())
		}
		info := nativeHistoryRequest(t, database, key)
		if info == nil || info.NewestRun == nil || info.NewestRun.Status != "stopped" || info.NewestRun.Rounds < 1 {
			t.Fatalf("active inherited-TERM did not gracefully finalize history: %+v", info)
		}
		t.Log("CNMON_NATIVE_COMMAND_INHERITED_TERM active-run=graceful history=stopped")
	})
	t.Run("inherited ignored SIGHUP during active run is preserved", func(t *testing.T) {
		binary := buildNativeCommand(t)
		root := t.TempDir()
		database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
		child := startNativeIgnoringSignal(t, binary, root, 20*time.Second, "HUP",
			"--format", "json", "--every", "10s", "--for", "30s",
			"--history-file", database, "--log-file", logPath, "127.0.0.1")
		key := nativeRequestKey(t, "127.0.0.1")
		if waitNativeRounds(t, child.ctx, database, key, 1) == nil {
			t.Fatal("inherited-HUP command did not complete a round")
		}
		child.signal(t, syscall.SIGHUP)
		select {
		case <-child.done:
			t.Fatalf("inherited ignored SIGHUP terminated active run: %v", child.err)
		case <-time.After(200 * time.Millisecond):
		}
		child.signal(t, syscall.SIGINT)
		if code := nativeExitCode(child.wait(t, 8*time.Second)); code != 0 && code != 1 && code != 3 {
			t.Fatalf("Ctrl+C after ignored active HUP exit=%d stderr=%q", code, child.stderr.String())
		}
		info := nativeHistoryRequest(t, database, key)
		if info == nil || info.NewestRun == nil || info.NewestRun.Status != "stopped" || info.NewestRun.Rounds < 1 {
			t.Fatalf("active ignored HUP did not preserve then finalize the run: %+v", info)
		}
		t.Log("CNMON_NATIVE_COMMAND_INHERITED_IGNORE signal=hangup active-run=preserved")
	})
}

func TestNativeCommandPauseResume(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	child := startNativeChild(t, binary, root, 45*time.Second,
		"--format", "json", "--every", "10s", "--for", "90s",
		"--history-file", database, "--log-file", logPath, "127.0.0.1")
	key := nativeRequestKey(t, "127.0.0.1")
	info := waitNativeRounds(t, child.ctx, database, key, 1)
	if info == nil {
		t.Fatal("native command did not complete a round before pause")
	}
	child.signal(t, syscall.SIGSTOP)
	waitNativeProcessState(t, child, 'T', 3*time.Second)
	pausedRounds := info.NewestRun.Rounds
	time.Sleep(11 * time.Second)
	paused := nativeHistoryRequest(t, database, key)
	if paused == nil || paused.NewestRun == nil || paused.NewestRun.Rounds != pausedRounds {
		t.Fatalf("stopped command changed history rounds: before=%d after=%+v", pausedRounds, paused)
	}
	child.signal(t, syscall.SIGCONT)
	waitNativeProcessState(t, child, 0, 3*time.Second)
	if waitNativeRounds(t, child.ctx, database, key, pausedRounds+1) == nil {
		t.Fatal("native command did not resume after SIGCONT")
	}
	child.signal(t, syscall.SIGTERM)
	if err := child.wait(t, 5*time.Second); nativeExitCode(err) != 0 && nativeExitCode(err) != 1 && nativeExitCode(err) != 3 {
		t.Fatalf("resumed command did not gracefully stop: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_PAUSE_RESUME paused=no-round-growth resumed=next-round")
}

func TestNativeCommandClosedStdoutPipeIsQuiet(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--format", "json", "--history-file", database,
		"--log-file", logPath, "--tcp", fmt.Sprint(port), "127.0.0.1")
	cmd.Env = nativeCommandEnv(t, root, database, logPath)
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	if err := stdoutReader.Close(); err != nil {
		_ = stdoutWriter.Close()
		t.Fatalf("close stdout reader before start: %v", err)
	}
	cmd.Stdout = stdoutWriter
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdoutWriter.Close()
		t.Fatalf("start command: %v", err)
	}
	if err := stdoutWriter.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("close parent stdout writer: %v", err)
	}
	runErr := cmd.Wait()
	code := nativeExitCode(runErr)
	if code != 0 && code != 3 {
		t.Fatalf("closed stdout changed normal exit code to %d: %v stderr=%q", code, runErr, stderr.String())
	}
	stderrText := strings.ToLower(stderr.String())
	for _, forbidden := range []string{"write report:", "broken pipe", "stdout"} {
		if strings.Contains(stderrText, forbidden) {
			t.Fatalf("closed stdout emitted a pipe/report diagnostic %q: %q", forbidden, stderr.String())
		}
	}
	request, _, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil {
		t.Fatal(err)
	}
	key := request.Key
	info := nativeHistoryRequest(t, database, key)
	if info == nil || info.NewestRun == nil || info.NewestRun.Status != "finished" || info.NewestRun.Rounds != 1 {
		t.Fatalf("closed stdout prevented completed history: info=%+v exit=%d runErr=%v stderr=%q", info, code, runErr, stderr.String())
	}
	page := nativeRequestRounds(t, database, key)
	if len(page) != 1 || len(page[0].Rows) != 1 || page[0].Rows[0].TCP[port] != "open" {
		t.Fatalf("closed stdout lost completed TCP result: %+v", page)
	}
	if code == 0 && page[0].Rows[0].Result == "ERROR" || code == 3 && page[0].Rows[0].Result != "ERROR" {
		t.Fatalf("process exit=%d disagrees with stored probe result %+v", code, page[0].Rows[0])
	}
	t.Logf("CNMON_NATIVE_COMMAND_CLOSED_STDOUT exit=%d pipe-diagnostic=none tcp=open history=finished", code)
}

type nativeChild struct {
	cmd       *exec.Cmd
	ctx       context.Context
	cancel    context.CancelFunc
	stdin     *os.File
	inputRead *os.File
	stdout    *lockedNativeBuffer
	stderr    *lockedNativeBuffer
	done      chan struct{}
	err       error
}

func startNativeChild(t *testing.T, binary, root string, timeout time.Duration, args ...string) *nativeChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = nativeCommandEnv(t, root, filepath.Join(root, "history.db"), filepath.Join(root, "cn.log"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	inputRead, stdin, err := os.Pipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stdin = inputRead
	stdout, stderr := &lockedNativeBuffer{}, &lockedNativeBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		_ = inputRead.Close()
		_ = stdin.Close()
		cancel()
		t.Fatalf("start private native child: %v", err)
	}
	child := &nativeChild{cmd: cmd, ctx: ctx, cancel: cancel, stdin: stdin, inputRead: inputRead, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go func() {
		child.err = cmd.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = inputRead.Close()
		if !child.exited() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-child.done:
			case <-time.After(2 * time.Second):
				t.Errorf("private native process group did not stop after SIGKILL")
			}
		}
		cancel()
	})
	return child
}

func startNativeIgnoringSignal(t *testing.T, binary, root string, timeout time.Duration, ignored string, args ...string) *nativeChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ready := filepath.Join(root, "shell-ready")
	commandArgs := append([]string{"-c", "trap '' " + ignored + "; : > \"$1\"; shift; exec \"$@\"", "sh", ready, binary}, args...)
	cmd := exec.CommandContext(ctx, "sh", commandArgs...)
	cmd.Env = nativeCommandEnv(t, root, filepath.Join(root, "history.db"), filepath.Join(root, "cn.log"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	inputRead, stdin, err := os.Pipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stdin = inputRead
	stdout, stderr := &lockedNativeBuffer{}, &lockedNativeBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		_ = inputRead.Close()
		_ = stdin.Close()
		cancel()
		t.Fatalf("start ignored-signal native child: %v", err)
	}
	child := &nativeChild{cmd: cmd, ctx: ctx, cancel: cancel, stdin: stdin, inputRead: inputRead, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go func() {
		child.err = cmd.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = inputRead.Close()
		if !child.exited() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-child.done:
			case <-time.After(2 * time.Second):
				t.Errorf("private ignored-signal process group did not stop after SIGKILL")
			}
		}
		cancel()
	})
	return child
}

func (child *nativeChild) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(-child.cmd.Process.Pid, sig); err != nil {
		t.Fatalf("send %s to private process group: %v", sig, err)
	}
}

func (child *nativeChild) exited() bool {
	select {
	case <-child.done:
		return true
	default:
		return false
	}
}

func (child *nativeChild) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-child.done:
		return child.err
	case <-time.After(timeout):
		t.Fatalf("native command did not exit within %s", timeout)
		return nil
	}
}

type lockedNativeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedNativeBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(value)
}

func (b *lockedNativeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedNativeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func waitNativeOutput(t *testing.T, child *nativeChild, text string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(child.stderr.String(), text) {
			return
		}
		if child.exited() {
			t.Fatalf("native command exited before writing %q: %v", text, child.err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("native command did not write %q within %s", text, timeout)
}

func waitNativeInputRead(t *testing.T, child *nativeChild) {
	t.Helper()
	if _, err := child.stdin.Write([]byte("127.0.0.1\n")); err != nil {
		t.Fatalf("write held-input readiness token: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		available, err := unix.IoctlGetInt(int(child.inputRead.Fd()), unix.TIOCINQ)
		if err == nil && available == 0 {
			return
		}
		if child.exited() {
			t.Fatalf("command exited before consuming held input: %v", child.err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("command did not consume readiness token while stdin remained open")
}

func nativeExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func nativeWasSignaled(err error, signal syscall.Signal) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == signal
}

func nativeRequestKey(t *testing.T, host string) string {
	t.Helper()
	request, _, err := target.Build([]string{host}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request.Key
}

func nativeHistoryRequest(t *testing.T, database, key string) *history.RequestInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil || reader == nil {
		return nil
	}
	defer reader.Close()
	var info *history.RequestInfo
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		var err error
		info, err = snapshot.Request(key)
		return err
	})
	if err != nil {
		return nil
	}
	return info
}

func waitNativeRounds(t *testing.T, ctx context.Context, database, key string, rounds int64) *history.RequestInfo {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info := nativeHistoryRequest(t, database, key); info != nil && info.NewestRun != nil && info.NewestRun.Status == "running" && info.NewestRun.Rounds >= rounds && info.NewestRun.RoundSeconds != nil {
			return info
		}
		select {
		case <-ctx.Done():
			t.Fatalf("history did not reach %d completed round(s): %v", rounds, ctx.Err())
			return nil
		case <-ticker.C:
		}
	}
}

func waitNativeProcessState(t *testing.T, child *nativeChild, want byte, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	path := filepath.Join("/proc", fmt.Sprint(child.cmd.Process.Pid), "status")
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read process state: %v", err)
		}
		var state byte
		for _, line := range strings.Split(string(contents), "\n") {
			if strings.HasPrefix(line, "State:") && len(line) > 7 {
				state = line[7]
				break
			}
		}
		if want == 0 && state != 'T' && state != 't' || want != 0 && state == want {
			return
		}
		if child.exited() {
			t.Fatalf("process exited before reaching state %q: %v", want, child.err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process did not reach state %q within %s", want, timeout)
}

func nativeRequestRounds(t *testing.T, database, key string) []history.StoredRound {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil || reader == nil {
		t.Fatalf("open history rounds: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	var result []history.StoredRound
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		request, err := snapshot.Request(key)
		if err != nil || request == nil {
			return err
		}
		page, err := snapshot.Rounds(request.ID, history.RoundFilter{Limit: 10})
		if err == nil {
			result = page.Items
		}
		return err
	})
	if err != nil {
		t.Fatalf("read history rounds: %v", err)
	}
	return result
}

func acquireNativeWriteLock(t *testing.T, database string) func() {
	t.Helper()
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(1000)")
	dsn := (&url.URL{Scheme: "file", Path: database, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open native write-lock connection: %v", err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, err := db.Conn(ctx)
	if err != nil {
		cancel()
		_ = db.Close()
		t.Fatalf("acquire native write-lock connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		cancel()
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("hold SQLite write lock: %v", err)
	}
	cancel()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
		closeErr := conn.Close()
		dbErr := db.Close()
		if rollbackErr != nil && !strings.Contains(strings.ToLower(rollbackErr.Error()), "no transaction") {
			t.Errorf("release native SQLite write lock: %v", rollbackErr)
		}
		if closeErr != nil {
			t.Errorf("close native SQLite lock connection: %v", closeErr)
		}
		if dbErr != nil {
			t.Errorf("close native SQLite lock database: %v", dbErr)
		}
	}
}
