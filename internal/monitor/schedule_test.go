// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"math"
	"testing"
	"time"
)

func TestRunScheduleAlignsExactAndOverrunSlots(t *testing.T) {
	origin := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	interval := 10 * time.Second
	schedule, err := newRunSchedule(origin, &interval, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		now         time.Time
		wantIndex   int64
		wantAt      time.Time
		wantSkipped int64
	}{
		{"exact slot", origin.Add(10 * time.Second), 1, origin.Add(10 * time.Second), 0},
		{"fraction after slot", origin.Add(10*time.Second + time.Nanosecond), 2, origin.Add(20 * time.Second), 1},
		{"multiple overrun slots", origin.Add(35 * time.Second), 4, origin.Add(40 * time.Second), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, at, skipped, done, err := schedule.next(0, tc.now)
			if err != nil || done || index != tc.wantIndex || !at.Equal(tc.wantAt) || skipped != tc.wantSkipped {
				t.Fatalf("next = (%d, %s, %d, %v, %v), want (%d, %s, %d, false, nil)", index, at, skipped, done, err, tc.wantIndex, tc.wantAt, tc.wantSkipped)
			}
		})
	}
}

func TestRunScheduleDurationBoundExcludesEndSlot(t *testing.T) {
	origin := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	interval, duration := 10*time.Second, 35*time.Second
	schedule, err := newRunSchedule(origin, &interval, &duration)
	if err != nil {
		t.Fatal(err)
	}
	index, at, done, err := schedule.first(origin.Add(time.Second))
	if err != nil || done || index != 0 || !at.Equal(origin) {
		t.Fatalf("first = (%d, %s, %v, %v)", index, at, done, err)
	}
	index, at, skipped, done, err := schedule.next(0, origin.Add(35*time.Second))
	if err != nil || !done || index != 0 || !at.IsZero() || skipped != 3 {
		t.Fatalf("past end = (%d, %s, %d, %v, %v), want done after 3 skipped slots", index, at, skipped, done, err)
	}
	index, at, skipped, done, err = schedule.next(0, origin.Add(25*time.Second))
	if err != nil || done || index != 3 || !at.Equal(origin.Add(30*time.Second)) || skipped != 2 {
		t.Fatalf("before end = (%d, %s, %d, %v, %v), want slot 3 after 2 skipped", index, at, skipped, done, err)
	}
}

func TestRunScheduleOneShotAndPastEnd(t *testing.T) {
	origin := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	duration := 10 * time.Second
	schedule, err := newRunSchedule(origin, nil, &duration)
	if err != nil {
		t.Fatal(err)
	}
	index, at, done, err := schedule.first(origin.Add(9 * time.Second))
	if err != nil || done || index != 0 || !at.Equal(origin) {
		t.Fatalf("first = (%d, %s, %v, %v), want origin slot", index, at, done, err)
	}
	_, _, done, err = schedule.first(origin.Add(10 * time.Second))
	if err != nil || !done {
		t.Fatalf("first at duration end = done %v, err %v", done, err)
	}
	_, _, skipped, done, err := schedule.next(0, origin)
	if err != nil || !done || skipped != 0 {
		t.Fatalf("one-shot next = done %v skipped %d err %v", done, skipped, err)
	}
}

func TestRunScheduleRejectsInvalidAndOverflowingSlots(t *testing.T) {
	origin := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	zero := time.Duration(0)
	if _, err := newRunSchedule(origin, &zero, nil); err == nil {
		t.Fatal("zero interval accepted")
	}
	if _, err := newRunSchedule(origin, nil, &zero); err == nil {
		t.Fatal("zero duration accepted")
	}
	interval := 2 * time.Nanosecond
	schedule, err := newRunSchedule(origin, &interval, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schedule.at(math.MaxInt64); err == nil {
		t.Fatal("overflowing slot index accepted")
	}
}
