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
