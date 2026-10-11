//go:build linux && native

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	collectorEINTRHelperEnv = "CNMON_LINUX_COLLECTOR_EINTR_HELPER"
	collectorEINTRToken     = "KEY_AFTER_POLL_EINTR"
)

func TestNativePTYCollectorRetriesInterruptedPoll(t *testing.T) {
	requireNativeCommand(t)
	master, slave := openNativeLinuxPTY(t)
	defer master.Close()
	defer slave.Close()

	command := exec.Command(os.Args[0], "-test.run=^TestNativeLinuxCollectorEINTRHelper$")
	command.Env = append(os.Environ(), collectorEINTRHelperEnv+"=1")
	command.ExtraFiles = []*os.File{master}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = stdoutWriter
	command.Stderr = stdoutWriter
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		t.Fatalf("start private collector helper: %v", err)
	}
	_ = stdoutWriter.Close()
	defer stdout.Close()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	waited := false

	lines := make(chan string, 8)
	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			select {
			case <-wait:
				waited = true
			case <-time.After(2 * time.Second):
				t.Error("private collector helper did not reap within 2s after kill")
			}
		}
		select {
		case <-scannerDone:
		case <-time.After(2 * time.Second):
			t.Error("private collector output scanner did not finish")
		}
	}()
	ready, err := collectorEINTRReadLine(lines, 5*time.Second)
	if err != nil {
		t.Fatalf("read private collector readiness: %v", err)
	}
	var pid, tid int
	if _, err := fmt.Sscanf(ready, "READY pid=%d tid=%d", &pid, &tid); err != nil || pid <= 0 || tid <= 0 {
		t.Fatalf("parse collector readiness %q: pid=%d tid=%d err=%v", ready, pid, tid, err)
	}
	if pid != command.Process.Pid {
		t.Fatalf("collector helper reported pid %d, child pid is %d", pid, command.Process.Pid)
	}
	signalMarker, err := collectorEINTRReadLine(lines, 3*time.Second)
	if err != nil || signalMarker != "SIGNAL_HANDLED" {
		t.Fatalf("targeted handled signal was not observed: marker=%q err=%v", signalMarker, err)
	}
	if _, err := master.Stat(); err != nil {
		t.Fatalf("PTY master closed after interrupted Poll: %v", err)
	}
	if _, err := slave.Write([]byte(collectorEINTRToken + "\n")); err != nil {
		t.Fatalf("write post-interruption token through PTY: %v", err)
	}
	line, err := collectorEINTRReadLine(lines, 2*time.Second)
	if err != nil || line != "TOKEN_COLLECTED" {
		t.Fatalf("collector did not read token after handled Poll interruption: marker=%q err=%v", line, err)
	}
	line, err = collectorEINTRReadLine(lines, 2*time.Second)
	if err != nil || line != "COLLECTOR_STOPPED" {
		t.Fatalf("collector did not stop after explicit stop: marker=%q err=%v", line, err)
	}
	select {
	case err := <-wait:
		waited = true
		if err != nil {
			t.Fatalf("wait for private collector helper: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("private collector helper did not exit after token and stop")
	}
	select {
	case <-scannerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("private collector output scanner did not finish")
	}
	t.Log("CNMON_NATIVE_COMMAND_LINUX_PTY collector=poll-eintr token=collected fd=live")
}

func TestNativeLinuxCollectorEINTRHelper(t *testing.T) {
	if os.Getenv(collectorEINTRHelperEnv) != "1" {
		return
	}
	master := os.NewFile(3, "private-pty-master")
	if master == nil {
		t.Fatal("missing PTY master descriptor")
	}
	defer master.Close()
	handled := make(chan os.Signal, 1)
	signal.Notify(handled, syscall.SIGUSR1)
	defer signal.Stop(handled)

	output := &nativePTYOutput{}
	stop := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	stopCollector := func() { stopOnce.Do(func() { close(stop) }) }
	waitCollector := func(timeout time.Duration) bool {
		stopCollector()
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			return false
		}
	}
	signalSent := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		pid, tid := os.Getpid(), unix.Gettid()
		_, _ = fmt.Fprintf(os.Stdout, "READY pid=%d tid=%d\n", pid, tid)
		go func() {
			if err := waitForLinuxPollSyscall(pid, tid, 3*time.Second); err != nil {
				signalSent <- err
				return
			}
			signalSent <- unix.Tgkill(pid, tid, unix.SIGUSR1)
		}()
		collectNativeLinuxPTY(master, output, stop, done)
	}()
	defer func() {
		if !waitCollector(2 * time.Second) {
			t.Error("collector did not stop within 2s during helper cleanup")
		}
	}()
	select {
	case err := <-signalSent:
		if err != nil {
			_, _ = fmt.Fprintf(os.Stdout, "SIGNAL_ERROR %v\n", err)
			return
		}
	case <-time.After(4 * time.Second):
		if !waitCollector(time.Second) {
			t.Fatal("collector did not stop after signal deadline")
		}
		t.Fatal("collector did not reach and interrupt Poll")
	}
	select {
	case sig := <-handled:
		if sig != syscall.SIGUSR1 {
			_, _ = fmt.Fprintf(os.Stdout, "SIGNAL_ERROR unexpected signal %v\n", sig)
			return
		}
		_, _ = fmt.Fprintln(os.Stdout, "SIGNAL_HANDLED")
	case <-time.After(time.Second):
		t.Fatal("targeted SIGUSR1 was not handled")
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-done:
			_, _ = fmt.Fprintf(os.Stdout, "COLLECTOR_EXITED bytes=%q\n", output.bytes())
			return
		case <-deadline.C:
			if !waitCollector(time.Second) {
				t.Fatal("collector did not stop after helper deadline")
			}
			t.Fatal("collector helper did not receive a token")
		default:
		}
		if output.contains([]byte(collectorEINTRToken)) {
			if waitCollector(time.Second) {
				_, _ = fmt.Fprintln(os.Stdout, "TOKEN_COLLECTED")
				_, _ = fmt.Fprintln(os.Stdout, "COLLECTOR_STOPPED")
				return
			}
			t.Fatal("collector did not stop after token")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func collectorEINTRReadLine(lines <-chan string, timeout time.Duration) (string, error) {
	select {
	case line, ok := <-lines:
		if !ok {
			return "", fmt.Errorf("private helper stdout closed")
		}
		return line, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out waiting for private helper stdout")
	}
}

func waitForLinuxPollSyscall(pid, tid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	path := fmt.Sprintf("/proc/%d/task/%d/syscall", pid, tid)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) > 0 {
				value, parseErr := strconv.ParseInt(fields[0], 10, 64)
				if parseErr == nil && (value == int64(unix.SYS_POLL) || value == int64(unix.SYS_PPOLL)) {
					return nil
				}
			}
		}
		if _, err := os.Stat(fmt.Sprintf("/proc/%d/task/%d", pid, tid)); err != nil {
			return fmt.Errorf("collector polling thread %d exited before entering poll", tid)
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("collector thread %d did not enter poll/ppoll within %s", tid, timeout)
}
