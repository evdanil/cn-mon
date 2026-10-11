// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux && native

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/target"
	"golang.org/x/sys/unix"
)

// TestNativeReleaseFollowDoesNotStopWriter qualifies extracted --follow with a
// real PTY while an independent extracted command continues committing rounds.
func TestNativeReleaseFollowDoesNotStopWriter(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "release follow history.db")
	writerLog := filepath.Join(directory, "release writer.log")
	followerLog := filepath.Join(directory, "release follower.log")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for extracted follow writer: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build extracted follow request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}

	writerCtx, writerCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer writerCancel()
	writer := exec.CommandContext(writerCtx, binary, "--format", "json", "--history-file", database, "--log-file", writerLog,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1")
	writer.Env = nativeCommandEnv(t, directory, database, writerLog)
	var writerStdout, writerStderr bytes.Buffer
	writer.Stdout, writer.Stderr = &writerStdout, &writerStderr
	if err := writer.Start(); err != nil {
		t.Fatalf("start extracted history writer: %v", err)
	}
	writerWait := make(chan error, 1)
	go func() { writerWait <- writer.Wait() }()
	writerWaited := false
	defer func() {
		if !writerWaited {
			_ = writer.Process.Kill()
			select {
			case <-writerWait:
			case <-time.After(5 * time.Second):
				t.Error("extracted writer did not exit after kill")
			}
		}
	}()
	if err := waitLinuxPTYCompletedRounds(database, request.Key, 1, 30*time.Second); err != nil {
		t.Fatalf("wait for extracted writer's first committed round: %v; stderr=%q", err, writerStderr.String())
	}
	if err := waitLinuxPTYCommandStatus(database, request.Key, "running", time.Second); err != nil {
		t.Fatalf("extracted writer is not active before follow: %v", err)
	}

	master, slave := openNativeLinuxPTY(t)
	defer master.Close()
	defer slave.Close()
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read release follow PTY termios: %v", err)
	}
	follower := exec.CommandContext(context.Background(), binary, "--follow", request.Key[:8], "--format", "json",
		"--history-file", database, "--log-file", followerLog)
	follower.Env = nativeCommandEnv(t, directory, database, followerLog)
	follower.Stdin, follower.Stderr = slave, slave
	var followerStdout bytes.Buffer
	follower.Stdout = &followerStdout
	follower.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := follower.Start(); err != nil {
		t.Fatalf("start extracted follow reader: %v", err)
	}
	followerWait := make(chan error, 1)
	go func() { followerWait <- follower.Wait() }()
	followerWaited := false
	output := &nativePTYOutput{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go collectNativeLinuxPTY(master, output, stop, done)
	stopCollector := func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("extracted follow PTY collector did not stop")
		}
	}
	defer func() {
		if !followerWaited {
			_ = follower.Process.Kill()
			select {
			case <-followerWait:
			case <-time.After(5 * time.Second):
				t.Error("extracted follow reader did not exit after kill")
			}
		}
		stopCollector()
		_ = master.Close()
	}()
	if err := waitLinuxPTYMode(slave, original, 5*time.Second); err != nil {
		t.Fatalf("extracted follow did not activate its real PTY input: %v", err)
	}
	if !output.waitContains([]byte("127.0.0.1"), 10*time.Second) {
		t.Fatalf("extracted follow did not display the committed request: %q", output.tail(3000))
	}
	baselineRounds, active, err := nativeReleaseRunRounds(database, request.Key)
	if err != nil || !active || baselineRounds < 1 {
		t.Fatalf("read committed round baseline after extracted follow activation: rounds=%d active=%t err=%v", baselineRounds, active, err)
	}
	if err := waitLinuxPTYCompletedRounds(database, request.Key, baselineRounds+1, 25*time.Second); err != nil {
		t.Fatalf("extracted writer did not commit a new round while follow was active after baseline=%d: %v", baselineRounds, err)
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatalf("send q to extracted follow: %v", err)
	}
	select {
	case err := <-followerWait:
		followerWaited = true
		if code := childExitCode(err); code != 0 {
			t.Fatalf("extracted follow q exit=%d err=%v stderr=%q", code, err, output.tail(3000))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("extracted follow did not exit after q")
	}
	if err := waitLinuxPTYRestored(slave, original, 2*time.Second); err != nil {
		t.Fatalf("extracted follow terminal restore: %v", err)
	}
	stopCollector()

	var report struct {
		Seen    []json.RawMessage            `json:"seen"`
		Latest  []map[string]json.RawMessage `json:"latest"`
		Summary []struct {
			Rounds int64 `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(followerStdout.Bytes(), &report); err != nil {
		t.Fatalf("decode extracted follow final JSON: %v stdout=%q tty=%q", err, followerStdout.String(), output.tail(3000))
	}
	if len(report.Seen) != 1 || len(report.Latest) != 1 || len(report.Summary) != 1 || report.Summary[0].Rounds < baselineRounds+1 {
		t.Fatalf("extracted follow final report lacks new committed data: seen=%d latest=%d summary=%+v", len(report.Seen), len(report.Latest), report.Summary)
	}
	var tcp string
	if err := json.Unmarshal(report.Latest[0][fmt.Sprintf("tcp_%d", port)], &tcp); err != nil || tcp != "open" {
		t.Fatalf("extracted follow final TCP=%q err=%v latest=%v", tcp, err, report.Latest)
	}
	if err := waitLinuxPTYCommandStatus(database, request.Key, "running", 2*time.Second); err != nil {
		t.Fatalf("q stopped the extracted writer instead of only the follower: %v", err)
	}
	if err := writer.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop extracted writer after follower assertion: %v", err)
	}
	select {
	case err := <-writerWait:
		writerWaited = true
		if code := childExitCode(err); code != 0 && code != 3 {
			t.Fatalf("extracted writer graceful exit=%d err=%v stderr=%q stdout=%q", code, err, writerStderr.String(), writerStdout.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("extracted writer did not stop gracefully")
	}
	store, err := history.OpenReader(context.Background(), database)
	if err != nil {
		t.Fatalf("open extracted follow history: %v", err)
	}
	err = store.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.NewestRun == nil || info.NewestRun.Status != "stopped" || info.NewestRun.Rounds < 2 {
			return fmt.Errorf("writer history after follower q=%+v", info)
		}
		return nil
	})
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("verify independent writer stopped history: view=%v close=%v", err, closeErr)
	}
	t.Log("CNMON_NATIVE_RELEASE_FOLLOW new-round=committed final-report=json q=follower-only writer=active tcp=open history=verified terminal=restored")
}

func TestNativeReleaseEndedFollowAllFormats(t *testing.T) {
	requireNativeReleaseQualification(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "ended follow history.db")
	logPath := filepath.Join(directory, "ended follow command.log")
	request, rejected, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build seeded ended request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	seedCommandHistory(t, database, request)
	for _, format := range []string{"json", "table", "csv", "md"} {
		t.Run(format, func(t *testing.T) {
			args := []string{"--follow", request.Key[:8], "--format", format, "--history-file", database, "--log-file", logPath}
			stdout, err := runNativeReleaseEndedFollowPTY(t, binary, directory, database, logPath, args)
			if err != nil {
				t.Fatalf("extracted ended follow exit: %v stdout=%q", err, stdout)
			}
			if !bytes.Contains(stdout, []byte("127.0.0.1")) {
				t.Fatalf("extracted ended follow format=%s omitted seeded host: %q", format, stdout)
			}
			switch format {
			case "json":
				var report map[string][]json.RawMessage
				if err := json.Unmarshal(stdout, &report); err != nil {
					t.Fatalf("decode extracted follow JSON: %v stdout=%q", err, stdout)
				}
				for _, section := range []string{"seen", "latest", "summary"} {
					if len(report[section]) != 1 {
						t.Fatalf("extracted follow JSON section %s rows=%d, want one", section, len(report[section]))
					}
				}
			case "table":
				for _, section := range []string{"SEEN", "LATEST", "SUMMARY", "Host", "Result"} {
					if !bytes.Contains(stdout, []byte(section)) {
						t.Fatalf("extracted follow table missing %q: %q", section, stdout)
					}
				}
			case "csv":
				records, err := csv.NewReader(bytes.NewReader(stdout)).ReadAll()
				if err != nil || len(records) < 4 || records[0][0] != "section" || !containsCSVSection(records, "seen") || !containsCSVSection(records, "latest") || !containsCSVSection(records, "summary") {
					t.Fatalf("extracted follow CSV sections/header malformed: records=%v err=%v", records, err)
				}
			case "md":
				for _, section := range []string{"## SEEN", "## LATEST", "## SUMMARY", "| Host | Address | Result |"} {
					if !bytes.Contains(stdout, []byte(section)) {
						t.Fatalf("extracted follow Markdown missing %q: %q", section, stdout)
					}
				}
			}
		})
	}
	store, err := history.OpenReader(context.Background(), database)
	if err != nil {
		t.Fatalf("open ended follow history after formats: %v", err)
	}
	err = store.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Runs != 1 || info.Rounds != 1 || info.NewestRun == nil || info.NewestRun.Status != "finished" {
			return fmt.Errorf("ended follow modified seeded history: %+v", info)
		}
		return nil
	})
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("verify ended follow history unchanged: view=%v close=%v", err, closeErr)
	}
	t.Log("CNMON_NATIVE_RELEASE_FOLLOW ended=read-only formats=json,table,csv,md exit=0 history=unchanged terminal=restored")
}

func containsCSVSection(records [][]string, section string) bool {
	for _, record := range records[1:] {
		if len(record) > 0 && record[0] == section {
			return true
		}
	}
	return false
}

func runNativeReleaseEndedFollowPTY(t *testing.T, binary, directory, database, logPath string, args []string) ([]byte, error) {
	t.Helper()
	master, slave := openNativeLinuxPTY(t)
	original, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		master.Close()
		slave.Close()
		t.Fatalf("read ended follow terminal mode: %v", err)
	}
	command := exec.Command(binary, args...)
	command.Dir = directory
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdin, command.Stderr = slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, fmt.Errorf("start extracted ended follow: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	output := &nativePTYOutput{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go collectNativeLinuxPTY(master, output, stop, done)
	stopAndJoin := func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("ended follow PTY collector did not stop")
		}
	}
	completed := false
	defer func() {
		if !completed {
			_ = command.Process.Kill()
			select {
			case <-wait:
			case <-time.After(5 * time.Second):
				t.Error("ended follow process did not exit after kill")
			}
		}
		stopAndJoin()
		_ = master.Close()
		_ = slave.Close()
	}()
	select {
	case err := <-wait:
		completed = true
		if err != nil {
			return stdout.Bytes(), err
		}
	case <-time.After(15 * time.Second):
		return stdout.Bytes(), fmt.Errorf("extracted ended follow timed out; tty=%q", output.tail(2000))
	}
	if err := waitLinuxPTYRestored(slave, original, 2*time.Second); err != nil {
		return stdout.Bytes(), err
	}
	stopAndJoin()
	return stdout.Bytes(), nil
}

func nativeReleaseRunRounds(database, key string) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store, err := history.OpenReader(ctx, database)
	if err != nil {
		return 0, false, err
	}
	defer store.Close()
	var rounds int64
	var active bool
	err = store.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info != nil && info.NewestRun != nil {
			rounds = info.NewestRun.Rounds
			active = info.NewestRun.Status == "running"
		}
		return nil
	})
	return rounds, active, err
}
