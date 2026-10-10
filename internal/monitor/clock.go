// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"time"
)

type realClock struct {
	started time.Time
}

func NewClock() Clock { return &realClock{started: time.Now()} }

func (c *realClock) Now() ClockReading {
	wall := time.Now()
	return ClockReading{Wall: wall.UTC(), Mono: wall.Sub(c.started)}
}

func (c *realClock) After(delay time.Duration) <-chan time.Time {
	return time.After(delay)
}

type runClock struct {
	source Clock
	ready  bool
	run    time.Time
	mono   time.Duration
	offset time.Duration
}

func newRunClock(source Clock) *runClock { return &runClock{source: source} }

func (c *runClock) Now() runReading {
	reading := c.source.Now()
	wall := reading.Wall.UTC()
	if !c.ready {
		c.ready = true
		c.run, c.mono = wall, reading.Mono
		return runReading{Run: c.run, Wall: wall, Mono: reading.Mono}
	}
	delta := reading.Mono - c.mono
	if delta < 0 {
		delta = 0
	}
	minimum := c.run.Add(delta)
	candidate := wall.Add(c.offset)
	if candidate.Before(minimum) {
		c.offset += minimum.Sub(candidate)
		candidate = minimum
	}
	c.run, c.mono = candidate, reading.Mono
	return runReading{Run: candidate, Wall: wall, Mono: reading.Mono}
}
