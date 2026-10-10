// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"errors"
	"math"
	"time"
)

type runSchedule struct {
	origin   time.Time
	interval time.Duration // zero denotes a one-round run
	until    *time.Time
}

func newRunSchedule(origin time.Time, interval, duration *time.Duration) (*runSchedule, error) {
	schedule := &runSchedule{origin: origin}
	if interval != nil {
		if *interval <= 0 {
			return nil, errors.New("run interval must be positive")
		}
		schedule.interval = *interval
	}
	if duration != nil {
		if *duration <= 0 {
			return nil, errors.New("run duration must be positive")
		}
		until := origin.Add(*duration)
		if until.Sub(origin) != *duration {
			return nil, errors.New("run end time overflows time.Duration range")
		}
		schedule.until = &until
	}
	return schedule, nil
}

func (s *runSchedule) first(now time.Time) (index int64, at time.Time, done bool, err error) {
	if s.until != nil && !now.Before(*s.until) {
		return 0, time.Time{}, true, nil
	}
	return 0, s.origin, false, nil
}

// next chooses the first aligned slot no earlier than now and strictly after
// previousIndex. It counts skipped slots only when they remain before until.
func (s *runSchedule) next(previousIndex int64, now time.Time) (index int64, at time.Time, skipped int64, done bool, err error) {
	if s.interval == 0 {
		return 0, time.Time{}, 0, true, nil
	}
	if previousIndex < 0 || previousIndex == math.MaxInt64 {
		return 0, time.Time{}, 0, false, errors.New("run slot index overflows")
	}
	minimumIndex := previousIndex + 1
	selectedIndex := minimumIndex
	elapsed := now.Sub(s.origin)
	if elapsed > 0 {
		interval := int64(s.interval)
		elapsedNanos := int64(elapsed)
		aligned := (elapsedNanos-1)/interval + 1
		if aligned > selectedIndex {
			selectedIndex = aligned
		}
	}
	lastBeforeEnd := int64(math.MaxInt64)
	if s.until != nil {
		untilDelta := s.until.Sub(s.origin)
		if untilDelta <= 0 {
			return 0, time.Time{}, 0, true, nil
		}
		lastBeforeEnd = (int64(untilDelta) - 1) / int64(s.interval)
	}
	if selectedIndex > lastBeforeEnd {
		lastSkipped := lastBeforeEnd
		if lastSkipped >= minimumIndex {
			skipped = lastSkipped - previousIndex
		}
		return 0, time.Time{}, skipped, true, nil
	}
	if selectedIndex > minimumIndex {
		skipped = selectedIndex - minimumIndex
	}
	at, err = s.at(selectedIndex)
	if err != nil {
		return 0, time.Time{}, 0, false, err
	}
	return selectedIndex, at, skipped, false, nil
}

func (s *runSchedule) at(index int64) (time.Time, error) {
	if index < 0 || s.interval <= 0 || index > math.MaxInt64/int64(s.interval) {
		return time.Time{}, errors.New("run slot time overflows")
	}
	delta := time.Duration(index * int64(s.interval))
	at := s.origin.Add(delta)
	if at.Sub(s.origin) != delta {
		return time.Time{}, errors.New("run slot time overflows time.Duration range")
	}
	return at, nil
}
