// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

// openHistory owns the complete initial history sequence. A failed read never
// publishes a partially folded snapshot; ordinary store failures detach the
// store and let the run continue using its in-memory tracker.
func (r *runner) openHistory() error {
	if r.tracker == nil {
		r.tracker = NewTracker(r.plan.Request.Hosts, r.plan.Request.Ports, 0)
	}
	store, err := r.deps.OpenHistory(r.workCtx, r.plan.HistoryFile)
	if err != nil {
		return r.historyOperationError(err, r.workCtx)
	}
	if store == nil {
		r.disableHistory(errors.New("history opener returned no store"))
		return nil
	}
	r.store = store
	if _, err := store.Prune(r.workCtx, r.origin, r.plan.HistoryDays); err != nil {
		return r.historyOperationError(err, r.workCtx)
	}
	if err := r.readEarlier(); err != nil {
		return r.historyOperationError(err, r.workCtx)
	}
	reading := r.clock.Now()
	_, err = store.BeginRun(r.workCtx, historyRequest(r.plan.Request), history.RunStart{
		ID: r.plan.RunID, Source: cloneString(r.plan.Source), Started: r.origin,
		Interval: cloneDuration(r.plan.Interval), Until: cloneTime(r.until),
		Host: r.plan.Hostname, PID: r.plan.PID, Heartbeat: reading.Wall,
	})
	if err != nil {
		return r.historyOperationError(err, r.workCtx)
	}
	r.runRecorded = true
	return nil
}

// readEarlier folds one stable public snapshot into private trackers and only
// publishes those trackers and their derived fields after View commits.
func (r *runner) readEarlier() error {
	if r.store == nil {
		return nil
	}
	if r.plan.OwnRunOnly {
		r.tracker = NewTracker(r.plan.Request.Hosts, r.plan.Request.Ports, 0)
		r.withoutOwn = nil
		r.outcome.EarlierRounds = 0
		r.outcome.Latest = []LatestRow{}
		r.outcome.TCPOpen = false
		return nil
	}
	filter := history.WindowFilter{Since: cloneTime(r.plan.Since), Before: cloneTime(&r.origin)}
	var withOwn, withoutOwn *Tracker
	var earlierRounds int
	var latest []LatestRow
	var historicalTCPOpen bool
	var lastRound *history.StoredRound
	err := r.store.View(r.workCtx, func(snapshot *history.Snapshot) error {
		requestInfo, err := snapshot.Request(r.plan.Request.Key)
		if err != nil {
			return err
		}
		if requestInfo == nil {
			withOwn = NewTracker(r.plan.Request.Hosts, r.plan.Request.Ports, 0)
			latest = []LatestRow{}
			return nil
		}
		if !slices.Equal(requestInfo.Targets, r.plan.Request.Tokens) ||
			!slices.Equal(requestInfo.TCPPorts, r.plan.Request.Ports) ||
			!slices.Equal(requestInfo.Hosts, r.plan.Request.Hosts) {
			return fmt.Errorf("history request %q has metadata that does not match this run", r.plan.Request.Key)
		}

		withoutSpacing, err := snapshot.Spacing(requestInfo.ID, filter, nil)
		if err != nil {
			return err
		}
		withSpacing := withoutSpacing
		if r.plan.Interval == nil {
			withSpacing, err = snapshot.Spacing(requestInfo.ID, filter, &r.origin)
			if err != nil {
				return err
			}
		}
		withOwn = NewTracker(r.plan.Request.Hosts, r.plan.Request.Ports, withSpacing)
		if r.plan.Interval == nil && withSpacing != withoutSpacing {
			withoutOwn = NewTracker(r.plan.Request.Hosts, r.plan.Request.Ports, withoutSpacing)
		}
		latest = []LatestRow{}
		pageFilter := history.RoundFilter{WindowFilter: filter, Limit: 100}
		for {
			page, err := snapshot.Rounds(requestInfo.ID, pageFilter)
			if err != nil {
				return err
			}
			for _, stored := range page.Items {
				round := modelRound(stored)
				if _, err := withOwn.Add(round); err != nil {
					return fmt.Errorf("fold stored round %d: %w", stored.ID, err)
				}
				if withoutOwn != nil {
					if _, err := withoutOwn.Add(round); err != nil {
						return fmt.Errorf("fold stored round %d without prospective start: %w", stored.ID, err)
					}
				}
				if earlierRounds == math.MaxInt {
					return errors.New("history contains too many rounds for this process")
				}
				earlierRounds++
				storedCopy := stored
				lastRound = &storedCopy
				if hasRequestedTCPOpen(stored, r.plan.Request.Ports) {
					historicalTCPOpen = true
				}
			}
			if page.Next == nil {
				break
			}
			pageFilter.After = page.Next
		}
		return nil
	})
	if err != nil {
		return err
	}
	if withOwn == nil {
		withOwn = NewTracker(r.plan.Request.Hosts, r.plan.Request.Ports, 0)
	}
	if lastRound != nil {
		latest, err = storedLatest(r.plan.Request, *lastRound)
		if err != nil {
			return fmt.Errorf("read latest rows for stored round %d: %w", lastRound.ID, err)
		}
	}
	r.tracker = withOwn
	r.withoutOwn = withoutOwn
	r.outcome.EarlierRounds = earlierRounds
	r.outcome.Latest = latest
	r.outcome.TCPOpen = r.outcome.TCPOpen || historicalTCPOpen
	return nil
}

func (r *runner) recordCompleted(round history.RoundInput) error {
	if r.store == nil {
		return nil
	}
	if _, err := r.store.RecordRound(r.hardCtx, r.plan.RunID, round); err != nil {
		return r.historyOperationError(err, r.hardCtx)
	}
	return nil
}

func (r *runner) heartbeat(elapsed *time.Duration, completed bool) error {
	if r.store == nil || !r.runRecorded {
		return nil
	}
	ctx := r.workCtx
	if completed {
		ctx = r.hardCtx
	}
	reading := r.clock.Now()
	if err := r.store.Heartbeat(ctx, r.plan.RunID, reading.Wall, elapsed); err != nil {
		return r.historyOperationError(err, ctx)
	}
	return nil
}

func (r *runner) endHistory(status string) error {
	if r.store == nil {
		return nil
	}
	store := r.store
	r.store = nil
	var endErr error
	if r.runRecorded {
		if r.hardCtx == nil {
			endErr = errors.New("hard context is nil while finalizing history")
		} else if err := r.hardCtx.Err(); err != nil {
			endErr = err
		} else {
			reading := r.clock.Now()
			if err := store.EndRun(r.hardCtx, r.plan.RunID, reading.Run, status); err != nil {
				endErr = r.historyOperationError(err, r.hardCtx)
			}
		}
	}
	closeErr := store.Close()
	if closeErr != nil {
		r.disableHistory(closeErr)
	}
	if endErr != nil {
		return errors.Join(endErr, closeErr)
	}
	return nil
}

func (r *runner) historyOperationError(err error, operationCtx context.Context) error {
	if err == nil {
		return nil
	}
	if operationCtx != nil && operationCtx.Err() != nil && history.CancellationOnly(err, operationCtx.Err()) {
		return err
	}
	r.disableHistory(err)
	return nil
}

func (r *runner) disableHistory(err error) {
	if err == nil {
		return
	}
	if store := r.store; store != nil {
		r.store = nil
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if r.outcome.HistoryError == nil {
		r.outcome.HistoryError = err
		if r.deps.Note != nil {
			r.deps.Note(fmt.Sprintf("history disabled after storage error: %v", err))
		}
	} else if errors.Is(r.outcome.HistoryError, err) {
		return
	} else {
		r.outcome.HistoryError = errors.Join(r.outcome.HistoryError, err)
	}
}

func modelRound(stored history.StoredRound) Round {
	round := Round{
		Started: stored.Started, Interval: durationValue(stored.Interval),
		Observations: stored.Observations, Rows: make(map[string]Row, len(stored.Rows)),
		Times: make(map[string]time.Time, len(stored.Rows)),
	}
	for _, row := range stored.Rows {
		round.Rows[row.Host] = Row{Address: row.Address, Result: row.Result, TCP: cloneTCPStates(row.TCP)}
		round.Times[row.Host] = row.Time
	}
	return round
}

func storedLatest(request target.Request, stored history.StoredRound) ([]LatestRow, error) {
	rows := make(map[string]history.ResultRow, len(stored.Rows))
	for _, row := range stored.Rows {
		if _, duplicate := rows[row.Host]; duplicate {
			return nil, fmt.Errorf("stored round contains duplicate host %q", row.Host)
		}
		rows[row.Host] = row
	}
	if len(stored.Observations) != len(request.Hosts) {
		return nil, fmt.Errorf("stored observation vector has %d entries for %d hosts", len(stored.Observations), len(request.Hosts))
	}
	latest := make([]LatestRow, 0, len(request.Hosts))
	for index, host := range request.Hosts {
		observation := stored.Observations[index]
		if isSeen(observation) {
			row, ok := rows[host]
			if !ok {
				return nil, fmt.Errorf("stored seen host %q has no detail row", host)
			}
			latest = append(latest, LatestRow{Host: host, Row: Row{Address: row.Address, Result: row.Result, TCP: cloneTCPStates(row.TCP)}})
			delete(rows, host)
			continue
		}
		if _, ok := rows[host]; ok {
			return nil, fmt.Errorf("stored unseen host %q has a detail row", host)
		}
		result, err := ResultText(observation)
		if err != nil {
			return nil, fmt.Errorf("stored observation for %q: %w", host, err)
		}
		tcp := make(map[int]string, len(request.Ports))
		for _, port := range request.Ports {
			tcp[port] = ""
		}
		latest = append(latest, LatestRow{Host: host, Row: Row{Result: result, TCP: tcp}})
	}
	if len(rows) != 0 {
		for host := range rows {
			return nil, fmt.Errorf("stored round contains unknown host %q", host)
		}
	}
	return latest, nil
}

func gracefulHistoryCancellation(workCtx, hardCtx context.Context, err error) bool {
	if workCtx == nil || hardCtx == nil || err == nil || workCtx.Err() == nil || hardCtx.Err() != nil {
		return false
	}
	return history.CancellationOnly(err, workCtx.Err())
}

func historyRequest(request target.Request) history.Request {
	return history.Request{
		RequestMeta: history.RequestMeta{
			Key: request.Key, Targets: append([]string(nil), request.Tokens...),
			TCPPorts: append([]int(nil), request.Ports...),
		},
		Hosts: append([]string(nil), request.Hosts...),
	}
}

func hasRequestedTCPOpen(stored history.StoredRound, ports []int) bool {
	for _, row := range stored.Rows {
		for _, port := range ports {
			if row.TCP[port] == "open" {
				return true
			}
		}
	}
	return false
}

func cloneTCPStates(values map[int]string) map[int]string {
	if values == nil {
		return nil
	}
	copyValues := make(map[int]string, len(values))
	for port, state := range values {
		copyValues[port] = state
	}
	return copyValues
}
