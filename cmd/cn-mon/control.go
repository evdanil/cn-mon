package main

import (
	"context"
	"sync"
)

type commandControl struct {
	mu               sync.Mutex
	stop             chan struct{}
	cancel           context.CancelFunc
	exit             func(int)
	emergencyRestore func()
	phase            commandPhase
	stopCause        string
	interrupts       int
}

type commandPhase uint8

const (
	commandStarting commandPhase = iota
	commandRunning
	commandFinished
)

func newCommandControl(cancel context.CancelFunc, exit func(int)) *commandControl {
	return &commandControl{stop: make(chan struct{}), cancel: cancel, exit: exit, phase: commandStarting}
}

func (c *commandControl) Stop() <-chan struct{} { return c.stop }

func (c *commandControl) EnterRunning() {
	c.mu.Lock()
	shouldClose := false
	if c.phase == commandStarting {
		c.phase = commandRunning
		shouldClose = c.stopCause != ""
	}
	c.mu.Unlock()
	if shouldClose {
		close(c.stop)
	}
}

func (c *commandControl) Finish() {
	c.mu.Lock()
	c.phase = commandFinished
	c.mu.Unlock()
}

func (c *commandControl) StopReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopCause
}

func (c *commandControl) InterruptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interrupts
}

// SetEmergencyRestore installs bounded terminal restoration for the hard-exit
// path. It may safely be called after command construction while the command is
// starting; interrupt callbacks never retain the control lock while invoking it.
func (c *commandControl) SetEmergencyRestore(restore func()) {
	c.mu.Lock()
	c.emergencyRestore = restore
	c.mu.Unlock()
}

func (c *commandControl) AddEmergencyRestore(restore func()) {
	if restore == nil {
		return
	}
	c.mu.Lock()
	previous := c.emergencyRestore
	if previous == nil {
		c.emergencyRestore = restore
	} else {
		c.emergencyRestore = func() {
			previous()
			restore()
		}
	}
	c.mu.Unlock()
}

func (c *commandControl) RequestStop(reason string) {
	c.mu.Lock()
	if c.phase == commandFinished {
		c.mu.Unlock()
		return
	}
	shouldClose := c.stopCause == ""
	if shouldClose {
		c.stopCause = reason
	}
	shouldClose = shouldClose && c.phase == commandRunning
	c.mu.Unlock()
	if shouldClose {
		close(c.stop)
	}
}

func (c *commandControl) Interrupt() {
	var cancel context.CancelFunc
	var exit func(int)
	var restore func()
	c.mu.Lock()
	if c.phase == commandFinished {
		c.mu.Unlock()
		return
	}
	c.interrupts++
	switch c.phase {
	case commandStarting:
		c.phase = commandFinished
		cancel, exit = c.cancel, c.exit
		restore = c.emergencyRestore
	case commandRunning:
		if c.interrupts == 1 {
			if c.stopCause == "" {
				c.stopCause = "interrupt"
				close(c.stop)
			}
		} else {
			c.phase = commandFinished
			cancel, exit = c.cancel, c.exit
			restore = c.emergencyRestore
		}
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if restore != nil {
		restore()
	}
	if exit != nil {
		exit(130)
	}
}
