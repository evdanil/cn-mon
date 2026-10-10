//go:build linux && native

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeCommandLinuxPTYHardInterruptRestoresWhileUIWritesBlocked(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "blocked-ui-history.db")
	logPath := filepath.Join(directory, "blocked-ui-cn.log")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	master, slave := openNativeLinuxPTY(t)
	defer master.Close()
	defer slave.Close()
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1")
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdin, command.Stderr, command.Stdout = slave, slave, io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatalf("start blocked-UI command: %v", err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- command.Wait() }()
	waitCollected := false
	defer func() {
		if !waitCollected {
			_ = command.Process.Kill()
			select {
			case <-waitResult:
				waitCollected = true
			case <-time.After(2 * time.Second):
				t.Error("blocked-UI command wait did not finish after kill")
			}
		}
	}()
	terminalOutput := &nativePTYOutput{}
	readerStop := make(chan struct{})
	readerDone := make(chan struct{})
	readerStopped := false
	stopReader := func() {
		if !readerStopped {
			close(readerStop)
			readerStopped = true
			select {
			case <-readerDone:
			case <-time.After(time.Second):
				t.Error("blocked-UI PTY reader did not stop")
			}
		}
	}
	go func() {
		defer close(readerDone)
		buffer := make([]byte, 2048)
		for {
			select {
			case <-readerStop:
				return
			default:
			}
			fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
			if _, pollErr := unix.Poll(fds, 100); pollErr != nil {
				return
			}
			if fds[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, readErr := unix.Read(int(master.Fd()), buffer)
			if n > 0 {
				terminalOutput.append(buffer[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()
	defer func() { stopReader(); _ = master.Close() }()
	if !terminalOutput.waitContains([]byte("\x1b[?25l"), 20*time.Second) {
		t.Fatalf("blocked-UI command never activated its live terminal: %q", terminalOutput.bytes())
	}
	if err := waitLinuxPTYMode(slave, original, 2*time.Second); err != nil {
		t.Fatalf("live command did not enable cbreak before blocking: %v", err)
	}
	stopReader()
	if err := unix.IoctlSetInt(int(slave.Fd()), unix.TCXONC, unix.TCOOFF); err != nil {
		t.Fatalf("stop PTY output before filling its queue: %v", err)
	}
	ptyNumber, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("get blocked-UI PTY number: %v", err)
	}
	fillFD, err := unix.Open(fmt.Sprintf("/dev/pts/%d", ptyNumber), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open nonblocking PTY filler: %v", err)
	}
	filler := bytes.Repeat([]byte("x"), 4096)
	filled := 0
	for {
		if filled >= 1<<20 {
			_ = unix.Close(fillFD)
			t.Fatal("PTY output queue did not reach EAGAIN before 1 MiB")
		}
		n, writeErr := unix.Write(fillFD, filler)
		if n > 0 {
			filled += n
		}
		if errors.Is(writeErr, unix.EAGAIN) || errors.Is(writeErr, unix.EWOULDBLOCK) {
			break
		}
		if writeErr != nil {
			_ = unix.Close(fillFD)
			t.Fatalf("fill stopped PTY output queue: %v", writeErr)
		}
		if n == 0 {
			_ = unix.Close(fillFD)
			t.Fatal("nonblocking PTY filler wrote zero bytes without EAGAIN")
		}
	}
	_ = unix.Close(fillFD)
	time.Sleep(time.Second)
	if err := waitLinuxBlockedWrite(command.Process.Pid, 2, 3*time.Second); err != nil {
		t.Fatalf("UI did not block writing to the full stopped PTY: filled=%d err=%v", filled, err)
	}
	if err := command.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := command.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitResult:
		waitCollected = true
		if nativeExitCode(err) != 130 {
			t.Fatalf("blocked-UI second interrupt exit=%d err=%v", nativeExitCode(err), err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("hard interrupt did not exit while PTY output was stopped; termios=%+v output=%q", mustPTYMode(t, slave), terminalOutput.bytes())
	}
	if err := waitLinuxPTYRestored(slave, original, time.Second); err != nil {
		t.Fatalf("hard interrupt did not restore termios with UI output blocked: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_LINUX_PTY blocked-ui=second-interrupt exit=130 termios=restored")
}

func mustPTYMode(t *testing.T, slave *os.File) *unix.Termios {
	t.Helper()
	mode, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read PTY mode for failure context: %v", err)
	}
	return mode
}

func waitLinuxBlockedWrite(pid int, fd uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
		if err != nil {
			return err
		}
		for _, task := range tasks {
			data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", task.Name(), "syscall"))
			if err != nil {
				lastErr = err
				continue
			}
			fields := strings.Fields(string(data))
			if len(fields) < 2 {
				continue
			}
			number, numberErr := strconv.ParseUint(fields[0], 0, 64)
			argument, argumentErr := strconv.ParseUint(fields[1], 0, 64)
			if numberErr == nil && argumentErr == nil && number == uint64(unix.SYS_WRITE) && argument == fd {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil && runtime.GOARCH != "amd64" {
		return fmt.Errorf("cannot inspect blocked write on %s: %w", runtime.GOARCH, lastErr)
	}
	return fmt.Errorf("process %d did not expose a blocked write to fd %d", pid, fd)
}
