// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package output

import (
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestLinuxKeyWorkerIgnoresAltAndEscapeSequences(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	var quits atomic.Int32
	s := &Screen{
		done:     make(chan struct{}),
		keys:     KeyHandlers{Quit: func() { quits.Add(1) }},
		platform: &terminalPlatform{inFD: int(reader.Fd())},
	}
	finished := make(chan struct{})
	go func() { s.readPlatformKeys(); close(finished) }()
	if _, err := writer.Write([]byte("\x1bq\x1bv\x1b[A")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	close(s.done)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("key worker did not stop")
	}
	if quits.Load() != 0 || s.table.Load() {
		t.Fatalf("escape sequences invoked commands: quits=%d table=%v", quits.Load(), s.table.Load())
	}
}

func TestLinuxBackgroundCursorCleanupOnlyShowsCursor(t *testing.T) {
	if !linuxCursorWritePermitted(false, true, "\x1b[?25h") {
		t.Fatal("background cleanup did not allow the six-byte show-cursor sequence")
	}
	if linuxCursorWritePermitted(false, true, "\x1b[?25l") {
		t.Fatal("background cleanup allowed a hide-cursor sequence")
	}
	if !linuxCursorWritePermitted(true, false, "\x1b[?25l") {
		t.Fatal("foreground cursor update was rejected")
	}
}

func TestLinuxBackgroundScreenStartsKeysAfterForegroundReturn(t *testing.T) {
	if os.Getenv("CN_MONITOR_LINUX_BG_KEYS") == "" {
		t.Skip("controlling-PTY child only")
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		t.Fatal("child stdin is not a terminal")
	}
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGTTOU)
	child := exec.Command("sleep", "5")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	quit := make(chan struct{}, 1)
	s := OpenScreen(os.Stdin, os.Stderr, "pastel", true, KeyHandlers{Quit: func() { quit <- struct{}{} }})
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, unix.Getpgrp()); err != nil {
		s.Close()
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString("READY\n")
	select {
	case <-quit:
	case <-time.After(3 * time.Second):
		s.Close()
		t.Fatal("foreground key worker did not dispatch q after background startup")
	}
	s.Close()
	current, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *current != *original {
		t.Fatalf("terminal mode after close differs from original: got=%#v want=%#v", *current, *original)
	}
}
