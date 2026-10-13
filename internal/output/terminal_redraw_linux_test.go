// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package output

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/target"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestScreenDrawSkipsIdenticalAndPatchesChangedFrameOnPTY(t *testing.T) {
	screen, master, slave := newRedrawPTY(t, 80, 24)
	defer master.Close()
	defer slave.Close()
	screen.platform = &terminalPlatform{outFD: int(slave.Fd()), outputTTY: true}

	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request: rejected=%v err=%v", rejected, err)
	}
	update := monitor.LiveUpdate{Request: request, Phase: monitor.LiveWaiting, Now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), Data: monitor.LiveData{Cells: []byte{monitor.Unprobed}}}
	if err := screen.drawLocked(update); err != nil {
		t.Fatal(err)
	}
	if output := drainRedrawPTY(t, master); len(output) == 0 {
		t.Fatal("first frame produced no terminal output")
	}
	if err := screen.drawLocked(update); err != nil {
		t.Fatal(err)
	}
	if output := drainRedrawPTY(t, master); len(output) != 0 {
		t.Fatalf("identical frame wrote %q", output)
	}

	update.Now = update.Now.Add(time.Second)
	if err := screen.drawLocked(update); err != nil {
		t.Fatal(err)
	}
	output := drainRedrawPTY(t, master)
	if len(output) == 0 || containsBytes(output, []byte("\x1b[1M")) || containsBytes(output, []byte("\x1b[5M")) {
		t.Fatalf("changed header did not use a row patch: %q", output)
	}
	if !containsBytes(output, []byte("\x1b[K")) {
		t.Fatalf("changed row did not clear its old suffix: %q", output)
	}
}

func TestScreenDrawForcesFullFrameAfterGeometryViewAndLayoutChanges(t *testing.T) {
	screen, master, slave := newRedrawPTY(t, 80, 24)
	defer master.Close()
	defer slave.Close()
	screen.platform = &terminalPlatform{outFD: int(slave.Fd()), outputTTY: true}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request: rejected=%v err=%v", rejected, err)
	}
	update := monitor.LiveUpdate{Request: request, Phase: monitor.LiveWaiting, Now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), Data: monitor.LiveData{Cells: []byte{monitor.Unprobed}}}
	full := func(label string) {
		t.Helper()
		if err := screen.drawLocked(update); err != nil {
			t.Fatalf("%s draw: %v", label, err)
		}
		if output := drainRedrawPTY(t, master); !hasLineDelete(output) {
			t.Fatalf("%s did not force full frame cleanup: %q", label, output)
		}
	}
	if err := screen.drawLocked(update); err != nil {
		t.Fatal(err)
	}
	_ = drainRedrawPTY(t, master)

	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 20, Col: 70}); err != nil {
		t.Fatal(err)
	}
	full("resize")

	screen.table.Store(true)
	full("view toggle")

	other, rejected, err := target.Build([]string{"192.0.2.2"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build changed request: rejected=%v err=%v", rejected, err)
	}
	update.Request = other
	update.Data.Cells = []byte{monitor.Unprobed}
	full("layout change")
}

func TestLinuxForegroundScreenLifecycleForcesRepaint(t *testing.T) {
	if action := os.Getenv("CN_MONITOR_OUTPUT_FOREGROUND_ACTION"); action != "" {
		runLinuxForegroundScreenAction(t, action)
		return
	}
	for _, action := range []string{"loss", "change", "note", "failure", "resume", "resume-background"} {
		t.Run(action, func(t *testing.T) {
			_, master, slave := newRedrawPTY(t, 80, 24)
			defer master.Close()
			command := exec.Command(os.Args[0], "-test.run=^TestLinuxForegroundScreenLifecycleForcesRepaint$")
			command.Env = append(os.Environ(), "CN_MONITOR_OUTPUT_FOREGROUND_ACTION="+action)
			command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := command.Run(); err != nil {
				t.Fatalf("foreground lifecycle child: %v", err)
			}
			_ = slave.Close()
			output := drainRedrawPTY(t, master)
			marker := []byte("REDRAW-" + action)
			markerIndex := bytes.Index(output, marker)
			if markerIndex < 0 {
				t.Fatalf("child did not emit redraw marker: %q", output)
			}
			afterMarker := output[markerIndex+len(marker):]
			if action != "failure" && !bytes.Contains(afterMarker, []byte("Ping Monitor")) {
				t.Fatalf("identical frame was not repainted after %s: %q", action, output[markerIndex:])
			}
			if action == "failure" && bytes.Contains(afterMarker, []byte("Ping Monitor")) {
				t.Fatalf("failed Screen.Draw unexpectedly rendered a frame: %q", afterMarker)
			}
			if action == "note" {
				if !bytes.Contains(output, []byte("background diagnostic")) {
					t.Fatalf("permanent Note was not retained after ownership loss: %q", output)
				}
				if hasLineDelete(afterMarker) {
					t.Fatalf("reacquisition deleted relative to a stale frame anchor: %q", afterMarker)
				}
			}
			if strings.HasPrefix(action, "resume") && hasLineDelete(afterMarker) {
				t.Fatalf("SIGCONT reacquisition used stale frame rows: %q", afterMarker)
			}
		})
	}
}

func runLinuxForegroundScreenAction(t *testing.T, action string) {
	t.Helper()
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		t.Fatal("foreground lifecycle child has no controlling PTY")
	}
	signal.Ignore(syscall.SIGTTOU)
	screen := OpenScreen(os.Stdin, os.Stderr, "default", true, KeyHandlers{})
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		screen.Close()
		t.Fatalf("build request: rejected=%v err=%v", rejected, err)
	}
	update := monitor.LiveUpdate{Request: request, Phase: monitor.LiveWaiting, Now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), Data: monitor.LiveData{Cells: []byte{monitor.Unprobed}}}
	screen.Draw(update)

	background := exec.Command("sleep", "10")
	background.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := background.Start(); err != nil {
		screen.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = background.Process.Kill()
		_ = background.Wait()
	}()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, background.Process.Pid); err != nil {
		screen.Close()
		t.Fatalf("move PTY foreground to another process group: %v", err)
	}

	switch action {
	case "loss":
		if screen.Enabled() {
			screen.Close()
			t.Fatal("screen remained enabled after foreground loss")
		}
	case "change":
		before := screen.frameDirty.Load()
		screen.Change(monitor.Change{Host: "192.0.2.1", Seen: true, Observation: monitor.EchoReply})
		if screen.frameDirty.Load() == before {
			screen.Close()
			t.Fatal("ineligible Change did not invalidate the frame")
		}
	case "note":
		before := screen.frameDirty.Load()
		screen.Note("background diagnostic")
		if screen.frameDirty.Load() == before {
			screen.Close()
			t.Fatal("ineligible permanent Note did not invalidate the frame")
		}
	case "failure":
		if err := os.Stderr.Close(); err != nil {
			screen.Close()
			t.Fatal(err)
		}
		screen.Draw(update)
		if !screen.failed.Load() || screen.frame != nil || screen.frameRows != 0 {
			screen.Close()
			t.Fatalf("failed Screen.Draw retained frame ownership: failed=%v frame=%+v rows=%d", screen.failed.Load(), screen.frame, screen.frameRows)
		}
	case "resume":
		// The PTY foreground is restored below before SIGCONT is delivered.
	case "resume-background":
		deliverResumeAndWait(t, screen, true)
	default:
		screen.Close()
		t.Fatalf("unknown action %q", action)
	}

	if action != "failure" {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, unix.Getpgrp()); err != nil {
			screen.Close()
			t.Fatalf("restore PTY foreground process group: %v", err)
		}
	}
	if action == "resume" {
		deliverResumeAndWait(t, screen, false)
	}
	if _, err := io.WriteString(os.Stdout, "REDRAW-"+action+"\n"); err != nil {
		screen.Close()
		t.Fatal(err)
	}
	screen.Draw(update)
	screen.Close()
}

func deliverResumeAndWait(t *testing.T, screen *Screen, retainHiddenCursor bool) {
	t.Helper()
	before := screen.frameDirty.Load()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGCONT); err != nil {
		screen.Close()
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for screen.frameDirty.Load() == before || (!retainHiddenCursor && screen.activeFlag.Load()) {
		if time.Now().After(deadline) {
			screen.Close()
			t.Fatal("SIGCONT did not invalidate frame ownership and release hidden cursor state")
		}
		time.Sleep(time.Millisecond)
	}
	if retainHiddenCursor && !screen.activeFlag.Load() {
		t.Fatal("background SIGCONT discarded hidden-cursor state before restoration was permitted")
	}
}

func newRedrawPTY(t *testing.T, width, height uint16) (*Screen, *os.File, *os.File) {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(masterFD)
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(masterFD)
		t.Fatal(err)
	}
	slaveFD, err := unix.Open("/dev/pts/"+strconv.Itoa(number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(masterFD)
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(masterFD, unix.TIOCSWINSZ, &unix.Winsize{Row: height, Col: width}); err != nil {
		_ = unix.Close(masterFD)
		_ = unix.Close(slaveFD)
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(masterFD), "redraw-pty-master")
	slave := os.NewFile(uintptr(slaveFD), "redraw-pty-slave")
	if err := unix.SetNonblock(masterFD, true); err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatal(err)
	}
	return &Screen{out: slave, theme: "default", noColor: true}, master, slave
}

func drainRedrawPTY(t *testing.T, master *os.File) []byte {
	t.Helper()
	var result []byte
	buffer := make([]byte, 4096)
	for {
		n, err := unix.Read(int(master.Fd()), buffer)
		if n > 0 {
			result = append(result, buffer[:n]...)
		}
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EIO) {
				return result
			}
			t.Fatal(err)
		}
		if n == 0 {
			return result
		}
	}
}

func containsBytes(haystack, needle []byte) bool {
	return bytes.Contains(haystack, needle)
}

func hasLineDelete(output []byte) bool {
	for index, value := range output {
		if value != 'M' || index == 0 || output[index-1] < '0' || output[index-1] > '9' {
			continue
		}
		cursor := index - 1
		for cursor >= 0 && output[cursor] >= '0' && output[cursor] <= '9' {
			cursor--
		}
		if cursor > 0 && output[cursor] == '[' && output[cursor-1] == '\x1b' {
			return true
		}
	}
	return false
}
