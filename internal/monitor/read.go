// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

func openReader(ctx context.Context, path string, deps ReadDeps) (Reader, error) {
	if deps.OpenHistory != nil {
		return deps.OpenHistory(ctx, path)
	}
	store, err := history.OpenReader(ctx, path)
	if err != nil || store == nil {
		return nil, err
	}
	return store, nil
}

type readSnapshot struct {
	outcome       ReadOutcome
	window        history.WindowInfo
	through       int64
	requestID     int64
	tracker       *Tracker
	lastOwner     string
	ownerLive     bool
	lastRound     *history.StoredRound
	selectedCount int64
}

func readClock(deps ReadDeps) Clock {
	if deps.Clock == nil {
		return NewClock()
	}
	return deps.Clock
}

func readNow(deps ReadDeps) time.Time { return readClock(deps).Now().Wall }

func listLiveRun(snapshot *history.Snapshot, requestID int64, now time.Time, deps ReadDeps) (*history.RunInfo, error) {
	var after *history.RunCursor
	for {
		page, err := snapshot.Runs(requestID, true, after, 100)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			if history.RunState(page.Items[i], now, deps.Hostname, pidStateFunc(deps)) == "running" {
				selected := page.Items[i]
				return &selected, nil
			}
		}
		if page.Next == nil {
			return nil, nil
		}
		after = page.Next
	}
}

// List returns all request summaries from one read-only history snapshot.
func List(ctx context.Context, path string, deps ReadDeps) ([]RequestRow, error) {
	reader, err := openReader(ctx, path, deps)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return []RequestRow{}, nil
	}
	deferClose := true
	defer func() {
		if deferClose {
			_ = reader.Close()
		}
	}()
	now := readNow(deps)
	rows := make([]RequestRow, 0)
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		var after *history.RequestCursor
		for {
			page, err := snapshot.Requests(after, 100)
			if err != nil {
				return err
			}
			for _, summary := range page.Items {
				stateRun, err := listLiveRun(snapshot, summary.ID, now, deps)
				if err != nil {
					return err
				}
				if stateRun == nil {
					stateRun = summary.NewestRun
				}
				row, err := requestRow(summary, stateRun, now, deps)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if page.Next == nil {
				return nil
			}
			after = page.Next
		}
	})
	closeErr := reader.Close()
	deferClose = false
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	return rows, nil
}

// Read builds one show snapshot, optionally continuing as a follower.
func Read(ctx context.Context, plan ReadPlan, deps ReadDeps) (ReadOutcome, error) {
	if plan.Follow {
		return followRead(ctx, plan, deps)
	}
	plan.Since = cloneTime(plan.Since)
	reader, err := openReader(ctx, plan.HistoryFile, deps)
	if err != nil {
		return ReadOutcome{}, err
	}
	if reader == nil {
		return ReadOutcome{Problem: fmt.Sprintf("no history found for %q", plan.Prefix)}, nil
	}
	deferClose := true
	defer func() {
		if deferClose {
			_ = reader.Close()
		}
	}()
	result := readSnapshot{}
	now := readNow(deps)
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		matches, err := snapshot.Find(plan.Prefix)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			result.outcome.Problem = fmt.Sprintf("no request matches %q", plan.Prefix)
			return nil
		}
		if len(matches) > 1 {
			result.outcome.Problem = fmt.Sprintf("request prefix %q is ambiguous", plan.Prefix)
			return nil
		}
		info, err := snapshot.Request(matches[0].Key)
		if err != nil {
			return err
		}
		if info == nil {
			result.outcome.Problem = fmt.Sprintf("no request matches %q", plan.Prefix)
			return nil
		}
		result, err = readRequestSnapshot(snapshot, info, history.WindowFilter{Since: plan.Since}, now, deps)
		return err
	})
	closeErr := reader.Close()
	deferClose = false
	if err != nil || closeErr != nil {
		return ReadOutcome{}, errors.Join(err, closeErr)
	}
	return result.outcome, nil
}

// ReadExitCode applies the historical show/follow reachability rules.
func ReadExitCode(out ReadOutcome) int {
	if !out.Found {
		return 2
	}
	if (!out.Run.TCPRequired && out.Run.Stats.Seen > 0) || (out.Run.TCPRequired && out.Run.TCPOpen) {
		return 0
	}
	return 1
}

func readRequestSnapshot(snapshot *history.Snapshot, info *history.RequestInfo, filter history.WindowFilter, now time.Time, deps ReadDeps) (readSnapshot, error) {
	return readRequestSnapshotChanges(snapshot, info, filter, now, deps, -1, nil, true)
}

func readRequestSnapshotChanges(snapshot *history.Snapshot, info *history.RequestInfo, filter history.WindowFilter, now time.Time, deps ReadDeps, noteAfterID int64, onChange func(history.StoredRound, Change), materializeSeen bool) (readSnapshot, error) {
	request := target.Request{
		Tokens: append([]string(nil), info.Targets...), Ports: append([]int(nil), info.TCPPorts...),
		Hosts: append([]string(nil), info.Hosts...), Key: info.Key,
	}
	result := readSnapshot{requestID: info.ID, through: 0}
	result.outcome = ReadOutcome{Request: request, Found: true, Run: RunOutcome{Seen: []HostSummary{}, Latest: []LatestRow{}, Unresolved: []string{}, TCPRequired: len(request.Ports) > 0}}
	window, err := snapshot.Window(info.ID, filter)
	if err != nil {
		return readSnapshot{}, err
	}
	result.window, result.through = window, window.Through
	spacing, err := snapshot.Spacing(info.ID, filter, nil)
	if err != nil {
		return readSnapshot{}, err
	}
	tracker := NewTracker(request.Hosts, request.Ports, spacing)
	var last *history.StoredRound
	var count int64
	pageFilter := history.RoundFilter{WindowFilter: filter, Limit: 100}
	for {
		page, err := snapshot.Rounds(info.ID, pageFilter)
		if err != nil {
			return readSnapshot{}, err
		}
		for i := range page.Items {
			stored := page.Items[i]
			changes, err := tracker.Add(modelRound(stored))
			if err != nil {
				return readSnapshot{}, fmt.Errorf("fold stored round %d: %w", stored.ID, err)
			}
			if stored.ID > noteAfterID && onChange != nil {
				for _, change := range changes {
					onChange(stored, change)
				}
			}
			count++
			copy := stored
			last = &copy
			if hasRequestedTCPOpen(stored, request.Ports) {
				result.outcome.Run.TCPOpen = true
			}
		}
		if page.Next == nil {
			break
		}
		pageFilter.After = page.Next
	}
	if count != window.Count {
		return readSnapshot{}, fmt.Errorf("history window count changed within snapshot: folded %d of %d rounds", count, window.Count)
	}
	currentRounds, err := readIntervalCount(count)
	if err != nil {
		return readSnapshot{}, err
	}
	result.selectedCount = count
	result.tracker = tracker
	result.lastRound = last
	if last != nil {
		result.lastOwner = last.RunID
		latest, err := storedLatest(request, *last)
		if err != nil {
			return readSnapshot{}, fmt.Errorf("read latest rows for stored round %d: %w", last.ID, err)
		}
		result.outcome.Run.Latest = latest
		result.outcome.Run.Interval = cloneDuration(last.Interval)
	}
	runsLive, ownerLive, err := requestLiveness(snapshot, info.ID, result.lastOwner, now, deps)
	if err != nil {
		return readSnapshot{}, err
	}
	result.ownerLive = ownerLive
	result.outcome.Running = runsLive
	result.outcome.Run.Stats = tracker.Stats()
	if materializeSeen {
		result.outcome.Run.Seen = tracker.Summaries(ownerLive)
	} else {
		result.outcome.Run.Seen = nil
	}
	result.outcome.Run.CurrentRounds = currentRounds
	result.outcome.Run.EarlierRounds = 0
	return result, nil
}

func requestLiveness(snapshot *history.Snapshot, requestID int64, owner string, now time.Time, deps ReadDeps) (bool, bool, error) {
	anyLive, ownerLive := false, false
	var after *history.RunCursor
	for {
		page, err := snapshot.Runs(requestID, true, after, 100)
		if err != nil {
			return false, false, err
		}
		for _, run := range page.Items {
			if history.RunState(run, now, deps.Hostname, pidStateFunc(deps)) != "running" {
				continue
			}
			anyLive = true
			if run.ID == owner {
				ownerLive = true
			}
		}
		if page.Next == nil {
			return anyLive, ownerLive, nil
		}
		after = page.Next
	}
}
