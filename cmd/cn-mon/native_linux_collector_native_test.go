//go:build linux && native

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativePTYCollectorDrainsQueuedBytesWithHangup(t *testing.T) {
	master, slave := openNativeLinuxPTY(t)
	defer master.Close()
	defer slave.Close()
	const tail = "collector-tail-7f3a\x1b[?25h"
	want := append(bytes.Repeat([]byte{'0'}, 3000), []byte(tail)...)
	command := exec.Command("/bin/sh", "-c", "printf '%03000d' 0; printf 'collector-tail-7f3a\\033[?25h'")
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	commandWait := make(chan error, 1)
	go func() { commandWait <- command.Wait() }()
	commandWaited := false
	defer func() {
		if commandWaited {
			return
		}
		_ = command.Process.Kill()
		select {
		case <-commandWait:
			commandWaited = true
		case <-time.After(2 * time.Second):
			t.Error("PTY session leader did not reap within 2s after kill")
		}
	}()
	select {
	case err := <-commandWait:
		commandWaited = true
		if err != nil {
			t.Fatalf("PTY session leader: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = command.Process.Kill()
		select {
		case <-commandWait:
			commandWaited = true
		case <-time.After(2 * time.Second):
			t.Fatal("PTY session leader did not exit after kill")
		}
		t.Fatal("PTY session leader did not exit within 2s")
	}
	if err := slave.Close(); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	if _, err := unix.Poll(fds, 1000); err != nil {
		t.Fatalf("poll queued session-leader output: %v", err)
	}
	if fds[0].Revents&unix.POLLIN == 0 || fds[0].Revents&unix.POLLHUP == 0 {
		t.Fatalf("wanted queued PTY bytes and hangup together, got revents=%#x", fds[0].Revents)
	}
	t.Logf("CNMON_NATIVE_COMMAND_LINUX_PTY collector-witness=readable-hup revents=%#x", fds[0].Revents)
	output := &nativePTYOutput{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		collectNativeLinuxPTY(master, output, stop, done)
	}()
	stopped, joined := false, false
	stopAndJoin := func() {
		if !stopped {
			close(stop)
			stopped = true
		}
		if joined {
			return
		}
		select {
		case <-done:
			joined = true
		case <-time.After(time.Second):
			t.Error("PTY collector did not stop within 1s")
		}
	}
	defer stopAndJoin()
	select {
	case <-done:
		joined = true
	case <-time.After(time.Second):
		stopAndJoin()
		t.Fatal("PTY collector did not stop after terminal hangup")
	}
	if !bytes.Contains(output.bytes(), want) {
		t.Fatalf("collector dropped queued cursor-restoration tail on POLLIN|POLLHUP: %q", output.bytes())
	}
	t.Log("CNMON_NATIVE_COMMAND_LINUX_PTY collector=readable-hup multi-chunk=drained cursor-tail=present")
}
