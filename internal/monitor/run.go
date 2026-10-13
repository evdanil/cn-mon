// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/probe"
)

func Run(ctx context.Context, plan RunPlan, deps RunDeps) (outcome RunOutcome, runErr error) {
	if err := validateRun(ctx, plan, deps); err != nil {
		return RunOutcome{}, err
	}
	if deps.Clock == nil {
		deps.Clock = NewClock()
	}
	plan = cloneRunPlan(plan)
	outcome = RunOutcome{
		Seen:        []HostSummary{},
		Latest:      []LatestRow{},
		Unresolved:  []string{},
		Interval:    cloneDuration(plan.Interval),
		TCPRequired: len(plan.Request.Ports) > 0,
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	stopForwardDone := make(chan struct{})
	stopForwardStop := make(chan struct{})
	if deps.Stop != nil {
		go func() {
			defer close(stopForwardDone)
			select {
			case <-deps.Stop:
				cancelWork()
			case <-ctx.Done():
			case <-stopForwardStop:
			}
		}()
	} else {
		close(stopForwardDone)
	}
	defer func() {
		close(stopForwardStop)
		cancelWork()
		<-stopForwardDone
	}()

	r := &runner{
		plan:       plan,
		deps:       deps,
		hardCtx:    ctx,
		workCtx:    workCtx,
		clock:      newRunClock(deps.Clock),
		outcome:    outcome,
		resolved:   make(map[string]struct{}, len(plan.Request.Hosts)),
		unresolved: make(map[string]struct{}, len(plan.Request.Hosts)),
	}
	defer func() {
		if r.store == nil {
			return
		}
		store := r.store
		r.store = nil
		if err := store.Close(); err != nil {
			r.disableHistory(err)
			outcome = r.finishOutcome()
			if runErr != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	originReading := r.clock.Now()
	r.origin = originReading.Run
	lastBusyHeartbeat := originReading.Mono
	schedule, err := newRunSchedule(r.origin, plan.Interval, plan.Duration)
	if err != nil {
		return r.outcome, fmt.Errorf("invalid run schedule: %w", err)
	}
	r.until = schedule.until

	if err := r.openHistory(); err != nil {
		if gracefulHistoryCancellation(r.workCtx, r.hardCtx, err) {
			r.outcome.Stopped = true
		} else if r.hardCtx.Err() != nil {
			return r.finishOutcome(), r.hardCtx.Err()
		} else {
			return r.finishUnexpected(err)
		}
	}
	lastBusyHeartbeat = r.clock.Now().Mono
	if r.tracker == nil {
		r.tracker = NewTracker(plan.Request.Hosts, plan.Request.Ports, 0)
	}
	if r.hardCtx.Err() != nil {
		return r.finishOutcome(), r.hardCtx.Err()
	}
	if deps.Stop != nil {
		select {
		case <-deps.Stop:
			r.outcome.Stopped = true
		default:
		}
	}
	if r.workCtx.Err() != nil {
		r.outcome.Stopped = true
	}

	if !r.outcome.Stopped {
		now := r.clock.Now().Run
		index, slot, done, scheduleErr := schedule.first(now)
		if scheduleErr != nil {
			return r.finishUnexpected(scheduleErr)
		}
		if !done {
			if plan.Interval != nil && deps.Note != nil {
				batches := (len(plan.Request.Hosts) + 99) / 100
				estimate := time.Duration(batches) * 3 * time.Second
				if estimate > *plan.Interval {
					deps.Note(fmt.Sprintf("initial round may take about %s for %d probe batches; this is an estimate", estimate, batches))
				}
			}
			for {
				if r.hardCtx.Err() != nil {
					return r.finishOutcome(), r.hardCtx.Err()
				}
				if r.workCtx.Err() != nil {
					r.outcome.Stopped = true
					break
				}
				startReading := r.clock.Now()
				now = startReading.Run
				if index > 0 && ((schedule.until != nil && !now.Before(*schedule.until)) || now.Sub(slot) >= *plan.Interval) {
					nextIndex, nextSlot, skipped, finished, nextErr := schedule.next(index-1, now)
					if nextErr != nil {
						return r.finishUnexpected(nextErr)
					}
					r.noteSkippedSlots(skipped)
					if finished {
						break
					}
					index, slot = nextIndex, nextSlot
					if slot.After(now) {
						stopped, waitErr := r.waitUntilLive(cancelWork, slot, &lastBusyHeartbeat, LiveWaiting)
						if waitErr != nil {
							if r.hardCtx.Err() != nil {
								return r.finishOutcome(), r.hardCtx.Err()
							}
							return r.finishUnexpected(waitErr)
						}
						if stopped {
							r.outcome.Stopped = true
							break
						}
					}
					continue
				}
				if schedule.until != nil && !now.Before(*schedule.until) {
					break
				}
				actualStart := now
				roundStart := actualStart
				if plan.Interval == nil {
					roundStart = r.origin
				}
				completed, roundErr := r.runRoundWithHeartbeat(cancelWork, roundStart, actualStart, &lastBusyHeartbeat)
				if roundErr != nil {
					if r.hardCtx.Err() != nil {
						return r.finishOutcome(), r.hardCtx.Err()
					}
					if r.workCtx.Err() != nil && isContextStop(roundErr) {
						r.outcome.Stopped = true
						break
					}
					return r.finishUnexpected(roundErr)
				}
				if !completed {
					if r.hardCtx.Err() != nil {
						return r.finishOutcome(), r.hardCtx.Err()
					}
					if r.workCtx.Err() != nil {
						r.outcome.Stopped = true
					}
					break
				}
				if plan.Interval == nil {
					break
				}
				now = r.clock.Now().Run
				nextIndex, nextSlot, skipped, finished, nextErr := schedule.next(index, now)
				if nextErr != nil {
					return r.finishUnexpected(nextErr)
				}
				r.noteSkippedSlots(skipped)
				if finished {
					break
				}
				stopped, waitErr := r.waitUntilLive(cancelWork, nextSlot, &lastBusyHeartbeat, LiveWaiting)
				if waitErr != nil {
					if r.hardCtx.Err() != nil {
						return r.finishOutcome(), r.hardCtx.Err()
					}
					return r.finishUnexpected(waitErr)
				}
				if stopped {
					r.outcome.Stopped = true
					break
				}
				index, slot = nextIndex, nextSlot
			}
		}
	}
	if r.hardCtx.Err() != nil {
		return r.finishOutcome(), r.hardCtx.Err()
	}
	if deps.Stop != nil {
		select {
		case <-deps.Stop:
			r.outcome.Stopped = true
		default:
		}
	}
	if r.workCtx.Err() != nil {
		r.outcome.Stopped = true
	}
	if r.outcome.Stopped && deps.Note != nil {
		deps.Note("stopping; preparing the final summary")
	}
	status := "finished"
	if r.outcome.Stopped {
		status = "stopped"
	}
	if err := r.endHistory(status); err != nil {
		return r.finishUnexpected(err)
	}
	if r.liveDrawEnabled() {
		r.drawLive(r.clock.Now(), LiveEnded, nil, nil, nil, r.outcome.CurrentRounds, len(r.plan.Request.Hosts), nil)
	}
	return r.finishOutcome(), nil
}

func validateRun(ctx context.Context, plan RunPlan, deps RunDeps) error {
	if ctx == nil {
		return errors.New("monitor run: nil context")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(plan.Request.Hosts) == 0 || plan.Request.Key == "" {
		return errors.New("monitor run: request must contain a key and at least one host")
	}
	if plan.RunID == "" || plan.HistoryFile == "" || plan.HistoryDays == nil || plan.HistoryDays.Sign() <= 0 {
		return errors.New("monitor run: run ID, history path and positive retention are required")
	}
	if deps.Prober == nil || deps.OpenHistory == nil {
		return errors.New("monitor run: prober and history opener are required")
	}
	if plan.Interval != nil && *plan.Interval <= 0 {
		return errors.New("monitor run: interval must be positive")
	}
	if plan.Duration != nil && *plan.Duration <= 0 {
		return errors.New("monitor run: duration must be positive")
	}
	seenHosts := make(map[string]struct{}, len(plan.Request.Hosts))
	for _, host := range plan.Request.Hosts {
		if host == "" {
			return errors.New("monitor run: request hosts must be nonempty")
		}
		if _, duplicate := seenHosts[host]; duplicate {
			return errors.New("monitor run: request hosts must be unique")
		}
		seenHosts[host] = struct{}{}
	}
	for i, port := range plan.Request.Ports {
		if port < 1 || port > 65535 || (i > 0 && port <= plan.Request.Ports[i-1]) {
			return errors.New("monitor run: request ports must be sorted, unique and in range")
		}
	}
	return nil
}

func cloneRunPlan(plan RunPlan) RunPlan {
	plan.Request.Tokens = append([]string(nil), plan.Request.Tokens...)
	plan.Request.Hosts = append([]string(nil), plan.Request.Hosts...)
	plan.Request.Ports = append([]int(nil), plan.Request.Ports...)
	plan.Interval = cloneDuration(plan.Interval)
	plan.Duration = cloneDuration(plan.Duration)
	plan.Since = cloneTime(plan.Since)
	plan.Source = cloneString(plan.Source)
	if plan.HistoryDays != nil {
		plan.HistoryDays = new(big.Int).Set(plan.HistoryDays)
	}
	return plan
}

func cloneDuration(value *time.Duration) *time.Duration {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

type asyncResult[T any] struct {
	value T
	err   error
}

func awaitWork[T any](r *runner, cancelWork context.CancelFunc, lastHeartbeat *time.Duration, work func(context.Context) (T, error)) (T, error, bool) {
	return awaitWorkObserved(r, cancelWork, lastHeartbeat, nil, work)
}

type liveProgress struct {
	phase       LivePhase
	pending     map[int]byte
	pendingRows map[int]Row
	pendingTime map[int]time.Time
	done, total int
	nextSlot    *time.Time
}

func awaitWorkLive[T any](r *runner, cancelWork context.CancelFunc, lastHeartbeat *time.Duration, progress liveProgress, work func(context.Context) (T, error)) (T, error, bool) {
	return awaitWorkObserved(r, cancelWork, lastHeartbeat, &progress, work)
}

func awaitWorkObserved[T any](r *runner, cancelWork context.CancelFunc, lastHeartbeat *time.Duration, progress *liveProgress, work func(context.Context) (T, error)) (T, error, bool) {
	var zero T
	var reading runReading
	read := false
	if lastHeartbeat != nil {
		reading = r.clock.Now()
		read = true
		if reading.Mono-*lastHeartbeat >= 10*time.Second {
			if err := r.heartbeat(nil, false); err != nil {
				return zero, err, true
			}
			*lastHeartbeat = reading.Mono
		}
	}
	if progress != nil && r.liveDrawEnabled() {
		if !read {
			reading = r.clock.Now()
		}
		r.drawLive(reading, progress.phase, progress.pending, progress.pendingRows, progress.pendingTime, progress.done, progress.total, progress.nextSlot)
	}
	results := make(chan asyncResult[T], 1)
	go func() {
		value, err := work(r.workCtx)
		results <- asyncResult[T]{value: value, err: err}
	}()
	timerReading := r.clock.Now()
	delay := heartbeatDelay(timerReading.Mono, lastHeartbeat)
	if progress != nil && r.liveDrawEnabled() {
		delay = r.liveDelay(timerReading.Mono, lastHeartbeat)
	}
	timer := r.deps.Clock.After(delay)
	for {
		select {
		case result := <-results:
			return result.value, result.err, r.workCtx.Err() != nil
		case <-r.workCtx.Done():
			result := <-results
			return result.value, result.err, true
		case <-timer:
			reading := r.clock.Now()
			if lastHeartbeat != nil && reading.Mono-*lastHeartbeat >= 10*time.Second {
				if err := r.heartbeat(nil, false); err != nil {
					cancelWork()
					result := <-results
					if result.err == nil && (r.hardCtx.Err() != nil || gracefulHistoryCancellation(r.workCtx, r.hardCtx, err)) {
						return result.value, nil, true
					}
					return result.value, errors.Join(err, result.err), true
				}
				*lastHeartbeat = reading.Mono
			}
			if progress != nil && r.liveDrawEnabled() {
				r.drawLive(reading, progress.phase, progress.pending, progress.pendingRows, progress.pendingTime, progress.done, progress.total, progress.nextSlot)
				// Draw is synchronous and may block. Re-sample on the state owner
				// before choosing the next wake so a due heartbeat is not deferred.
				scheduleMono := r.clock.Now().Mono
				timer = r.deps.Clock.After(r.liveDelay(scheduleMono, lastHeartbeat))
			} else {
				timer = r.deps.Clock.After(heartbeatDelay(r.clock.Now().Mono, lastHeartbeat))
			}
		}
	}
}

func heartbeatDelay(now time.Duration, last *time.Duration) time.Duration {
	if last == nil {
		return 10 * time.Second
	}
	elapsed := now - *last
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed >= 10*time.Second {
		return time.Nanosecond
	}
	return 10*time.Second - elapsed
}

func (r *runner) runRound(cancelWork context.CancelFunc, roundStart, actualStart time.Time) (bool, error) {
	lastBusyHeartbeat := r.clock.Now().Mono
	return r.runRoundWithHeartbeat(cancelWork, roundStart, actualStart, &lastBusyHeartbeat)
}

func (r *runner) noteSkippedSlots(skipped int64) {
	if skipped == 0 {
		return
	}
	r.outcome.SkippedSlots += skipped
	if r.deps.Note != nil {
		r.deps.Note(fmt.Sprintf("skipped %d scheduled round slot(s)", skipped))
	}
}

func (r *runner) runRoundWithHeartbeat(cancelWork context.CancelFunc, roundStart, actualStart time.Time, lastBusyHeartbeat *time.Duration) (bool, error) {
	hosts := r.plan.Request.Hosts
	pending := make(map[int]byte)
	pendingRows := make(map[int]Row)
	pendingTimes := make(map[int]time.Time)
	resolved, resolveErr, canceled := awaitWorkLive(r, cancelWork, lastBusyHeartbeat, liveProgress{
		phase: LiveResolving, pending: pending, pendingRows: pendingRows, pendingTime: pendingTimes,
		total: len(hosts),
	}, func(ctx context.Context) ([]probe.Resolved, error) {
		return r.deps.Prober.Resolve(ctx, hosts)
	})
	if resolveErr != nil {
		if r.hardCtx.Err() != nil {
			return false, r.hardCtx.Err()
		}
		if r.workCtx.Err() != nil && isContextStop(resolveErr) {
			return false, nil
		}
		return false, fmt.Errorf("resolve targets: %w", resolveErr)
	}
	ordered, err := validateResolved(hosts, resolved)
	if err != nil {
		return false, err
	}
	probed := make([]probe.Resolved, 0, len(ordered))
	rows := make([]Row, len(hosts))
	rowTimes := make([]time.Time, len(hosts))
	allProbeResults := make(map[int]validatedProbeResult, len(ordered))
	done := 0
	for i, item := range ordered {
		if item.LookupErr != nil {
			if _, ever := r.resolved[item.Host]; !ever {
				r.unresolved[item.Host] = struct{}{}
			}
			rows[i] = Row{Result: "no such host", TCP: map[int]string{}}
			pending[i] = NoName
			pendingRows[i] = rows[i]
			done++
			continue
		}
		if _, ever := r.resolved[item.Host]; !ever {
			delete(r.unresolved, item.Host)
			r.resolved[item.Host] = struct{}{}
		} else {
			delete(r.unresolved, item.Host)
		}
		probed = append(probed, item)
		rows[i] = Row{Address: item.Address.String(), TCP: make(map[int]string)}
	}
	if canceled && len(probed) > 0 {
		return false, nil
	}
	if r.workCtx.Err() != nil && len(probed) > 0 {
		return false, nil
	}
	if len(probed) == 0 {
		if r.liveDrawEnabled() {
			r.drawLive(r.clock.Now(), LiveProbing, pending, pendingRows, pendingTimes, done, len(hosts), nil)
		}
		return r.commitRound(roundStart, actualStart, rows, rowTimes, nil, lastBusyHeartbeat)
	}
	for offset := 0; offset < len(probed); offset += 100 {
		end := min(offset+100, len(probed))
		batch := probed[offset:end]
		sampleAt := r.clock.Now().Run
		batchResults, batchErr, wasCanceled := awaitWorkLive(r, cancelWork, lastBusyHeartbeat, liveProgress{
			phase: LiveProbing, pending: pending, pendingRows: pendingRows, pendingTime: pendingTimes,
			done: done, total: len(hosts),
		}, func(ctx context.Context) ([]probe.Result, error) {
			return r.deps.Prober.ProbeBatch(ctx, batch, r.plan.Request.Ports)
		})
		if batchErr != nil {
			if r.hardCtx.Err() != nil {
				return false, r.hardCtx.Err()
			}
			if r.workCtx.Err() != nil && isContextStop(batchErr) {
				return false, nil
			}
			return false, fmt.Errorf("probe batch: %w", batchErr)
		}
		batchRows, err := validateProbeResults(batch, batchResults, r.plan.Request.Ports)
		if err != nil {
			return false, err
		}
		for index, row := range batchRows {
			rows[index] = row.row
			rowTimes[index] = sampleAt
			allProbeResults[index] = row
			pending[index] = Observation(row.row, r.plan.Request.Ports)
			pendingRows[index] = row.row
			pendingTimes[index] = sampleAt
		}
		done += len(batch)
		if r.liveDrawEnabled() {
			r.drawLive(r.clock.Now(), LiveProbing, pending, pendingRows, pendingTimes, done, len(hosts), nil)
		}
		finalBatch := end == len(probed)
		if wasCanceled && !finalBatch {
			return false, nil
		}
		if r.workCtx.Err() != nil && !finalBatch {
			return false, nil
		}
		if finalBatch {
			return r.commitRound(roundStart, actualStart, rows, rowTimes, allProbeResults, lastBusyHeartbeat)
		}
	}
	return false, errors.New("probe schedule ended without a final batch")
}

type validatedProbeResult struct {
	row     Row
	result  probe.Result
	address netip.Addr
	index   int
}

func validateResolved(hosts []string, resolved []probe.Resolved) ([]probe.Resolved, error) {
	if len(resolved) != len(hosts) {
		return nil, fmt.Errorf("resolver returned %d entries for %d hosts", len(resolved), len(hosts))
	}
	ordered := make([]probe.Resolved, len(hosts))
	seen := make([]bool, len(hosts))
	for _, item := range resolved {
		if item.Index < 0 || item.Index >= len(hosts) || seen[item.Index] || item.Host != hosts[item.Index] {
			return nil, fmt.Errorf("resolver returned unexpected target index %d for host %q", item.Index, item.Host)
		}
		if item.LookupErr == nil && !item.Address.IsValid() {
			return nil, fmt.Errorf("resolver returned no address for host %q without a lookup error", item.Host)
		}
		ordered[item.Index] = item
		seen[item.Index] = true
	}
	for index, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("resolver omitted target index %d", index)
		}
	}
	return ordered, nil
}

func validateProbeResults(batch []probe.Resolved, results []probe.Result, ports []int) (map[int]validatedProbeResult, error) {
	if len(results) != len(batch) {
		return nil, fmt.Errorf("prober returned %d rows for %d targets", len(results), len(batch))
	}
	expected := make(map[int]probe.Resolved, len(batch))
	for _, target := range batch {
		expected[target.Index] = target
	}
	validated := make(map[int]validatedProbeResult, len(batch))
	for _, result := range results {
		target, ok := expected[result.Target.Index]
		if !ok || result.Target.Host != target.Host || result.Target.Address != target.Address {
			return nil, fmt.Errorf("prober returned unexpected target index %d", result.Target.Index)
		}
		if _, duplicate := validated[result.Target.Index]; duplicate {
			return nil, fmt.Errorf("prober returned duplicate target index %d", result.Target.Index)
		}
		if result.ICMP.State == "" {
			return nil, fmt.Errorf("prober returned an empty ICMP state for host %q", target.Host)
		}
		row := Row{Address: target.Address.String(), Result: result.ICMP.State, TCP: make(map[int]string, len(ports))}
		for _, port := range ports {
			tcpResult, ok := result.TCP[port]
			if !ok {
				return nil, fmt.Errorf("prober omitted TCP port %d for host %q", port, target.Host)
			}
			row.TCP[port] = tcpResult.State
		}
		validated[target.Index] = validatedProbeResult{row: row, result: result, address: target.Address, index: target.Index}
	}
	return validated, nil
}

func (r *runner) commitRound(roundStart, actualStart time.Time, rows []Row, rowTimes []time.Time, latestBatch map[int]validatedProbeResult, lastBusyHeartbeat *time.Duration) (bool, error) {
	observations := make([]byte, len(rows))
	modelRows := make(map[string]Row)
	modelTimes := make(map[string]time.Time)
	input := history.RoundInput{Started: roundStart}
	inputRows := make([]history.ResultRow, 0, len(rows))
	probeFailed := false
	tcpOpen := false
	diagnostic := probeFailureDiagnostic(r.plan.Request.Hosts, r.plan.Request.Ports, latestBatch)
	for index, row := range rows {
		host := r.plan.Request.Hosts[index]
		observation := Observation(row, r.plan.Request.Ports)
		observations[index] = observation
		if ExecutionFailure(row, r.plan.Request.Ports) {
			probeFailed = true
		}
		if result, ok := latestBatch[index]; ok {
			if result.result.ICMP.Err != nil {
				probeFailed = true
			}
			for _, port := range r.plan.Request.Ports {
				tcp := result.result.TCP[port]
				if tcp.Err != nil {
					probeFailed = true
				}
				if tcp.State == "open" {
					tcpOpen = true
				}
			}
		}
		if !isSeen(observation) {
			continue
		}
		when := rowTimes[index]
		if when.IsZero() {
			return false, fmt.Errorf("seen host %q has no batch sample time", host)
		}
		modelRows[host] = row
		modelTimes[host] = when
		tcp := make(map[int]string, len(row.TCP))
		for port, state := range row.TCP {
			tcp[port] = state
		}
		inputRows = append(inputRows, history.ResultRow{Host: host, Address: row.Address, Result: row.Result, Time: when, TCP: tcp})
	}
	input.Observations = string(observations)
	input.Rows = inputRows
	if err := r.recordCompleted(input); err != nil {
		return false, err
	}
	completedReading := r.clock.Now()
	elapsed := completedReading.Run.Sub(actualStart)
	if elapsed < 0 {
		elapsed = 0
	}
	if err := r.heartbeat(&elapsed, true); err != nil && r.hardCtx.Err() == nil {
		return false, err
	}
	if lastBusyHeartbeat != nil {
		*lastBusyHeartbeat = completedReading.Mono
	}
	changes, err := r.tracker.Add(Round{Started: roundStart, Interval: durationValue(r.plan.Interval), Observations: input.Observations, Rows: modelRows, Times: modelTimes})
	if err != nil {
		return false, fmt.Errorf("fold completed round: %w", err)
	}
	r.outcome.CurrentRounds++
	r.outcome.ProbeFailed = r.outcome.ProbeFailed || probeFailed
	r.outcome.TCPOpen = r.outcome.TCPOpen || tcpOpen
	latest := make([]LatestRow, len(rows))
	for index, row := range rows {
		latest[index] = LatestRow{Host: r.plan.Request.Hosts[index], Row: cloneRow(row)}
	}
	r.outcome.Latest = latest
	r.withoutOwn = nil
	if diagnostic != nil && r.deps.Note != nil {
		r.deps.Note(formatProbeDiagnostic(*diagnostic))
	}
	r.liveChange(changes)
	if r.liveDrawEnabled() {
		r.drawLive(completedReading, LiveWaiting, nil, nil, nil, r.outcome.CurrentRounds, len(r.plan.Request.Hosts), nil)
	}
	return true, nil
}

type probeDiagnostic struct {
	host       string
	protocol   string
	cause      error
	additional int
	permission bool
}

func probeFailureDiagnostic(hosts []string, ports []int, results map[int]validatedProbeResult) *probeDiagnostic {
	var diagnostic *probeDiagnostic
	failures := 0
	for index, host := range hosts {
		result, ok := results[index]
		if !ok {
			continue
		}
		if result.result.ICMP.Err != nil || strings.HasPrefix(result.row.Result, "ERROR") {
			failures++
			if diagnostic == nil {
				cause := result.result.ICMP.Err
				diagnostic = &probeDiagnostic{host: host, protocol: "ICMP", cause: cause}
			}
			if result.result.ICMP.Err != nil && isICMPPermissionFailure(result.result.ICMP.Err) {
				diagnostic.permission = true
			}
		}
		for _, port := range ports {
			tcp := result.result.TCP[port]
			if tcp.Err == nil && tcp.State != "error" {
				continue
			}
			failures++
			if diagnostic == nil {
				diagnostic = &probeDiagnostic{host: host, protocol: fmt.Sprintf("TCP port %d", port), cause: tcp.Err}
			}
		}
	}
	if diagnostic != nil {
		diagnostic.additional = failures - 1
	}
	return diagnostic
}

func isICMPPermissionFailure(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.Errno(10013))
}

func formatProbeDiagnostic(diagnostic probeDiagnostic) string {
	cause := "error detail unavailable"
	if diagnostic.cause != nil {
		cause = diagnostic.cause.Error()
	}
	suffix := ""
	if diagnostic.additional > 0 {
		suffix += fmt.Sprintf("; %d additional failing host/protocol attempts", diagnostic.additional)
	}
	if diagnostic.permission {
		switch runtime.GOOS {
		case "linux":
			suffix += "; permission remedy: grant CAP_NET_RAW to the installed binary or use an authorized elevated process"
		case "windows":
			suffix += "; permission remedy: use an authorized elevated Administrator terminal"
		}
	}
	prefix := "probe failure: host " + diagnosticQuote(diagnostic.host, 512) + " " + diagnostic.protocol + ": "
	const maxBytes = 4096
	causeLimit := maxBytes - len(prefix) - len(suffix)
	if causeLimit < 2 {
		causeLimit = 2
	}
	return prefix + diagnosticQuote(cause, causeLimit) + suffix
}

func diagnosticQuote(value string, maxBytes int) string {
	quoted := strconv.Quote(value)
	if len(quoted) <= maxBytes {
		return quoted
	}
	const marker = "…[truncated]"
	var prefix strings.Builder
	for _, character := range value {
		candidate := prefix.String() + string(character) + marker
		if len(strconv.Quote(candidate)) > maxBytes {
			break
		}
		prefix.WriteRune(character)
	}
	return strconv.Quote(prefix.String() + marker)
}

func durationValue(value *time.Duration) time.Duration {
	if value == nil {
		return 0
	}
	return *value
}

func isContextStop(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *runner) waitUntil(cancelWork context.CancelFunc, slot time.Time, lastHeartbeat *time.Duration) (bool, error) {
	return r.waitUntilPhase(cancelWork, slot, lastHeartbeat, nil)
}

func (r *runner) waitUntilLive(cancelWork context.CancelFunc, slot time.Time, lastHeartbeat *time.Duration, phase LivePhase) (bool, error) {
	if r.deps.Live == nil || r.deps.Live.Draw == nil {
		return r.waitUntil(cancelWork, slot, lastHeartbeat)
	}
	return r.waitUntilPhase(cancelWork, slot, lastHeartbeat, &phase)
}

func (r *runner) waitUntilPhase(cancelWork context.CancelFunc, slot time.Time, lastHeartbeat *time.Duration, phase *LivePhase) (bool, error) {
	for {
		if r.hardCtx.Err() != nil {
			return false, r.hardCtx.Err()
		}
		if r.workCtx.Err() != nil {
			return true, nil
		}
		reading := r.clock.Now()
		live := phase != nil && r.liveDrawEnabled()
		if live {
			r.drawLive(reading, *phase, nil, nil, nil, r.outcome.CurrentRounds, len(r.plan.Request.Hosts), &slot)
			reading = r.clock.Now()
			if r.hardCtx.Err() != nil {
				return false, r.hardCtx.Err()
			}
			if r.workCtx.Err() != nil {
				return true, nil
			}
			if lastHeartbeat != nil && reading.Mono-*lastHeartbeat >= 10*time.Second {
				if err := r.heartbeat(nil, false); err != nil {
					cancelWork()
					return false, err
				}
				*lastHeartbeat = reading.Mono
				reading = r.clock.Now()
			}
		}
		if r.until != nil && !reading.Run.Before(*r.until) {
			return false, nil
		}
		remaining := slot.Sub(reading.Run)
		if remaining <= 0 {
			return false, nil
		}
		wait := min(remaining, 500*time.Millisecond)
		timer := r.deps.Clock.After(wait)
		select {
		case <-r.hardCtx.Done():
			return false, r.hardCtx.Err()
		case <-r.workCtx.Done():
			return true, nil
		case <-timer:
			current := r.clock.Now()
			if lastHeartbeat != nil && current.Mono-*lastHeartbeat >= 10*time.Second {
				if err := r.heartbeat(nil, false); err != nil {
					cancelWork()
					return false, err
				}
				*lastHeartbeat = current.Mono
			}
		}
	}
}

func (r *runner) finishOutcome() RunOutcome {
	if r.outcome.CurrentRounds == 0 && r.withoutOwn != nil {
		r.tracker = r.withoutOwn
		r.withoutOwn = nil
	}
	if r.tracker != nil {
		r.outcome.Stats = r.tracker.Stats()
		r.outcome.Seen = r.tracker.Summaries(false)
	}
	r.outcome.Unresolved = make([]string, 0, len(r.unresolved))
	for host := range r.unresolved {
		if _, everResolved := r.resolved[host]; !everResolved {
			r.outcome.Unresolved = append(r.outcome.Unresolved, host)
		}
	}
	sort.Strings(r.outcome.Unresolved)
	return r.outcome
}

func (r *runner) finishUnexpected(err error) (RunOutcome, error) {
	if r.hardCtx.Err() == nil {
		if endErr := r.endHistory("failed"); endErr != nil {
			err = errors.Join(err, endErr)
		}
	}
	return r.finishOutcome(), err
}

// RunExitCode applies target-run failure and success precedence.
func RunExitCode(outcome RunOutcome, invalid bool) int {
	if outcome.ProbeFailed || outcome.HistoryError != nil {
		return 3
	}
	if invalid {
		return 2
	}
	if outcome.CurrentRounds > 0 {
		if outcome.TCPRequired {
			if outcome.TCPOpen {
				return 0
			}
		} else if outcome.Stats.Seen > 0 {
			return 0
		}
	}
	return 1
}
