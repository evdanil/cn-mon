// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"testing"
	"time"
)

type sequenceClock struct {
	readings []ClockReading
	calls    int
}

func (c *sequenceClock) Now() ClockReading {
	if c.calls >= len(c.readings) {
		panic("unexpected clock reading")
	}
	reading := c.readings[c.calls]
	c.calls++
	return reading
}

func (*sequenceClock) After(time.Duration) <-chan time.Time { panic("unexpected timer") }

func TestRunClockUsesOneReadingAndCorrectsRollback(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	source := &sequenceClock{readings: []ClockReading{
		{Wall: t0, Mono: 0},
		{Wall: t0.Add(-5 * time.Second), Mono: 2 * time.Second},
		{Wall: t0.Add(-4 * time.Second), Mono: 3 * time.Second},
		{Wall: t0.Add(10 * time.Second), Mono: 4 * time.Second},
		{Wall: t0.Add(9 * time.Second), Mono: 5 * time.Second},
	}}
	clock := newRunClock(source)
	if source.calls != 0 {
		t.Fatalf("constructor consumed %d readings", source.calls)
	}
	want := []time.Time{t0, t0.Add(2 * time.Second), t0.Add(3 * time.Second), t0.Add(17 * time.Second), t0.Add(18 * time.Second)}
	for i, expected := range want {
		got := clock.Now()
		if !got.Run.Equal(expected) {
			t.Errorf("reading %d corrected time = %s, want %s", i, got.Run, expected)
		}
		if !got.Wall.Equal(source.readings[i].Wall.UTC()) {
			t.Errorf("reading %d raw wall = %s, want %s", i, got.Wall, source.readings[i].Wall.UTC())
		}
		if got.Mono != source.readings[i].Mono {
			t.Errorf("reading %d monotonic = %s, want %s", i, got.Mono, source.readings[i].Mono)
		}
	}
	if source.calls != len(want) {
		t.Fatalf("Now consumed %d readings, want %d", source.calls, len(want))
	}
}

func TestNewClockReturnsUTCWallAndMonotonicElapsed(t *testing.T) {
	clock := NewClock()
	first := clock.Now()
	second := clock.Now()
	if first.Wall.Location() != time.UTC || second.Wall.Location() != time.UTC {
		t.Fatalf("wall locations = %v, %v; want UTC", first.Wall.Location(), second.Wall.Location())
	}
	if first.Mono < 0 || second.Mono < first.Mono {
		t.Fatalf("monotonic readings = %s then %s", first.Mono, second.Mono)
	}
}
