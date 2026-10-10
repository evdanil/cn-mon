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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/target"
	"golang.org/x/sys/unix"
)

func TestNativeCommandLinuxPTYLossWithInheritedHangupIgnored(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "lost-terminal-history.db")
	logPath := filepath.Join(directory, "lost-terminal-cn.log")
	reportPath := filepath.Join(directory, "lost-terminal-report.json")
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
	defer markerRead.Close()
	defer markerWrite.Close()
	var controllerOutput bytes.Buffer
	controller := exec.Command(os.Args[0], "-test.run=^TestNativeCommandLinuxPTYLossController$")
	controller.Env = append(nativeCommandEnv(t, directory, database, logPath),
		"CNMON_LINUX_LOSS_HELPER=1",
		"CNMON_LINUX_LOSS_BINARY="+binary,
		"CNMON_LINUX_LOSS_PORT="+strconv.Itoa(port),
		"CNMON_LINUX_LOSS_REPORT="+reportPath,
	)
	controller.Stdin, controller.Stderr, controller.Stdout = slave, slave, &controllerOutput
	controller.ExtraFiles = []*os.File{markerWrite}
	controller.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := controller.Start(); err != nil {
		t.Fatalf("start terminal-loss controller: %v", err)
	}
	controllerWait := make(chan error, 1)
	go func() { controllerWait <- controller.Wait() }()
	controllerReaped := false
	reapController := func(timeout time.Duration) (error, bool) {
		select {
		case err := <-controllerWait:
			controllerReaped = true
			return err, true
		case <-time.After(timeout):
			return nil, false
		}
	}
	_ = markerWrite.Close()
	var terminalOutput nativePTYOutput
	readerStop := make(chan struct{})
	readerDone := make(chan struct{})
	var stopReaderOnce sync.Once
	stopReader := func() {
		stopReaderOnce.Do(func() {
			close(readerStop)
			select {
			case <-readerDone:
			case <-time.After(2 * time.Second):
				t.Error("PTY collector did not stop within 2s")
			}
		})
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
			if readErr != nil && !errors.Is(readErr, unix.EAGAIN) {
				return
			}
		}
	}()
	markers := make(chan string, 4)
	scanner := bufio.NewScanner(markerRead)
	go func() {
		for scanner.Scan() {
			markers <- scanner.Text()
		}
		close(markers)
	}()
	defer func() {
		stopReader()
		_ = master.Close()
		if !controllerReaped {
			_ = controller.Process.Kill()
			select {
			case <-controllerWait:
				controllerReaped = true
			case <-time.After(2 * time.Second):
				t.Error("terminal-loss controller did not reap within 2s after kill")
			}
		}
	}()
	timeout := time.NewTimer(25 * time.Second)
	defer timeout.Stop()
	ready, done := false, false
	postLossCPUTicks := uint64(0)
	request := linuxLossRequestKey(t, strconv.Itoa(port))
	var baselineRounds int64
	for !done {
		select {
		case marker, ok := <-markers:
			if !ok {
				waitErr, reaped := reapController(2 * time.Second)
				t.Fatalf("terminal-loss helper ended early; reaped=%t wait=%v controller=%q terminal=%q", reaped, waitErr, controllerOutput.String(), terminalOutput.bytes())
			}
			switch {
			case strings.HasPrefix(marker, "READY_FOR_TERMINAL_LOSS:"):
				pid, parseErr := strconv.Atoi(strings.TrimPrefix(marker, "READY_FOR_TERMINAL_LOSS:"))
				if parseErr != nil {
					t.Fatalf("parse terminal-loss product pid from %q: %v", marker, parseErr)
				}
				cpuBefore, cpuErr := linuxProcessCPU(pid)
				if cpuErr != nil {
					t.Fatalf("read product CPU before terminal loss: %v", cpuErr)
				}
				if !linuxProcessIgnoresSignal(pid, syscall.SIGHUP) {
					t.Fatal("product did not inherit ignored SIGHUP disposition")
				}
				mode, modeErr := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
				if modeErr != nil || *mode == *original {
					t.Fatalf("product did not activate terminal before loss: mode=%+v original=%+v err=%v", mode, original, modeErr)
				}
				if !terminalOutput.waitContains([]byte("\x1b[?25l"), time.Second) {
					t.Fatalf("product did not draw its live frame before terminal loss: %q", terminalOutput.bytes())
				}
				stopReader()
				if err := master.Close(); err != nil {
					t.Fatalf("close PTY master to deliver terminal loss: %v", err)
				}
				baseline := nativeHistoryRequest(t, database, request)
				if baseline == nil || baseline.NewestRun == nil || baseline.NewestRun.Status != "running" || baseline.NewestRun.Rounds < 1 {
					t.Fatalf("post-loss baseline history is not a running completed round: %+v", baseline)
				}
				baselineRounds = baseline.NewestRun.Rounds
				ready = true
				time.Sleep(2 * time.Second)
				cpuAfter, cpuErr := linuxProcessCPU(pid)
				if cpuErr != nil {
					t.Fatalf("read product CPU after terminal loss: %v", cpuErr)
				}
				if used := cpuAfter - cpuBefore; used > 50 {
					t.Fatalf("product used %d CPU ticks in 2s after PTY loss; possible busy key poll", used)
				} else {
					postLossCPUTicks = used
				}
			case marker == "DONE":
				done = true
			}
		case <-timeout.C:
			t.Fatalf("terminal-loss command timed out; ready=%t output=%q", ready, controllerOutput.String())
		}
	}
	if err, reaped := reapController(2 * time.Second); !reaped {
		t.Fatalf("terminal-loss controller did not exit within 2s after helper completion; output=%q", controllerOutput.String())
	} else if err != nil {
		t.Fatalf("terminal-loss controller: %v; output=%q", err, controllerOutput.String())
	}
	if !ready {
		t.Fatal("terminal loss was delivered before a useful round completed")
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Latest  []map[string]any `json:"latest"`
		Summary []struct {
			Rounds int `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode terminal-loss report: %v; report=%q", err, data)
	}
	if len(report.Latest) != 1 || len(report.Summary) != 1 || int64(report.Summary[0].Rounds) <= baselineRounds {
		t.Fatalf("terminal-loss report rounds=%+v did not exceed post-loss baseline %d", report.Summary, baselineRounds)
	}
	if report.Latest[0]["tcp_"+strconv.Itoa(port)] != "open" {
		t.Fatalf("terminal-loss TCP result=%v, want open", report.Latest[0]["tcp_"+strconv.Itoa(port)])
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read terminal-loss command log: %v", err)
	}
	for _, message := range []string{"starting target run", "target run finished"} {
		if !bytes.Contains(logData, []byte(message)) {
			t.Fatalf("terminal-loss log omitted %q: %q", message, logData)
		}
	}
	info := nativeHistoryRequest(t, database, request)
	if info == nil || info.NewestRun == nil || info.NewestRun.Status != "finished" || info.NewestRun.Rounds != int64(report.Summary[0].Rounds) {
		t.Fatalf("terminal-loss history did not finish: %+v", info)
	}
	t.Logf("CNMON_NATIVE_COMMAND_LINUX_PTY terminal-loss=inherited-hup-ignore useful-round=complete report=complete history=finished post-loss-cpu-ticks-per-2s=%d", postLossCPUTicks)
}

func TestNativeCommandLinuxPTYLossController(t *testing.T) {
	if os.Getenv("CNMON_LINUX_LOSS_HELPER") != "1" {
		return
	}
	markers := os.NewFile(3, "terminal-loss-markers")
	if markers == nil {
		t.Fatal("missing terminal-loss marker descriptor")
	}
	defer markers.Close()
	mark := func(value string) {
		t.Helper()
		if _, err := fmt.Fprintln(markers, value); err != nil {
			t.Fatalf("write terminal-loss marker %q: %v", value, err)
		}
	}
	signal.Ignore(syscall.SIGHUP)
	binary := os.Getenv("CNMON_LINUX_LOSS_BINARY")
	port := os.Getenv("CNMON_LINUX_LOSS_PORT")
	reportPath := os.Getenv("CNMON_LINUX_LOSS_REPORT")
	directory := filepath.Dir(os.Getenv("CNMON_NATIVE_HISTORY"))
	database := os.Getenv("CNMON_NATIVE_HISTORY")
	logPath := os.Getenv("CNMON_NATIVE_LOG")
	report, err := os.Create(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", port, "--every", "10s", "--for", "11s", "127.0.0.1")
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdin, command.Stderr, command.Stdout = os.Stdin, os.Stderr, report
	if err := command.Start(); err != nil {
		_ = report.Close()
		t.Fatalf("start command in inherited-HUP-ignore session: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			select {
			case <-wait:
				waited = true
			case <-time.After(2 * time.Second):
				t.Error("terminal-loss command did not reap within 2s after kill")
			}
		}
	}()
	request := linuxLossRequestKey(t, port)
	if err := waitLinuxPTYCompletedRounds(database, request, 1, 15*time.Second); err != nil {
		logBytes, _ := os.ReadFile(logPath)
		info := nativeHistoryRequest(t, database, request)
		t.Fatalf("wait for a completed round before terminal loss: %v; run=%+v log=%q", err, info, logBytes)
	}
	mark(fmt.Sprintf("READY_FOR_TERMINAL_LOSS:%d", command.Process.Pid))
	select {
	case err := <-wait:
		waited = true
		code := nativeExitCode(err)
		if code != 0 && code != 3 {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("wait terminal-loss command: %v", err)
			}
			t.Fatalf("terminal-loss exit=%d, want completed report or probe failure", code)
		}
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		select {
		case <-wait:
			waited = true
		case <-time.After(2 * time.Second):
			t.Fatal("inherited-HUP-ignore command did not reap within 2s after kill")
		}
		t.Fatal("inherited-HUP-ignore command did not finish after terminal loss")
	}
	if err := report.Close(); err != nil {
		t.Fatal(err)
	}
	info := nativeHistoryRequest(t, database, request)
	if info == nil || info.NewestRun == nil || info.NewestRun.Status != "finished" || info.NewestRun.Rounds < 1 {
		t.Fatalf("terminal-loss command did not finish useful history: %+v", info)
	}
	mark("DONE")
}

func linuxProcessCPU(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 13 {
		return 0, fmt.Errorf("short /proc/%d/stat", pid)
	}
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	system, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return user + system, nil
}

func linuxProcessIgnoresSignal(pid int, signal syscall.Signal) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "SigIgn:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false
		}
		mask, err := strconv.ParseUint(fields[1], 16, 64)
		return err == nil && mask&(uint64(1)<<uint(signal-1)) != 0
	}
	return false
}

func linuxLossRequestKey(t *testing.T, port string) string {
	t.Helper()
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse terminal-loss TCP port: %v", err)
	}
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{portNumber})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build terminal-loss request: rejected=%v err=%v", rejected, err)
	}
	return request.Key
}
