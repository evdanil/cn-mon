//go:build linux && native

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/target"
	"golang.org/x/sys/unix"
)

func TestNativeCommandLinuxPTYForegroundSuspendLifecycle(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "foreground-history.db")
	logPath := filepath.Join(directory, "foreground-cn.log")
	reportPath := filepath.Join(directory, "foreground-report.json")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback for foreground lifecycle command: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	master, slave := openNativeLinuxPTY(t)
	defer slave.Close()
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read initial lifecycle PTY termios: %v", err)
	}
	markerRead, markerWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create lifecycle marker pipe: %v", err)
	}
	proceedRead, proceedWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create lifecycle controller pipe: %v", err)
	}
	defer markerRead.Close()
	defer markerWrite.Close()
	defer proceedRead.Close()
	defer proceedWrite.Close()
	var controllerOutput bytes.Buffer
	controller := exec.Command(os.Args[0], "-test.run=^TestNativeCommandLinuxPTYForegroundSuspendLifecycleController$")
	controller.Env = append(nativeCommandEnv(t, directory, database, logPath),
		"CNMON_LINUX_LIFECYCLE_HELPER=1",
		"CNMON_LINUX_LIFECYCLE_BINARY="+binary,
		"CNMON_LINUX_LIFECYCLE_PORT="+strconv.Itoa(port),
		"CNMON_LINUX_LIFECYCLE_REPORT="+reportPath,
	)
	controller.Stdin, controller.Stderr, controller.Stdout = slave, slave, &controllerOutput
	controller.ExtraFiles = []*os.File{markerWrite, proceedRead}
	controller.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := controller.Start(); err != nil {
		t.Fatalf("start controlling PTY helper: %v", err)
	}
	_ = markerWrite.Close()
	_ = proceedRead.Close()
	terminalOutput := &nativePTYOutput{}
	terminalStop := make(chan struct{})
	terminalDone := make(chan struct{})
	go func() {
		defer close(terminalDone)
		buffer := make([]byte, 2048)
		for {
			select {
			case <-terminalStop:
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
			if readErr != nil && !errors.Is(readErr, unix.EAGAIN) {
				return
			}
		}
	}()
	defer func() {
		close(terminalStop)
		_ = master.Close()
		<-terminalDone
		if controller.ProcessState == nil {
			_ = controller.Process.Kill()
			_ = controller.Wait()
		}
	}()
	markerScanner := bufio.NewScanner(markerRead)
	markers := make(chan string, 8)
	go func() {
		for markerScanner.Scan() {
			markers <- markerScanner.Text()
		}
		close(markers)
	}()
	deadline := time.NewTimer(35 * time.Second)
	defer deadline.Stop()
	var seen []string
	quitSent := false
	finished := false
	for !finished {
		select {
		case marker, ok := <-markers:
			if !ok {
				finished = true
				continue
			}
			seen = append(seen, marker)
			if marker == "FOREGROUND_ACTIVATED" {
				if !terminalOutput.waitContains([]byte("\x1b[?25l"), 3*time.Second) {
					t.Fatalf("foreground command did not draw a live frame before background transition: %q", terminalOutput.bytes())
				}
				if _, err := fmt.Fprintln(proceedWrite, "CONTINUE"); err != nil {
					t.Fatalf("allow foreground lifecycle to continue: %v", err)
				}
			}
			if marker == "READY_FOR_QUIT" {
				if _, err := master.Write([]byte("q")); err != nil {
					t.Fatalf("send q after foreground resume: %v", err)
				}
				quitSent = true
			}
			if marker == "DONE" {
				finished = true
			}
		case <-deadline.C:
			t.Fatalf("foreground lifecycle timed out; markers=%v terminal=%q controller=%q", seen, terminalOutput.bytes(), controllerOutput.String())
		}
	}
	if err := controller.Wait(); err != nil {
		t.Fatalf("foreground lifecycle controller: %v; controller=%q; markers=%v; terminal=%q", err, controllerOutput.String(), seen, terminalOutput.bytes())
	}
	if !quitSent {
		t.Fatalf("lifecycle helper never reached foreground-resumed q phase; markers=%v output=%q", seen, controllerOutput.String())
	}
	assertLinuxPTYTermios(t, int(slave.Fd()), original, "outer post-lifecycle verification")
	for _, marker := range []string{"BACKGROUND_START_UNCHANGED", "FOREGROUND_ACTIVATED", "BACKGROUND_OWNER_UNCHANGED", "FOREGROUND_RESTORED", "SUSPENDED_RESTORED", "RESUMED_REARMED", "DONE"} {
		if !containsString(seen, marker) {
			t.Fatalf("lifecycle evidence omitted %s: %v", marker, seen)
		}
	}
	output := terminalOutput.bytes()
	if bytes.Count(output, []byte("\x1b[?25l")) < 2 || bytes.Count(output, []byte("\x1b[?25h")) < 2 {
		t.Fatalf("foreground/suspend lifecycle did not hide/show cursor in both active phases: %q", output)
	}
	t.Log("CNMON_NATIVE_COMMAND_LINUX_PTY background-start=unchanged foreground-activation=pass background-owner-mode=unchanged suspend=restored resume=rearmed q=graceful cursor=restored")
}

func TestNativeCommandLinuxPTYForegroundSuspendLifecycleController(t *testing.T) {
	if os.Getenv("CNMON_LINUX_LIFECYCLE_HELPER") != "1" {
		return
	}
	markers := os.NewFile(3, "lifecycle-markers")
	if markers == nil {
		t.Fatal("missing lifecycle marker descriptor")
	}
	defer markers.Close()
	proceed := os.NewFile(4, "lifecycle-controller")
	if proceed == nil {
		t.Fatal("missing lifecycle controller descriptor")
	}
	defer proceed.Close()
	mark := func(value string) {
		t.Helper()
		if _, err := fmt.Fprintln(markers, value); err != nil {
			t.Fatalf("write lifecycle marker %s: %v", value, err)
		}
	}
	fd := int(os.Stdin.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	controllerGroup := unix.Getpgrp()
	if foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || foreground != controllerGroup {
		t.Fatalf("controller does not start foreground: foreground=%d controller=%d err=%v", foreground, controllerGroup, err)
	}
	signalResetJobControl()
	binary := os.Getenv("CNMON_LINUX_LIFECYCLE_BINARY")
	port := os.Getenv("CNMON_LINUX_LIFECYCLE_PORT")
	reportPath := os.Getenv("CNMON_LINUX_LIFECYCLE_REPORT")
	directory := filepath.Dir(os.Getenv("CNMON_NATIVE_HISTORY"))
	database := os.Getenv("CNMON_NATIVE_HISTORY")
	logPath := os.Getenv("CNMON_NATIVE_LOG")
	report, err := os.Create(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", port, "--every", "10s", "--for", "1m", "127.0.0.1")
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdin, command.Stderr, command.Stdout = os.Stdin, os.Stderr, report
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = report.Close()
		t.Fatalf("start product in a separate process group: %v", err)
	}
	productGroup, groupErr := unix.Getpgid(command.Process.Pid)
	productSession, sessionErr := unix.Getsid(command.Process.Pid)
	controllerSession, controllerSessionErr := unix.Getsid(0)
	if groupErr != nil || sessionErr != nil || controllerSessionErr != nil || productGroup != command.Process.Pid || productSession != controllerSession {
		t.Fatalf("product must be a non-orphaned process group in the controller session: pid=%d pgrp=%d session=%d controller-session=%d errors=%v/%v/%v", command.Process.Pid, productGroup, productSession, controllerSession, groupErr, sessionErr, controllerSessionErr)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	assertLinuxPTYTermios(t, fd, original, "background startup")
	mark("BACKGROUND_START_UNCHANGED")
	if foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || foreground != controllerGroup {
		t.Fatalf("product stole foreground during background startup: foreground=%d controller=%d err=%v", foreground, controllerGroup, err)
	}
	signalIgnoreJobControl()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, command.Process.Pid); err != nil {
		t.Fatalf("foreground product group: %v", err)
	}
	if foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err != nil || foreground != productGroup {
		t.Fatalf("terminal foreground after product activation: got=%d want=%d err=%v", foreground, productGroup, err)
	}
	if err := waitLinuxPTYMode(os.Stdin, original, 5*time.Second); err != nil {
		t.Fatalf("background-started command did not activate cbreak on foreground: %v", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := target.Build([]string{"127.0.0.1"}, []int{portNumber})
	if err != nil {
		t.Fatal(err)
	}
	key := request.Key
	if err := waitLinuxPTYCompletedRounds(database, key, 1, 15*time.Second); err != nil {
		t.Fatalf("wait for completed foreground command round: %v", err)
	}
	mark("FOREGROUND_ACTIVATED")
	proceedScanner := bufio.NewScanner(proceed)
	if !proceedScanner.Scan() || proceedScanner.Text() != "CONTINUE" {
		t.Fatalf("parent did not authorize foreground-frame observation: %q err=%v", proceedScanner.Text(), proceedScanner.Err())
	}

	jobMode, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, controllerGroup); err != nil {
		t.Fatalf("background product group: %v", err)
	}
	ownerMode := *original
	ownerMode.Lflag |= unix.ICANON
	if ownerMode.Lflag&unix.ECHO == 0 {
		ownerMode.Lflag |= unix.ECHO
	} else {
		ownerMode.Lflag &^= unix.ECHO
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &ownerMode); err != nil {
		t.Fatalf("set foreground owner's distinct terminal mode: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	assertLinuxPTYTermios(t, fd, &ownerMode, "background owner")
	if err := command.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("product exited while in background: %v", err)
	}
	mark("BACKGROUND_OWNER_UNCHANGED")
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, jobMode); err != nil {
		t.Fatalf("restore job terminal mode before foregrounding: %v", err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, command.Process.Pid); err != nil {
		t.Fatalf("restore product foreground group: %v", err)
	}
	if err := waitLinuxPTYMode(os.Stdin, original, 3*time.Second); err != nil {
		t.Fatalf("product mode after returning to foreground: %v", err)
	}
	mark("FOREGROUND_RESTORED")

	status, statusErr := os.ReadFile(fmt.Sprintf("/proc/%d/status", command.Process.Pid))
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	t.Logf("product signal status before TSTP: %s", signalStatusLines(string(status)))
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGTSTP); err != nil {
		t.Fatalf("suspend foreground product process group: %v", err)
	}
	if err := waitLinuxProcessState(command.Process.Pid, 'T', 3*time.Second); err != nil {
		t.Fatalf("product process group did not stop after SIGTSTP: %v", err)
	}
	if err := waitLinuxPTYRestored(os.Stdin, original, 3*time.Second); err != nil {
		t.Fatalf("terminal mode during suspend: %v", err)
	}
	mark("SUSPENDED_RESTORED")
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGCONT); err != nil {
		t.Fatalf("resume suspended product process group: %v", err)
	}
	if err := waitLinuxPTYMode(os.Stdin, original, 3*time.Second); err != nil {
		t.Fatalf("terminal mode after foreground resume: %v", err)
	}
	if err := waitLinuxProcessRunning(command.Process.Pid, 3*time.Second); err != nil {
		t.Fatalf("product did not resume: %v", err)
	}
	mark("RESUMED_REARMED")
	mark("READY_FOR_QUIT")
	if err := command.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 0 && exit.ExitCode() != 3 {
			t.Fatalf("product q exit: %v", err)
		}
	}
	if err := report.Close(); err != nil {
		t.Fatal(err)
	}
	assertLinuxPTYTermios(t, fd, original, "normal command completion")
	if err := assertLinuxPTYRun(database, key); err != nil {
		t.Fatalf("read completed stopped history: %v", err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Latest  []map[string]any `json:"latest"`
		Summary []map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode lifecycle report: %v; report=%q", err, data)
	}
	if len(result.Latest) != 1 || len(result.Summary) != 1 {
		t.Fatalf("lifecycle report sections latest=%d summary=%d", len(result.Latest), len(result.Summary))
	}
	if got := result.Latest[0]["tcp_"+port]; got != "open" {
		t.Fatalf("lifecycle TCP result=%v, want open", got)
	}
	mark("DONE")
}

func signalStatusLines(status string) string {
	var lines []string
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "SigCgt:") || strings.HasPrefix(line, "SigIgn:") || strings.HasPrefix(line, "State:") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, " ")
}

func assertLinuxPTYTermios(t *testing.T, fd int, want *unix.Termios, phase string) {
	t.Helper()
	got, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatalf("read %s termios: %v", phase, err)
	}
	if *got != *want {
		t.Fatalf("%s termios changed: got=%+v want=%+v", phase, got, want)
	}
}

func waitLinuxProcessState(pid int, want byte, timeout time.Duration) error {
	return waitLinuxProcessPredicate(pid, timeout, func(state byte) bool { return state == want })
}

func waitLinuxProcessRunning(pid int, timeout time.Duration) error {
	return waitLinuxProcessPredicate(pid, timeout, func(state byte) bool { return state != 'T' && state != 'Z' })
}

func waitLinuxProcessPredicate(pid int, timeout time.Duration, match func(byte) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		end := strings.LastIndexByte(string(data), ')')
		if end >= 0 {
			fields := strings.Fields(string(data[end+1:]))
			if len(fields) > 0 && len(fields[0]) == 1 && match(fields[0][0]) {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not reach requested state before timeout", pid)
}

func signalResetJobControl() {
	// Keep the controller's own process in the standard job-control state until
	// the product has been exec'd with default SIGTTOU.
	signal.Reset(syscall.SIGTTOU, syscall.SIGTSTP, syscall.SIGCONT)
}

func signalIgnoreJobControl() {
	// The controller must be able to change the terminal foreground group after
	// it moves to the background; the already-started product keeps default SIGTTOU.
	signal.Ignore(syscall.SIGTTOU)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
