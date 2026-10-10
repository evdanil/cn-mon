//go:build native && windows

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/target"
)

const (
	windowsCreateNewConsole = 0x00000010
	windowsDetachedProcess  = 0x00000008
	windowsCtrlCEvent       = 0
	windowsInvalidHandle    = syscall.Errno(6)
)

var (
	windowsKernel32              = syscall.NewLazyDLL("kernel32.dll")
	windowsFreeConsole           = windowsKernel32.NewProc("FreeConsole")
	windowsAttachConsole         = windowsKernel32.NewProc("AttachConsole")
	windowsSetConsoleCtrlHandler = windowsKernel32.NewProc("SetConsoleCtrlHandler")
	windowsGenerateCtrlEvent     = windowsKernel32.NewProc("GenerateConsoleCtrlEvent")
	windowsPeekNamedPipe         = windowsKernel32.NewProc("PeekNamedPipe")
)

type nativeWindowsCommand struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdoutIn    io.ReadCloser
	done        chan struct{}
	waitErr     error
	stdout      chan []byte
	stderr      chan []byte
	stdoutErr   chan error
	stderrErr   chan error
	stderrLines chan string
	stderrMu    sync.Mutex
	stderrBytes bytes.Buffer
	stdoutDone  chan struct{}
	stderrDone  chan struct{}
}

func TestNativeCommandWindowsFirstCtrlCStopsAndReports(t *testing.T) {
	command, database, logFile, requestKey, listener := startWindowsRepeatingCommand(t, "first-stop")
	defer listener.Close()
	defer command.killIfRunning()

	if _, err := waitWindowsRun(t, database, requestKey, func(run history.RunInfo) bool {
		return run.Status == "running" && run.Rounds >= 1 && run.RoundSeconds != nil
	}); err != nil {
		t.Fatalf("wait for completed loopback round: %v", err)
	}
	if err := sendPrivateConsoleCtrlC(command.cmd.Process.Pid); err != nil {
		t.Fatalf("send Ctrl+C to private command console: %v", err)
	}
	result, err := command.waitFor(15 * time.Second)
	if code := exitCode(err); code != 0 {
		t.Fatalf("graceful command exit = %d; stderr: %s", exitCode(err), result.stderr)
	}
	if !strings.Contains(string(result.stdout), `"summary": [`) {
		t.Fatalf("graceful stop did not write its final JSON report: %s", result.stdout)
	}
	var report struct {
		Summary []struct {
			Rounds int `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(result.stdout, &report); err != nil {
		t.Fatalf("decode final JSON: %v\n%s", err, result.stdout)
	}
	if len(report.Summary) != 1 || report.Summary[0].Rounds < 1 {
		t.Fatalf("final summary = %+v, want completed round", report.Summary)
	}
	run, err := waitWindowsRun(t, database, requestKey, func(run history.RunInfo) bool {
		return run.Status == "stopped"
	})
	if err != nil {
		t.Fatalf("wait for EndRun(stopped): %v", err)
	}
	if run.Rounds < 1 {
		t.Fatalf("stopped history row has %d rounds", run.Rounds)
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("command log was not created: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS graceful-first-ctrl-c=pass report=pass history-stopped=pass")
}

func TestNativeCommandWindowsSecondCtrlCInterruptsFinalization(t *testing.T) {
	command, database, _, requestKey, listener := startWindowsRepeatingCommand(t, "second-stop")
	defer listener.Close()
	defer command.killIfRunning()

	if _, err := waitWindowsRun(t, database, requestKey, func(run history.RunInfo) bool {
		return run.Status == "running" && run.Rounds >= 1 && run.RoundSeconds != nil
	}); err != nil {
		t.Fatalf("wait for completed loopback round: %v", err)
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

	// Each sender is a new, detached test-binary process. The first event asks
	// for graceful finalization; with the write lock held, the second must take
	// the hard-interrupt path before SQLite's 10-second busy timeout.
	firstSignalAt := time.Now()
	if err := sendPrivateConsoleCtrlC(command.cmd.Process.Pid); err != nil {
		t.Fatalf("send first Ctrl+C: %v", err)
	}
	if err := command.waitForStderrLine("stopping; preparing the final summary", 5*time.Second); err != nil {
		t.Fatalf("wait for graceful finalization under the held database lock: %v", err)
	}
	if err := sendPrivateConsoleCtrlC(command.cmd.Process.Pid); err != nil {
		t.Fatalf("send second Ctrl+C: %v", err)
	}
	result, err := command.waitFor(8 * time.Second)
	if code := exitCode(err); code != 130 {
		t.Fatalf("second Ctrl+C exit = %d, want 130; stderr: %s", code, result.stderr)
	}
	if time.Since(firstSignalAt) >= 8*time.Second {
		t.Fatalf("hard interrupt waited for SQLite timeout: elapsed %s", time.Since(firstSignalAt))
	}
	if _, err := waitWindowsRun(t, database, requestKey, func(run history.RunInfo) bool {
		return run.Status == "running" && run.Rounds >= 1
	}); err != nil {
		t.Fatalf("hard interrupt changed the locked, unfinalized history row: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS second-ctrl-c=130 finalization-lock=pass")
}

func TestNativeCommandWindowsPreInputCtrlCDoesNoStartupIO(t *testing.T) {
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "must-not-create.db")
	logFile := filepath.Join(directory, "must-not-create.log")
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create held stdin pipe: %v", err)
	}
	defer stdinRead.Close()
	defer stdinWrite.Close()
	command := startWindowsCommandWithStdin(t, binary, directory, database, logFile, []string{"--format", "json", "--file", "-"}, stdinRead, stdinWrite)
	defer command.killIfRunning()
	// Feed a valid object but retain the write end. PeekNamedPipe reaching zero
	// proves ReadObjects consumed that data and is now waiting for EOF.
	if _, err := io.WriteString(stdinWrite, "127.0.0.1\n"); err != nil {
		t.Fatalf("write held stdin object: %v", err)
	}
	if err := waitWindowsPipeDrained(stdinRead, 5*time.Second); err != nil {
		t.Fatalf("wait until target input was consumed: %v", err)
	}
	if err := sendPrivateConsoleCtrlC(command.cmd.Process.Pid); err != nil {
		t.Fatalf("send startup Ctrl+C: %v", err)
	}
	result, err := command.waitFor(5 * time.Second)
	if code := exitCode(err); code != 130 {
		t.Fatalf("pre-input Ctrl+C exit = %d, want 130; stderr: %s", code, result.stderr)
	}
	for _, path := range []string{database, logFile, filepath.Join(directory, ".cn"), filepath.Join(directory, "cn.log"), filepath.Join(directory, ".cn-ping-history.db")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("pre-input interrupt created %q (stat error %v)", path, err)
		}
	}
	if len(result.stdout) != 0 {
		t.Fatalf("pre-input interrupt wrote a report: %s", result.stdout)
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS pre-input-ctrl-c=130 no-startup-io=pass")
}

func TestNativeCommandWindowsClosedStdoutPipeIsQuiet(t *testing.T) {
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "closed-output.db")
	logFile := filepath.Join(directory, "closed-output.log")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create closed stdout pipe: %v", err)
	}
	defer pipeWrite.Close()
	if err := pipeRead.Close(); err != nil {
		t.Fatalf("close stdout pipe reader before process start: %v", err)
	}
	command := startWindowsCommandWithOutput(t, binary, directory, database, logFile, []string{
		"--format", "json", "--history-file", database, "--log-file", logFile, "--tcp", strconv.Itoa(port), "127.0.0.1",
	}, pipeWrite)
	if err := pipeWrite.Close(); err != nil {
		t.Fatalf("close parent stdout pipe handle: %v", err)
	}
	defer command.killIfRunning()
	result, err := command.waitFor(45 * time.Second)
	if err != nil {
		t.Fatalf("wait for command with closed stdout: %v\n%s", err, result.stderr)
	}
	if code := exitCode(err); code != 0 {
		t.Fatalf("closed stdout changed ordinary exit to %d; stderr: %s", code, result.stderr)
	}
	for _, line := range strings.Split(string(result.stderr), "\n") {
		lower := strings.ToLower(line)
		probeNote := strings.Contains(lower, "probe failure:")
		if strings.Contains(lower, "write report") || strings.Contains(lower, "report write") ||
			((strings.Contains(lower, "pipe") || strings.Contains(lower, "stdout")) && !probeNote) {
			t.Fatalf("closed stdout produced a pipe/report-write diagnostic: %s", result.stderr)
		}
	}
	t.Log("CNMON_NATIVE_COMMAND_WINDOWS closed-stdout-pipe=quiet ordinary-exit=pass")
}

func TestNativeCommandWindowsProbePermissionRole(t *testing.T) {
	expectation := os.Getenv("CNMON_NATIVE_COMMAND_EXPECT_PROBE")
	if expectation != "granted" && expectation != "denied" {
		t.Fatalf("native Windows harness must set CNMON_NATIVE_COMMAND_EXPECT_PROBE to granted or denied")
	}
	identity, err := user.Current()
	if err != nil {
		t.Fatalf("resolve Windows permission-role identity: %v", err)
	}
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "permission-role.db")
	logFile := filepath.Join(directory, "permission-role.log")
	listener, port := nativeLoopbackListener(t)
	defer listener.Close()
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build loopback request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	command := startWindowsCommand(t, binary, directory, database, logFile, []string{
		"--format", "json", "--history-file", database, "--log-file", logFile,
		"--tcp", strconv.Itoa(port), "127.0.0.1",
	})
	defer command.killIfRunning()
	result, err := command.waitFor(45 * time.Second)
	var report struct {
		Latest []map[string]json.RawMessage `json:"latest"`
	}
	if unmarshalErr := json.Unmarshal(result.stdout, &report); unmarshalErr != nil {
		t.Fatalf("decode native permission report: %v (process error %v)\n%s", unmarshalErr, err, result.stdout)
	}
	if len(report.Latest) != 1 {
		t.Fatalf("latest rows = %d, want one loopback host; process error %v\n%s", len(report.Latest), err, result.stdout)
	}
	var icmpState, tcpState string
	if err := json.Unmarshal(report.Latest[0]["result"], &icmpState); err != nil {
		t.Fatalf("decode ICMP state: %v", err)
	}
	if err := json.Unmarshal(report.Latest[0][fmt.Sprintf("tcp_%d", port)], &tcpState); err != nil {
		t.Fatalf("decode TCP state for port %d: %v", port, err)
	}
	if tcpState != "open" {
		t.Fatalf("loopback TCP state = %q, want open", tcpState)
	}
	wantState, wantCode := "OK", 0
	if expectation == "denied" {
		wantState, wantCode = "ERROR", 3
	}
	stateMatches := icmpState == wantState || (expectation == "granted" && strings.HasPrefix(icmpState, "OK ("))
	if !stateMatches || exitCode(err) != wantCode {
		t.Fatalf("native ICMP permission result = %q/exit %d, want %q/exit %d; stderr: %s", icmpState, exitCode(err), wantState, wantCode, result.stderr)
	}
	if expectation == "denied" {
		if diagnosticErr := assertWindowsProbeFailureDiagnostic(string(result.stderr), "127.0.0.1"); diagnosticErr != nil {
			t.Fatalf("denied permission role lacks a bounded host/ICMP cause and Administrator remedy: %v; stderr: %s", diagnosticErr, result.stderr)
		}
	}
	if _, err := waitWindowsRun(t, database, request.Key, func(run history.RunInfo) bool {
		return run.Status == "finished" && run.Rounds == 1
	}); err != nil {
		t.Fatalf("read actual built-command history result: %v", err)
	}
	t.Logf("CNMON_NATIVE_COMMAND_WINDOWS ICMP-permission=%s identity=%s result=%s exit=%d tcp=open history=one-round", expectation, identity.Username, icmpState, wantCode)
}

// TestNativePrivateConsoleCtrlCSender is invoked only as a separate helper
// process. That process detaches from any inherited console before attaching
// to the one private console identified by the test target PID.
func TestNativePrivateConsoleCtrlCSender(t *testing.T) {
	rawPID := os.Getenv("CNMON_PRIVATE_CONSOLE_PID")
	if rawPID == "" {
		return
	}
	parsed, err := strconv.ParseUint(rawPID, 10, 32)
	if err != nil || parsed == 0 {
		t.Fatalf("invalid private console PID %q", rawPID)
	}
	if err := detachWindowsConsole(); err != nil {
		t.Fatalf("detach inherited console: %v", err)
	}
	if err := callWindowsBool(windowsAttachConsole, uintptr(parsed)); err != nil {
		t.Fatalf("attach only to private child console: %v", err)
	}
	defer detachWindowsConsole()
	if err := callWindowsBool(windowsSetConsoleCtrlHandler, 0, 1); err != nil {
		t.Fatalf("ignore Ctrl+C in event sender: %v", err)
	}
	if err := callWindowsBool(windowsGenerateCtrlEvent, windowsCtrlCEvent, 0); err != nil {
		t.Fatalf("generate private-console Ctrl+C: %v", err)
	}
	if err := detachWindowsConsole(); err != nil {
		t.Fatalf("detach from private child console: %v", err)
	}
}

type windowsCommandResult struct {
	stdout []byte
	stderr []byte
}

func startWindowsRepeatingCommand(t *testing.T, name string) (*nativeWindowsCommand, string, string, string, net.Listener) {
	t.Helper()
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, name+".db")
	logFile := filepath.Join(directory, name+".log")
	listener, port := nativeLoopbackListener(t)
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build loopback request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	command := startWindowsCommand(t, binary, directory, database, logFile, []string{
		"--format", "json", "--history-file", database, "--log-file", logFile,
		"--tcp", strconv.Itoa(port), "--every", "10s", "--for", "1m", "127.0.0.1",
	})
	return command, database, logFile, request.Key, listener
}

func startWindowsCommand(t *testing.T, binary, directory, database, logFile string, args []string) *nativeWindowsCommand {
	return startWindowsCommandWithIO(t, binary, directory, database, logFile, args, nil, nil, nil)
}

func startWindowsCommandWithStdin(t *testing.T, binary, directory, database, logFile string, args []string, input io.Reader, heldInput io.WriteCloser) *nativeWindowsCommand {
	return startWindowsCommandWithIO(t, binary, directory, database, logFile, args, input, heldInput, nil)
}

func startWindowsCommandWithOutput(t *testing.T, binary, directory, database, logFile string, args []string, output io.Writer) *nativeWindowsCommand {
	return startWindowsCommandWithIO(t, binary, directory, database, logFile, args, nil, nil, output)
}

func startWindowsCommandWithIO(t *testing.T, binary, directory, database, logFile string, args []string, input io.Reader, heldInput io.WriteCloser, output io.Writer) *nativeWindowsCommand {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = nativeCommandEnv(t, directory, database, logFile)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsCreateNewConsole}
	var err error
	var stdout io.ReadCloser
	if output == nil {
		stdout, err = command.StdoutPipe()
		if err != nil {
			t.Fatalf("create child stdout pipe: %v", err)
		}
	} else {
		command.Stdout = output
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("create child stderr pipe: %v", err)
	}
	stdin := heldInput
	if input == nil {
		stdin, err = command.StdinPipe()
		if err != nil {
			t.Fatalf("create child stdin pipe: %v", err)
		}
	} else {
		command.Stdin = input
	}
	if err := command.Start(); err != nil {
		t.Fatalf("start private-console cn-mon: %v", err)
	}
	result := &nativeWindowsCommand{
		cmd: command, stdin: stdin,
		stdoutIn: stdout, done: make(chan struct{}), stdout: make(chan []byte, 1), stderr: make(chan []byte, 1),
		stdoutErr: make(chan error, 1), stderrErr: make(chan error, 1),
		stderrLines: make(chan string, 128),
		stdoutDone:  make(chan struct{}), stderrDone: make(chan struct{}),
	}
	if stdout == nil {
		result.stdout <- nil
		result.stdoutErr <- nil
		close(result.stdoutDone)
	} else {
		go func() {
			defer close(result.stdoutDone)
			data, err := io.ReadAll(stdout)
			result.stdout <- data
			result.stdoutErr <- err
		}()
	}
	go result.readStderr(stderr)
	go func() {
		<-result.stdoutDone
		<-result.stderrDone
		result.waitErr = command.Wait()
		close(result.done)
	}()
	t.Cleanup(result.killIfRunning)
	return result
}

func (command *nativeWindowsCommand) readStderr(stderr io.Reader) {
	defer close(command.stderrDone)
	reader := bufio.NewReader(stderr)
	for {
		line, err := reader.ReadString('\n')
		if len(line) != 0 {
			command.stderrMu.Lock()
			_, _ = command.stderrBytes.WriteString(line)
			command.stderrMu.Unlock()
			select {
			case command.stderrLines <- strings.TrimRight(line, "\r\n"):
			default:
			}
		}
		if err != nil {
			if err != io.EOF {
				command.stderrErr <- err
			} else {
				command.stderrErr <- nil
			}
			command.stderrMu.Lock()
			data := append([]byte(nil), command.stderrBytes.Bytes()...)
			command.stderrMu.Unlock()
			command.stderr <- data
			return
		}
	}
}

func (command *nativeWindowsCommand) waitForStderrLine(want string, limit time.Duration) error {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case line := <-command.stderrLines:
			if strings.Contains(line, want) {
				return nil
			}
		case <-command.done:
			return fmt.Errorf("child exited before stderr marker %q", want)
		case <-timer.C:
			return fmt.Errorf("stderr marker %q not observed within %s", want, limit)
		}
	}
}

func (command *nativeWindowsCommand) closeStdout() error {
	return command.stdoutIn.Close()
}

func (command *nativeWindowsCommand) waitFor(limit time.Duration) (windowsCommandResult, error) {
	var result windowsCommandResult
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-command.done:
		result.stdout = <-command.stdout
		result.stderr = <-command.stderr
		if readErr := <-command.stdoutErr; readErr != nil && !errors.Is(readErr, os.ErrClosed) {
			return result, fmt.Errorf("read child stdout: %w", readErr)
		}
		if readErr := <-command.stderrErr; readErr != nil && !errors.Is(readErr, os.ErrClosed) {
			return result, fmt.Errorf("read child stderr: %w", readErr)
		}
		return result, command.waitErr
	case <-timer.C:
		command.killIfRunning()
		return result, fmt.Errorf("child did not exit within %s", limit)
	}
}

func (command *nativeWindowsCommand) killIfRunning() {
	if command == nil || command.cmd == nil || command.cmd.Process == nil {
		return
	}
	select {
	case <-command.done:
		return
	default:
	}
	_ = command.stdin.Close()
	_ = command.cmd.Process.Kill()
	select {
	case <-command.done:
	case <-time.After(2 * time.Second):
	}
}

func sendPrivateConsoleCtrlC(pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativePrivateConsoleCtrlCSender$")
	helper.Env = append(os.Environ(), "CNMON_PRIVATE_CONSOLE_PID="+strconv.Itoa(pid))
	helper.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsDetachedProcess}
	output, err := helper.CombinedOutput()
	if err != nil {
		return fmt.Errorf("private console sender: %w: %s", err, boundedNativeOutput(output))
	}
	return nil
}

func waitWindowsPipeDrained(pipe *os.File, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		var available uint32
		ok, _, callErr := windowsPeekNamedPipe.Call(pipe.Fd(), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
		if ok == 0 {
			if callErr != syscall.Errno(0) {
				return fmt.Errorf("PeekNamedPipe: %w", callErr)
			}
			return errors.New("PeekNamedPipe failed")
		}
		if available == 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("stdin still has unread bytes after %s", limit)
}

func detachWindowsConsole() error {
	result, _, callErr := windowsFreeConsole.Call()
	if result != 0 {
		return nil
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == windowsInvalidHandle {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return errors.New("FreeConsole failed without a Windows error code")
}

func callWindowsBool(proc *syscall.LazyProc, args ...uintptr) error {
	result, _, callErr := proc.Call(args...)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return fmt.Errorf("%s failed", proc.Name)
}

func nativeLoopbackListener(t *testing.T) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on local TCP loopback: %v", err)
	}
	return listener, listener.Addr().(*net.TCPAddr).Port
}

// Keep the lock connection's URI behavior aligned with history's Windows
// SQLite path handling: an opaque file URI preserves the drive prefix while
// escaping path data (notably spaces) before ncruces/go-sqlite3 opens it.
func windowsSQLiteURI(filename string) string {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return ""
	}
	location := strings.ReplaceAll(absolute, `\`, "/")
	query := url.Values{}
	query.Set("mode", "rwc")
	query.Add("_pragma", "busy_timeout(10000)")
	query.Add("_pragma", "foreign_keys(ON)")
	return (&url.URL{
		Scheme:   "file",
		Opaque:   (&url.URL{Path: location}).EscapedPath(),
		RawQuery: query.Encode(),
	}).String()
}

func waitWindowsRun(t *testing.T, filename, key string, ready func(history.RunInfo) bool) (history.RunInfo, error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last history.RunInfo
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		store, err := history.OpenReader(ctx, filename)
		if err == nil && store != nil {
			err = store.View(ctx, func(snapshot *history.Snapshot) error {
				info, err := snapshot.Request(key)
				if err != nil || info == nil {
					return err
				}
				page, err := snapshot.Runs(info.ID, false, nil, 10)
				if err != nil || len(page.Items) == 0 {
					return err
				}
				last = page.Items[0]
				return nil
			})
			closeErr := store.Close()
			if err == nil {
				err = closeErr
			}
		}
		cancel()
		if err == nil && ready(last) {
			return last, nil
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-time.After(time.Until(deadline)):
		}
	}
	return last, fmt.Errorf("requested history condition not reached; latest run=%+v", last)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func assertWindowsProbeFailureDiagnostic(stderr, host string) error {
	var candidates []string
	for _, line := range strings.Split(stderr, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(line, host) || !strings.Contains(lower, "icmp") ||
			!strings.Contains(lower, "authorized") || !strings.Contains(lower, "elevated") || !strings.Contains(lower, "administrator") {
			continue
		}
		candidates = append(candidates, line)
	}
	if len(candidates) != 1 {
		return fmt.Errorf("found %d matching probe-failure diagnostics, want exactly one", len(candidates))
	}
	line := candidates[0]
	lower := strings.ToLower(line)
	message := strings.TrimPrefix(line, "cn-mon: ")
	if len(message) > 4096 {
		return fmt.Errorf("diagnostic is %d bytes, limit is 4096", len(message))
	}
	if !utf8.ValidString(message) {
		return errors.New("diagnostic is not valid UTF-8")
	}
	if strings.IndexFunc(line, unicode.IsControl) >= 0 {
		return errors.New("diagnostic contains an unescaped control character")
	}
	icmpAt := strings.Index(lower, "icmp") + len("icmp")
	remedyAt := strings.Index(lower, "authorized")
	if remedyAt <= icmpAt {
		return errors.New("diagnostic has no cause between ICMP and the permission remedy")
	}
	cause := strings.Trim(lower[icmpAt:remedyAt], " \t:;,.-\"'")
	if cause == "" || strings.Contains(cause, "error detail unavailable") || cause == "error" {
		return errors.New("diagnostic does not include the actual ICMP error cause")
	}
	return nil
}
