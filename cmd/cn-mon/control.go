package main

import (
	"context"
	"sync"
)

type commandControl struct {
	mu         sync.Mutex
	stop       chan struct{}
	cancel     context.CancelFunc
	exit       func(int)
	phase      commandPhase
	stopCause  string
	interrupts int
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
	if c.phase == commandStarting {
		c.phase = commandRunning
	}
	c.mu.Unlock()
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

func (c *commandControl) RequestStop(reason string) {
	c.mu.Lock()
	if c.phase != commandRunning {
		c.mu.Unlock()
		return
	}
	shouldClose := c.stopCause == ""
	if shouldClose {
		c.stopCause = reason
	}
	c.mu.Unlock()
	if shouldClose {
		close(c.stop)
	}
}

func (c *commandControl) Interrupt() {
	var cancel context.CancelFunc
	var exit func(int)
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
	case commandRunning:
		if c.interrupts == 1 {
			if c.stopCause == "" {
				c.stopCause = "interrupt"
				close(c.stop)
			}
		} else {
			c.phase = commandFinished
			cancel, exit = c.cancel, c.exit
		}
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if exit != nil {
		exit(130)
	}
}
