package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestCommandControlStartupInterruptExitsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exits := make(chan int, 1)
	control := newCommandControl(cancel, func(code int) { exits <- code })

	control.Interrupt()
	select {
	case code := <-exits:
		if code != 130 {
			t.Fatalf("startup interrupt exit code = %d, want 130", code)
		}
	case <-time.After(time.Second):
		t.Fatal("startup interrupt did not exit promptly")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("startup interrupt did not cancel the hard context")
	}
	select {
	case <-control.Stop():
		t.Fatal("startup interrupt must not be turned into a graceful run stop")
	default:
	}
}

func TestCommandControlStartupInterruptRestoresAfterHardCancelBeforeExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var control *commandControl
	order := make(chan string, 2)
	control = newCommandControl(cancel, func(code int) {
		if code != 130 {
			t.Errorf("startup hard exit code=%d, want 130", code)
		}
		if control.InterruptCount() != 1 {
			t.Errorf("exit callback observed interrupt count=%d, want 1", control.InterruptCount())
		}
		order <- "exit"
	})
	control.SetEmergencyRestore(func() {
		select {
		case <-ctx.Done():
		default:
			t.Error("emergency restoration ran before hard cancellation")
		}
		if control.InterruptCount() != 1 {
			t.Errorf("restore callback observed interrupt count=%d, want 1", control.InterruptCount())
		}
		order <- "restore"
	})

	control.Interrupt()
	if got := <-order; got != "restore" {
		t.Fatalf("callback order first=%q, want restore", got)
	}
	if got := <-order; got != "exit" {
		t.Fatalf("callback order second=%q, want exit", got)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("startup interrupt did not cancel the hard context")
	}
	if got := control.InterruptCount(); got != 1 {
		t.Fatalf("startup interrupt count=%d, want 1", got)
	}
}

func TestCommandControlFirstInterruptStopsAndSecondHardExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exits := make(chan int, 2)
	control := newCommandControl(cancel, func(code int) { exits <- code })
	control.EnterRunning()

	control.Interrupt()
	select {
	case <-control.Stop():
	default:
		t.Fatal("first running interrupt did not request a graceful stop")
	}
	if control.StopReason() != "interrupt" {
		t.Fatalf("first stop reason = %q, want interrupt", control.StopReason())
	}
	select {
	case code := <-exits:
		t.Fatalf("first interrupt exited with %d", code)
	default:
	}
	select {
	case <-ctx.Done():
		t.Fatal("first interrupt canceled hard context")
	default:
	}

	control.Interrupt()
	select {
	case code := <-exits:
		if code != 130 {
			t.Fatalf("second interrupt exit code = %d, want 130", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second interrupt did not exit promptly")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("second interrupt did not cancel the hard context")
	}
	if got := control.InterruptCount(); got != 2 {
		t.Fatalf("interrupt count = %d, want 2", got)
	}
}

func TestCommandControlStopRequestsPreserveFirstReasonAndAreIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	control := newCommandControl(cancel, func(int) { t.Error("RequestStop must not exit") })
	control.EnterRunning()

	control.RequestStop("term")
	stop := control.Stop()
	control.RequestStop("hangup")
	control.RequestStop("interrupt-like")
	select {
	case <-stop:
	default:
		t.Fatal("stop channel is not closed")
	}
	if got := control.StopReason(); got != "term" {
		t.Fatalf("stop reason = %q, want first reason term", got)
	}
	if got := control.InterruptCount(); got != 0 {
		t.Fatalf("non-interrupt stop requests incremented interrupt count: %d", got)
	}
	select {
	case <-ctx.Done():
		t.Fatal("graceful stop canceled hard context")
	default:
	}
}

func TestCommandControlDefersQuitDuringInitialReadUntilFollowingTransition(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	control := newCommandControl(cancel, func(int) { t.Error("quit must not hard-exit") })
	// The screen key worker is active while the initial history View is held.
	control.RequestStop("quit")
	select {
	case <-control.Stop():
		t.Fatal("early q must not interrupt the initial history View")
	default:
	}
	if got := control.StopReason(); got != "quit" {
		t.Fatalf("queued stop reason=%q, want quit", got)
	}
	control.EnterRunning() // bound to EnterFollowing after the initial View
	select {
	case <-control.Stop():
	default:
		t.Fatal("q typed during the initial View was lost at EnterFollowing")
	}
	if got := control.InterruptCount(); got != 0 {
		t.Fatalf("q incremented Ctrl+C count to %d", got)
	}
}

func TestCommandControlIgnoresSignalsAfterFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exits := 0
	var mu sync.Mutex
	control := newCommandControl(cancel, func(int) {
		mu.Lock()
		exits++
		mu.Unlock()
	})
	control.EnterRunning()
	control.Finish()
	control.Interrupt()
	control.RequestStop("term")
	if got := control.InterruptCount(); got != 0 {
		t.Fatalf("finished control counted interrupt: %d", got)
	}
	if got := control.StopReason(); got != "" {
		t.Fatalf("finished control accepted stop reason %q", got)
	}
	select {
	case <-ctx.Done():
		t.Fatal("finished control canceled hard context")
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	if exits != 0 {
		t.Fatalf("finished control exited %d times", exits)
	}
}

func TestCommandControlCallbacksRunWithoutHoldingStateLock(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	var control *commandControl
	control = newCommandControl(cancel, func(code int) {
		if code != 130 || control.InterruptCount() != 2 {
			t.Errorf("exit callback observed code=%d interrupts=%d", code, control.InterruptCount())
		}
	})
	control.EnterRunning()
	completed := make(chan struct{})
	go func() {
		control.Interrupt()
		control.Interrupt()
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("control callback deadlocked while querying state")
	}
}

func TestSecondInterruptRunsEmergencyRestoreBeforePromptExit(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	exits := make(chan int, 1)
	var control *commandControl
	order := make(chan string, 2)
	control = newCommandControl(cancel, func(code int) {
		if control.InterruptCount() != 2 {
			t.Errorf("exit callback observed interrupt count %d", control.InterruptCount())
		}
		order <- "exit"
		exits <- code
	})
	control.SetEmergencyRestore(func() {
		if control.InterruptCount() != 2 {
			t.Errorf("restore callback observed interrupt count %d", control.InterruptCount())
		}
		order <- "restore"
	})
	control.EnterRunning()
	control.Interrupt()
	control.Interrupt()

	if got := <-order; got != "restore" {
		t.Fatalf("callback order first=%q, want restore", got)
	}
	if got := <-order; got != "exit" {
		t.Fatalf("callback order second=%q, want exit", got)
	}
	if code := <-exits; code != 130 {
		t.Fatalf("hard exit code=%d, want 130", code)
	}
}

func TestAddedEmergencyRestoreComposesCallbacks(t *testing.T) {
	control := newCommandControl(func() {}, func(int) {})
	order := make(chan string, 2)
	control.SetEmergencyRestore(func() { order <- "screen" })
	control.AddEmergencyRestore(func() { order <- "report" })
	control.EnterRunning()
	control.Interrupt()
	control.Interrupt()
	if got := <-order; got != "screen" {
		t.Fatalf("first restore=%q, want screen", got)
	}
	if got := <-order; got != "report" {
		t.Fatalf("second restore=%q, want report", got)
	}
}
