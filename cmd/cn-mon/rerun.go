// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

type rerunSelection struct {
	request  target.Request
	interval *time.Duration
	duration *time.Duration
	source   *string
}

type rerunSelectionFailure struct {
	message string
}

func (e *rerunSelectionFailure) Error() string { return e.message }

func selectRerun(ctx context.Context, path, prefix string, every, duration *time.Duration, deps monitor.ReadDeps) (rerunSelection, error) {
	open := deps.OpenHistory
	if open == nil {
		open = openReadOnlyHistory
	}
	reader, err := open(ctx, path)
	if err != nil {
		return rerunSelection{}, fmt.Errorf("open rerun history: %w", err)
	}
	if reader == nil {
		return rerunSelection{}, &rerunSelectionFailure{message: "no saved request matches that ID"}
	}
	var selected rerunSelection
	var selectionErr error
	viewErr := reader.View(ctx, func(snapshot *history.Snapshot) error {
		matches, err := snapshot.Find(prefix)
		if err != nil {
			return err
		}
		switch len(matches) {
		case 0:
			selectionErr = &rerunSelectionFailure{message: "no saved request matches that ID"}
			return nil
		case 1:
		default:
			selectionErr = &rerunSelectionFailure{message: "request ID is ambiguous; provide a longer prefix"}
			return nil
		}
		info, err := snapshot.Request(matches[0].Key)
		if err != nil {
			return err
		}
		if info == nil {
			selectionErr = &rerunSelectionFailure{message: "no saved request matches that ID"}
			return nil
		}
		restored, err := target.Restore(target.Request{
			Tokens: append([]string(nil), info.Targets...),
			Ports:  append([]int(nil), info.TCPPorts...),
			Hosts:  append([]string(nil), info.Hosts...),
			Key:    info.Key,
		})
		if err != nil {
			return fmt.Errorf("saved request cannot be replayed: %w", err)
		}
		if len(restored.Tokens) == 0 || len(restored.Hosts) == 0 {
			selectionErr = &rerunSelectionFailure{message: "saved request has no runnable targets"}
			return nil
		}
		if info.NewestRun == nil {
			selectionErr = &rerunSelectionFailure{message: "saved request has no stored run"}
			return nil
		}
		run := info.NewestRun
		interval, err := validatedStoredInterval(run.Interval)
		if err != nil {
			return fmt.Errorf("saved run has an invalid interval: %w", err)
		}
		configuredDuration, err := validatedStoredDuration(run.Started, run.Until)
		if err != nil {
			return fmt.Errorf("saved run has an invalid configured duration: %w", err)
		}
		if every != nil {
			interval = cloneDuration(every)
		}
		if duration != nil {
			configuredDuration = cloneDuration(duration)
			if interval == nil {
				implicit := time.Minute
				interval = &implicit
			}
		}
		selected = rerunSelection{request: restored, interval: interval, duration: configuredDuration, source: cloneString(run.Source)}
		return nil
	})
	closeErr := reader.Close()
	if viewErr != nil || closeErr != nil {
		return rerunSelection{}, errors.Join(viewErr, closeErr)
	}
	if selectionErr != nil {
		return rerunSelection{}, selectionErr
	}
	return selected, nil
}

func validatedStoredInterval(value *time.Duration) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	if *value <= 0 {
		return nil, errors.New("interval must be positive")
	}
	return cloneDuration(value), nil
}

func validatedStoredDuration(started time.Time, until *time.Time) (*time.Duration, error) {
	if until == nil {
		return nil, nil
	}
	duration := until.Sub(started)
	if duration <= 0 || !started.Add(duration).Equal(*until) {
		return nil, errors.New("configured duration must be positive and representable")
	}
	return &duration, nil
}

func cloneDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
