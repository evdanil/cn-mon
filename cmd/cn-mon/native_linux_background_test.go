//go:build linux && native

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/target"
	"golang.org/x/sys/unix"
)

func TestNativeCommandLinuxPTYBackgroundCleanup(t *testing.T) {
	requireNativeCommand(t)
	for _, exitMode := range []string{"normal", "graceful", "report-error", "hard"} {
		for _, tostop := range []bool{false, true} {
			name := fmt.Sprintf("%s/tostop=%t", exitMode, tostop)
			t.Run(name, func(t *testing.T) {
				runNativeLinuxBackgroundCleanup(t, exitMode, tostop)
			})
		}
	}
}

func runNativeLinuxBackgroundCleanup(t *testing.T, exitMode string, tostop bool) {
	t.Helper()
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "background-history.db")
	logPath := filepath.Join(directory, "background-cn.log")
	reportPath := filepath.Join(directory, "background-report.json")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	master, slave := openNativeLinuxPTY(t)
	defer slave.Close()
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	markerRead, markerWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	proceedRead, proceedWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer markerRead.Close()
	defer markerWrite.Close()
	defer proceedRead.Close()
	defer proceedWrite.Close()
	var controllerOutput lockedNativeBuffer
	controller := exec.Command(os.Args[0], "-test.run=^TestNativeCommandLinuxPTYBackgroundCleanupController$")
	controller.Env = append(nativeCommandEnv(t, directory, database, logPath),
		"CNMON_LINUX_BACKGROUND_HELPER=1",
		"CNMON_LINUX_BACKGROUND_BINARY="+binary,
		"CNMON_LINUX_BACKGROUND_PORT="+strconv.Itoa(port),
		"CNMON_LINUX_BACKGROUND_REPORT="+reportPath,
		"CNMON_LINUX_BACKGROUND_MODE="+exitMode,
		"CNMON_LINUX_BACKGROUND_TOSTOP="+strconv.FormatBool(tostop),
	)
	controller.Stdin, controller.Stderr, controller.Stdout = slave, slave, &controllerOutput
	controller.ExtraFiles = []*os.File{markerWrite, proceedRead}
	controller.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := controller.Start(); err != nil {
		t.Fatalf("start background cleanup controller: %v", err)
	}
	controllerWait := make(chan error, 1)
	go func() { controllerWait <- controller.Wait() }()
	controllerWaitCollected := false
	defer func() {
		if !controllerWaitCollected {
			_ = controller.Process.Kill()
			select {
			case <-controllerWait:
				controllerWaitCollected = true
			case <-time.After(2 * time.Second):
				t.Error("background controller wait did not finish after kill")
			}
		}
	}()
	_ = markerWrite.Close()
	_ = proceedRead.Close()
	terminalOutput := &nativePTYOutput{}
	terminalDone := make(chan struct{})
	terminalStop := make(chan struct{})
	terminalStopped := false
	go collectNativeLinuxPTY(master, terminalOutput, terminalStop, terminalDone)
	defer func() {
		if !terminalStopped {
			close(terminalStop)
			terminalStopped = true
		}
		select {
		case <-terminalDone:
		case <-time.After(time.Second):
			t.Error("background cleanup PTY collector did not stop")
		}
		_ = master.Close()
	}()
	markerScanner := bufio.NewScanner(markerRead)
	markers := make(chan string, 8)
	markerDone := make(chan struct{})
	go func() {
		defer close(markerDone)
		for markerScanner.Scan() {
			markers <- markerScanner.Text()
		}
		close(markers)
	}()
	defer func() {
		_ = markerRead.Close()
		select {
		case <-markerDone:
		case <-time.After(time.Second):
			t.Error("background marker reader did not stop")
		}
	}()
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	var suffixAt int
	var result string
	for result == "" {
		select {
		case marker, ok := <-markers:
			if !ok {
				t.Fatalf("background helper ended early: output=%q markers=%q", controllerOutput.String(), terminalOutput.bytes())
			}
			switch {
			case marker == "FOREGROUND_ACTIVE":
				if !terminalOutput.waitContains([]byte("\x1b[?25l"), 3*time.Second) {
					t.Fatalf("command did not produce a foreground live frame: %q", terminalOutput.bytes())
				}
				if _, err := fmt.Fprintln(proceedWrite, "BACKGROUND"); err != nil {
					t.Fatalf("authorize background transition: %v", err)
				}
			case marker == "BACKGROUND_READY":
				if !terminalOutput.waitQuiet(150*time.Millisecond, time.Second) {
					t.Fatalf("terminal output did not drain at background boundary: %q", terminalOutput.tail(2048))
				}
				suffixAt = terminalOutput.length()
				if _, err := fmt.Fprintln(proceedWrite, "CAPTURE_READY"); err != nil {
					t.Fatalf("acknowledge background output boundary: %v", err)
				}
			case strings.HasPrefix(marker, "RESULT "):
				result = strings.TrimPrefix(marker, "RESULT ")
			case marker == "STOPPED_SIGTTOU":
				t.Fatalf("child stopped by SIGTTOU while terminating in background: %q", terminalOutput.bytes())
			}
		case <-deadline.C:
			t.Fatalf("background cleanup timed out mode=%s tostop=%t output=%q controller=%q", exitMode, tostop, terminalOutput.bytes(), controllerOutput.String())
		}
	}
	var controllerErr error
	select {
	case controllerErr = <-controllerWait:
		controllerWaitCollected = true
	case <-time.After(2 * time.Second):
		t.Fatal("background controller did not exit after result marker")
	}
	if controllerErr != nil {
		t.Fatalf("background helper: %v; output=%q", controllerErr, controllerOutput.String())
	}
	if !terminalOutput.waitQuiet(150*time.Millisecond, time.Second) {
		t.Fatalf("terminal output did not drain after product exit: %q", terminalOutput.tail(2048))
	}
	close(terminalStop)
	terminalStopped = true
	select {
	case <-terminalDone:
	case <-time.After(time.Second):
		t.Fatal("background PTY collector did not stop after product exit")
	}
	ownerMode := *original
	ownerMode.Lflag |= unix.ICANON | unix.ECHO
	if tostop {
		ownerMode.Lflag |= unix.TOSTOP
	} else {
		ownerMode.Lflag &^= unix.TOSTOP
	}
	assertLinuxPTYTermios(t, int(slave.Fd()), &ownerMode, "background cleanup completion")
	if suffixAt < 0 || suffixAt > terminalOutput.length() {
		t.Fatalf("invalid background output boundary %d", suffixAt)
	}
	suffix := terminalOutput.bytes()[suffixAt:]
	if bytes.Contains(suffix, []byte("\x1b[?25l")) || bytes.Contains(suffix, []byte("\x1b[H")) || bytes.Contains(suffix, []byte("\x1b[2J")) {
		t.Fatalf("background cleanup redrew or hid the cursor: %q", suffix)
	}
	showCount := bytes.Count(suffix, []byte("\x1b[?25h"))
	wantShow := 0
	if !tostop {
		wantShow = 1
	}
	if showCount != wantShow {
		t.Fatalf("background cleanup cursor-show writes=%d, want %d (TOSTOP=%t): %q", showCount, wantShow, tostop, suffix)
	}
	if exitMode == "hard" && result != "130" {
		t.Fatalf("hard background exit=%s, want 130", result)
	}
	if exitMode == "report-error" && result != "3" {
		t.Fatalf("real report destination error exit=%s, want 3", result)
	}
	if exitMode == "report-error" {
		hasReportDiagnostic := bytes.Contains(suffix, []byte("cn-mon: write report:"))
		if hasReportDiagnostic == tostop {
			t.Fatalf("report-error diagnostic visibility=%t with TOSTOP=%t; output tail=%q", hasReportDiagnostic, tostop, suffix)
		}
		logData, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read report-error log: %v", err)
		}
		if !bytes.Contains(logData, []byte("report write failed")) {
			t.Fatalf("report error was not preserved in file log: %q", logData)
		}
	}
	if exitMode != "hard" && exitMode != "report-error" && result != "0" && result != "3" {
		t.Fatalf("background exit=%s, want completed 0/3", result)
	}
	key := nativeRequestKeyWithPort(t, port)
	info := nativeHistoryRequest(t, database, key)
	if info == nil || info.NewestRun == nil || info.NewestRun.Rounds < 1 {
		t.Fatalf("background exit did not preserve completed history: %+v", info)
	}
	wantStatus := ""
	switch exitMode {
	case "normal", "report-error":
		wantStatus = "finished"
	case "graceful":
		wantStatus = "stopped"
	}
	if wantStatus != "" && info.NewestRun.Status != wantStatus {
		t.Fatalf("background exit mode=%s history status=%q, want %q", exitMode, info.NewestRun.Status, wantStatus)
	}
	if exitMode == "normal" || exitMode == "graceful" {
		reportBytes, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("read final background JSON report: %v", err)
		}
		var report struct {
			Seen    []json.RawMessage `json:"seen"`
			Latest  []json.RawMessage `json:"latest"`
			Summary []struct {
				Rounds int64 `json:"rounds"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(reportBytes, &report); err != nil {
			t.Fatalf("decode final background JSON report: %v; body=%q", err, reportBytes)
		}
		if len(report.Seen) != 1 || len(report.Latest) != 1 || len(report.Summary) != 1 || report.Summary[0].Rounds != info.NewestRun.Rounds {
			t.Fatalf("final JSON/history disagree: seen=%d latest=%d summary=%+v history-rounds=%d", len(report.Seen), len(report.Latest), report.Summary, info.NewestRun.Rounds)
		}
	}
	t.Logf("CNMON_NATIVE_COMMAND_LINUX_PTY background-exit=%s tostop=%t owner-mode=unchanged frame=none cursor-show=%d", exitMode, tostop, showCount)
}

func TestNativeCommandLinuxPTYBackgroundCleanupController(t *testing.T) {
	if os.Getenv("CNMON_LINUX_BACKGROUND_HELPER") != "1" {
		return
	}
	markers := os.NewFile(3, "background-markers")
	if markers == nil {
		t.Fatal("missing background marker fd")
	}
	defer markers.Close()
	proceed := os.NewFile(4, "background-control")
	if proceed == nil {
		t.Fatal("missing background control fd")
	}
	defer proceed.Close()
	mark := func(value string) {
		t.Helper()
		if _, err := fmt.Fprintln(markers, value); err != nil {
			t.Fatalf("write background marker: %v", err)
		}
	}
	fd := int(os.Stdin.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	controllerGroup := unix.Getpgrp()
	signalResetJobControl()
	binary := os.Getenv("CNMON_LINUX_BACKGROUND_BINARY")
	port := os.Getenv("CNMON_LINUX_BACKGROUND_PORT")
	mode := os.Getenv("CNMON_LINUX_BACKGROUND_MODE")
	tostop, err := strconv.ParseBool(os.Getenv("CNMON_LINUX_BACKGROUND_TOSTOP"))
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(os.Getenv("CNMON_NATIVE_HISTORY"))
	database := os.Getenv("CNMON_NATIVE_HISTORY")
	logPath := os.Getenv("CNMON_NATIVE_LOG")
	reportPath := os.Getenv("CNMON_LINUX_BACKGROUND_REPORT")
	var stdout *os.File
	if mode == "report-error" {
		stdout, err = os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("open real failing report destination: %v", err)
		}
	} else {
		stdout, err = os.Create(reportPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(binary, "--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", port, "--every", "10s", "--for", "30s", "127.0.0.1")
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdin, command.Stderr, command.Stdout = os.Stdin, os.Stderr, stdout
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	waitCollected := false
	defer func() {
		if !waitCollected {
			_ = command.Process.Kill()
			select {
			case <-wait:
				waitCollected = true
			case <-time.After(2 * time.Second):
				t.Error("background product wait did not finish after kill")
			}
		}
	}()
	if pgrp, _ := unix.Getpgid(command.Process.Pid); pgrp != command.Process.Pid {
		t.Fatalf("product process group=%d, want pid=%d", pgrp, command.Process.Pid)
	}
	controllerSession, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	if session, _ := unix.Getsid(command.Process.Pid); session != controllerSession {
		t.Fatalf("product did not remain in controller session: child=%d controller=%d", session, controllerSession)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, command.Process.Pid); err != nil {
		t.Fatalf("foreground product before live-screen check: %v", err)
	}
	signalIgnoreJobControl()
	if err := waitLinuxPTYMode(os.Stdin, original, 5*time.Second); err != nil {
		t.Fatalf("foreground product did not enable terminal input mode: %v", err)
	}
	key := nativeRequestKeyWithPort(t, mustParsePort(t, port))
	if err := waitLinuxPTYCompletedRounds(database, key, 1, 15*time.Second); err != nil {
		t.Fatalf("wait for background cleanup first round: %v", err)
	}
	mark("FOREGROUND_ACTIVE")
	reader := bufio.NewScanner(proceed)
	if !reader.Scan() || reader.Text() != "BACKGROUND" {
		t.Fatalf("parent did not authorize background transition: %v", reader.Err())
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, controllerGroup); err != nil {
		t.Fatalf("background product: %v", err)
	}
	ownerMode := *original
	ownerMode.Lflag |= unix.ICANON | unix.ECHO
	if tostop {
		ownerMode.Lflag |= unix.TOSTOP
	} else {
		ownerMode.Lflag &^= unix.TOSTOP
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &ownerMode); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	assertLinuxPTYTermios(t, fd, &ownerMode, "background owner before exit")
	mark("BACKGROUND_READY")
	if !reader.Scan() || reader.Text() != "CAPTURE_READY" {
		t.Fatalf("parent did not acknowledge output boundary: %v", reader.Err())
	}
	if mode == "graceful" {
		if err := command.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "hard" {
		unlock := acquireNativeWriteLock(t, database)
		defer unlock()
		if err := command.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case waitErr := <-wait:
			waitCollected = true
			t.Fatalf("first background interrupt exited before the second signal: %v", waitErr)
		case <-time.After(250 * time.Millisecond):
		}
		if err := command.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.NewTimer(40 * time.Second)
	defer deadline.Stop()
	var waitErr error
	select {
	case waitErr = <-wait:
		waitCollected = true
	case <-deadline.C:
		if waitLinuxProcessState(command.Process.Pid, 'T', 100*time.Millisecond) == nil {
			mark("STOPPED_SIGTTOU")
			_ = command.Process.Kill()
		}
		t.Fatal("background command did not exit before deadline")
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	assertLinuxPTYTermios(t, fd, &ownerMode, "background owner after product exit")
	code := nativeExitCode(waitErr)
	mark("RESULT " + strconv.Itoa(code))
	mark("DONE")
}

func nativeRequestKeyWithPort(t *testing.T, port int) string {
	t.Helper()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build background request key: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	return request.Key
}

func mustParsePort(t *testing.T, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("parse background port %q: %v", value, err)
	}
	return port
}
