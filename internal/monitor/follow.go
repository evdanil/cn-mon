// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
)

const (
	followPollInterval = 2 * time.Second
	followDrawInterval = 500 * time.Millisecond
)

type followSnapshot struct {
	read      readSnapshot
	activeRun *history.RunInfo
	window    history.WindowInfo
	through   int64
}

// followRead owns one read-only Store for the complete follower lifetime.
func followRead(ctx context.Context, plan ReadPlan, deps ReadDeps) (ReadOutcome, error) {
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

	now := readNow(deps)
	initial := followSnapshot{}
	var resolvedKey string
	var problem string
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		matches, err := snapshot.Find(plan.Prefix)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			problem = fmt.Sprintf("no request matches %q", plan.Prefix)
			return nil
		}
		if len(matches) > 1 {
			problem = fmt.Sprintf("request prefix %q is ambiguous", plan.Prefix)
			return nil
		}
		resolvedKey = matches[0].Key
		info, err := snapshot.Request(resolvedKey)
		if err != nil || info == nil {
			return err
		}
		initial, err = readFollowSnapshot(snapshot, info, history.WindowFilter{Since: plan.Since}, now, deps, nil, 0, nil)
		return err
	})
	if err != nil {
		return closeReadError(reader, &deferClose, err)
	}
	if problem != "" {
		if closeErr := closeRead(reader, &deferClose); closeErr != nil {
			return ReadOutcome{}, closeErr
		}
		return ReadOutcome{Problem: problem}, nil
	}
	if resolvedKey == "" {
		if closeErr := closeRead(reader, &deferClose); closeErr != nil {
			return ReadOutcome{}, closeErr
		}
		return ReadOutcome{Problem: fmt.Sprintf("no request matches %q", plan.Prefix)}, nil
	}
	if !initial.read.outcome.Running {
		if deps.Note != nil {
			deps.Note("no live run found; showing current history")
		}
		finalizeReadSnapshot(&initial.read)
		if closeErr := closeRead(reader, &deferClose); closeErr != nil {
			return ReadOutcome{}, closeErr
		}
		return initial.read.outcome, nil
	}
	if err := ctx.Err(); err != nil {
		return closeReadError(reader, &deferClose, err)
	}
	if deps.EnterFollowing != nil {
		deps.EnterFollowing()
	}
	drawFollow(deps.Live, initial, now, LiveFollowing)

	clock := readClock(deps)
	lastPoll := clock.Now().Mono
	for {
		if ctx.Err() != nil {
			return closeReadError(reader, &deferClose, ctx.Err())
		}
		if stopClosed(deps.Stop) {
			final, readErr := pollFollow(ctx, reader, resolvedKey, plan.Since, deps, initial)
			if readErr != nil {
				return closeReadError(reader, &deferClose, readErr)
			}
			initial = final
			drawFollow(deps.Live, initial, readNow(deps), LiveEnded)
			finalizeReadSnapshot(&initial.read)
			if closeErr := closeRead(reader, &deferClose); closeErr != nil {
				return ReadOutcome{}, closeErr
			}
			return initial.read.outcome, nil
		}
		select {
		case <-ctx.Done():
			return closeReadError(reader, &deferClose, ctx.Err())
		case <-deps.Stop:
			continue
		case <-clock.After(followDrawInterval):
		}
		if ctx.Err() != nil {
			return closeReadError(reader, &deferClose, ctx.Err())
		}
		now = clock.Now().Wall
		drawFollow(deps.Live, initial, now, LiveFollowing)
		mono := clock.Now().Mono
		if mono < lastPoll {
			lastPoll = mono
			continue
		}
		if mono-lastPoll < followPollInterval {
			continue
		}
		current, readErr := pollFollow(ctx, reader, resolvedKey, plan.Since, deps, initial)
		if readErr != nil {
			return closeReadError(reader, &deferClose, readErr)
		}
		initial = current
		lastPoll = mono
		if stopClosed(deps.Stop) {
			continue
		}
		if !initial.read.outcome.Running {
			drawFollow(deps.Live, initial, now, LiveEnded)
			finalizeReadSnapshot(&initial.read)
			if closeErr := closeRead(reader, &deferClose); closeErr != nil {
				return ReadOutcome{}, closeErr
			}
			return initial.read.outcome, nil
		}
	}
}

func pollFollow(ctx context.Context, reader Reader, key string, since *time.Time, deps ReadDeps, previous followSnapshot) (followSnapshot, error) {
	now := readNow(deps)
	var current followSnapshot
	err := reader.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info == nil {
			window, err := snapshot.Window(previous.read.requestID, history.WindowFilter{Since: cloneTime(since)})
			if err != nil {
				return err
			}
			current = missingFollowSnapshot(previous)
			current.window = window
			current.read.through = window.Through
			current.through = max(previous.through, window.Through)
			return nil
		}
		current, err = readFollowSnapshot(snapshot, info, history.WindowFilter{Since: cloneTime(since)}, now, deps, &previous, previous.through, followChangeCallback(deps))
		return err
	})
	if err != nil {
		return followSnapshot{}, err
	}
	return current, nil
}

func readFollowSnapshot(snapshot *history.Snapshot, info *history.RequestInfo, filter history.WindowFilter, now time.Time, deps ReadDeps, previous *followSnapshot, observedThrough int64, onChange func(history.StoredRound, Change)) (followSnapshot, error) {
	window, err := snapshot.Window(info.ID, filter)
	if err != nil {
		return followSnapshot{}, err
	}
	anyLive, ownerLive, active, err := liveRuns(snapshot, info.ID, previousOwner(previous), now, deps)
	if err != nil {
		return followSnapshot{}, err
	}
	changed := previous == nil || selectedWindowChanged(previous, info, window)
	if !changed && window.Through > observedThrough {
		page, err := snapshot.Rounds(info.ID, history.RoundFilter{WindowFilter: filter, NewerThan: observedThrough, Limit: 1})
		if err != nil {
			return followSnapshot{}, err
		}
		changed = len(page.Items) != 0
	}
	if changed {
		read, err := readRequestSnapshotChanges(snapshot, info, filter, now, deps, observedThrough, onChange, false)
		if err != nil {
			return followSnapshot{}, err
		}
		read.outcome.Running = anyLive
		return followSnapshot{read: read, activeRun: active, window: window, through: max(observedThrough, window.Through)}, nil
	}
	current := *previous
	current.activeRun = active
	current.window = window
	current.through = max(observedThrough, window.Through)
	current.read.through = window.Through
	current.read.outcome.Running = anyLive
	current.read.ownerLive = ownerLive
	current.read.outcome.Run.Seen = nil
	return current, nil
}

func followChangeCallback(deps ReadDeps) func(history.StoredRound, Change) {
	if deps.Live == nil || deps.Live.Change == nil {
		return nil
	}
	return func(_ history.StoredRound, change Change) {
		if deps.Live.Enabled == nil || deps.Live.Enabled() {
			deps.Live.Change(change)
		}
	}
}

func selectedWindowChanged(previous *followSnapshot, info *history.RequestInfo, window history.WindowInfo) bool {
	if previous == nil || previous.read.requestID != info.ID ||
		previous.read.outcome.Request.Key != info.Key ||
		!reflect.DeepEqual(previous.read.outcome.Request.Tokens, info.Targets) ||
		!reflect.DeepEqual(previous.read.outcome.Request.Ports, info.TCPPorts) ||
		!reflect.DeepEqual(previous.read.outcome.Request.Hosts, info.Hosts) {
		return true
	}
	old := previous.window
	return old.Count != window.Count || !reflect.DeepEqual(old.First, window.First) || !reflect.DeepEqual(old.Last, window.Last)
}

func previousOwner(previous *followSnapshot) string {
	if previous == nil {
		return ""
	}
	return previous.read.lastOwner
}

func liveRuns(snapshot *history.Snapshot, requestID int64, owner string, now time.Time, deps ReadDeps) (bool, bool, *history.RunInfo, error) {
	anyLive, ownerLive := false, false
	var active *history.RunInfo
	var after *history.RunCursor
	for {
		page, err := snapshot.Runs(requestID, true, after, 100)
		if err != nil {
			return false, false, nil, err
		}
		for i := range page.Items {
			run := page.Items[i]
			if history.RunState(run, now, deps.Hostname, pidStateFunc(deps)) != "running" {
				continue
			}
			anyLive = true
			if active == nil {
				copy := run
				active = &copy
			}
			if run.ID == owner {
				ownerLive = true
			}
		}
		if page.Next == nil {
			return anyLive, ownerLive, active, nil
		}
		after = page.Next
	}
}

func missingFollowSnapshot(previous followSnapshot) followSnapshot {
	request := previous.read.outcome.Request
	read := readSnapshot{
		requestID: previous.read.requestID,
		outcome: ReadOutcome{Request: request, Found: true, Running: false, Run: RunOutcome{
			Seen: []HostSummary{}, Latest: []LatestRow{}, Unresolved: []string{}, TCPRequired: len(request.Ports) > 0,
		}},
	}
	return followSnapshot{read: read, window: history.WindowInfo{}, through: previous.through}
}

func finalizeReadSnapshot(read *readSnapshot) {
	if read.tracker == nil {
		return
	}
	read.outcome.Run.Seen = read.tracker.Summaries(read.ownerLive)
	read.outcome.Run.Stats = read.tracker.Stats()
}

func drawFollow(hooks *LiveHooks, snapshot followSnapshot, now time.Time, phase LivePhase) {
	if hooks == nil || hooks.Draw == nil || (hooks.Enabled != nil && !hooks.Enabled()) || snapshot.read.tracker == nil {
		return
	}
	data := makeLiveData(snapshot.read.tracker, snapshot.read.outcome.Request.Hosts, snapshot.read.outcome.Request.Ports, nil, nil, nil)
	update := LiveUpdate{Request: cloneRequest(snapshot.read.outcome.Request), Data: data, Phase: phase, Now: now}
	if snapshot.activeRun != nil {
		active := snapshot.activeRun
		update.Origin = active.Started
		update.Until = cloneTime(active.Until)
		update.Interval = cloneDuration(active.Interval)
		update.Source = cloneString(active.Source)
		if active.Rounds > int64(^uint(0)>>1) {
			update.CurrentRounds = int(^uint(0) >> 1)
		} else if active.Rounds > 0 {
			update.CurrentRounds = int(active.Rounds)
		}
	}
	hooks.Draw(update)
}

func stopClosed(stop <-chan struct{}) bool {
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func closeRead(reader Reader, deferred *bool) error {
	*deferred = false
	return reader.Close()
}

func closeReadError(reader Reader, deferred *bool, err error) (ReadOutcome, error) {
	closeErr := closeRead(reader, deferred)
	return ReadOutcome{}, errors.Join(err, closeErr)
}
