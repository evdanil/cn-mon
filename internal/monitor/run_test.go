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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/probe"
	"github.com/evdanil/gotest/internal/target"
)

type engineTestClock struct {
	mu            sync.Mutex
	now           time.Time
	mono          time.Duration
	nowCalls      int
	jumpAtNowCall int
	jumpBy        time.Duration
	timers        []engineTestTimer
	added         chan time.Duration
}

type engineTestTimer struct {
	deadline time.Duration
	channel  chan time.Time
}

func (c *engineTestClock) Now() ClockReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowCalls++
	if c.nowCalls == c.jumpAtNowCall && c.jumpBy > 0 {
		c.now = c.now.Add(c.jumpBy)
		c.mono += c.jumpBy
	}
	reading := ClockReading{Wall: c.now, Mono: c.mono}
	return reading
}

func (c *engineTestClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	deadline := c.mono + delay
	if deadline <= c.mono {
		ch <- c.now
	} else {
		c.timers = append(c.timers, engineTestTimer{deadline: deadline, channel: ch})
	}
	if c.added != nil {
		select {
		case c.added <- delay:
		default:
		}
	}
	return ch
}

func (c *engineTestClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mono += delay
	kept := c.timers[:0]
	for _, timer := range c.timers {
		if timer.deadline <= c.mono {
			timer.channel <- c.now
		} else {
			kept = append(kept, timer)
		}
	}
	c.timers = kept
	c.mu.Unlock()
}

type engineTestProber struct {
	resolved     []probe.Resolved
	results      []probe.Result
	err          error
	resolveFn    func(context.Context, []string) ([]probe.Resolved, error)
	probeFn      func(context.Context, []probe.Resolved, []int) ([]probe.Result, error)
	resolveCalls int
	batchCalls   int
}

func (p *engineTestProber) Resolve(ctx context.Context, hosts []string) ([]probe.Resolved, error) {
	p.resolveCalls++
	if p.resolveFn != nil {
		return p.resolveFn(ctx, hosts)
	}
	if p.err != nil {
		return nil, p.err
	}
	return append([]probe.Resolved(nil), p.resolved...), nil
}

func (p *engineTestProber) ProbeBatch(ctx context.Context, targets []probe.Resolved, ports []int) ([]probe.Result, error) {
	p.batchCalls++
	if p.probeFn != nil {
		return p.probeFn(ctx, targets, ports)
	}
	if p.err != nil {
		return nil, p.err
	}
	return append([]probe.Result(nil), p.results...), nil
}

type engineTestHistory struct {
	runStarted  history.RunStart
	rounds      []history.RoundInput
	heartbeats  []time.Time
	elapsed     []*time.Duration
	clock       *engineTestClock
	commitTime  time.Duration
	pruneFn     func(context.Context) error
	endFn       func(context.Context) error
	heartbeatFn func(context.Context, time.Time, *time.Duration) error
	recorded    chan time.Time
	closeErr    error
	closeCalls  int
	beginCalls  int
	events      *[]string
	ended       string
	closed      bool
}

func (h *engineTestHistory) Close() error {
	h.closeCalls++
	h.closed = true
	return h.closeErr
}

func (h *engineTestHistory) BeginRun(_ context.Context, _ history.Request, run history.RunStart) (int64, error) {
	h.beginCalls++
	h.runStarted = run
	return 1, nil
}

func (h *engineTestHistory) RecordRound(_ context.Context, _ string, round history.RoundInput) (int64, error) {
	h.rounds = append(h.rounds, round)
	if h.recorded != nil {
		h.recorded <- round.Started
	}
	if h.clock != nil && h.commitTime > 0 {
		h.clock.Advance(h.commitTime)
	}
	return int64(len(h.rounds)), nil
}

func (h *engineTestHistory) Heartbeat(ctx context.Context, _ string, at time.Time, elapsed *time.Duration) error {
	h.heartbeats = append(h.heartbeats, at)
	if elapsed == nil {
		h.elapsed = append(h.elapsed, nil)
	} else {
		copyElapsed := *elapsed
		h.elapsed = append(h.elapsed, &copyElapsed)
	}
	if h.heartbeatFn != nil {
		return h.heartbeatFn(ctx, at, elapsed)
	}
	return nil
}

func (h *engineTestHistory) EndRun(ctx context.Context, _ string, _ time.Time, status string) error {
	h.ended = status
	if h.events != nil {
		*h.events = append(*h.events, "end")
	}
	if h.endFn != nil {
		return h.endFn(ctx)
	}
	return nil
}

func (h *engineTestHistory) Prune(ctx context.Context, _ time.Time, _ *big.Int) (int64, error) {
	if h.pruneFn != nil {
		return 0, h.pruneFn(ctx)
	}
	return 0, nil
}

func (*engineTestHistory) View(context.Context, func(*history.Snapshot) error) error {
	return nil
}

func TestRunOneShotCommitsAndFoldsCompletedProbe(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clock := &engineTestClock{now: start}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	store := &engineTestHistory{}
	plan := RunPlan{
		Request:     request,
		OwnRunOnly:  true,
		HistoryFile: "history.db",
		HistoryDays: big.NewInt(30),
		RunID:       "run-1",
	}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock:       clock,
		Prober:      prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.CurrentRounds != 1 || outcome.Stats.Rounds != 1 || outcome.Stats.Seen != 1 {
		t.Fatalf("run outcome stats = %+v, current rounds=%d", outcome.Stats, outcome.CurrentRounds)
	}
	if len(outcome.Latest) != 1 || outcome.Latest[0].Host != "192.0.2.1" || outcome.Latest[0].Row.Result != "OK" {
		t.Fatalf("latest rows = %+v", outcome.Latest)
	}
	if prober.resolveCalls != 1 || prober.batchCalls != 1 {
		t.Fatalf("probe calls resolve=%d batch=%d", prober.resolveCalls, prober.batchCalls)
	}
	if len(store.rounds) != 1 || store.rounds[0].Observations != "!" {
		t.Fatalf("stored rounds = %+v", store.rounds)
	}
	if code := RunExitCode(outcome, false); code != 0 {
		t.Fatalf("RunExitCode = %d, want 0", code)
	}
}

func TestValidateRunAcceptsTargetBuildNumericSubnetOrder(t *testing.T) {
	request, rejected, err := target.Build([]string{"10.0.0.0/28"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build rejected subnet: rejected=%+v err=%v", rejected, err)
	}
	if len(request.Hosts) < 10 || request.Hosts[8] != "10.0.0.9" || request.Hosts[9] != "10.0.0.10" {
		t.Fatalf("target.Build host order = %v", request.Hosts)
	}
	plan := RunPlan{Request: request, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-subnet"}
	err = validateRun(context.Background(), plan, RunDeps{Prober: &engineTestProber{}, OpenHistory: func(context.Context, string) (History, error) { return nil, nil }})
	if err != nil {
		t.Fatalf("validateRun rejected target.Build's numeric subnet order: %v", err)
	}
}

func TestRunContinuesInMemoryWhenHistoryOpenFails(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clock := &engineTestClock{now: start}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-open-error"}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: clock, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return nil, errors.New("open failed") },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.HistoryError == nil || outcome.CurrentRounds != 1 || outcome.Stats.Rounds != 1 || outcome.Stats.Seen != 1 {
		t.Fatalf("outcome after history open failure = %+v", outcome)
	}
	if len(outcome.Latest) != 1 || outcome.Latest[0].Row.Result != "OK" {
		t.Fatalf("latest after history open failure = %+v", outcome.Latest)
	}
	if code := RunExitCode(outcome, false); code != 3 {
		t.Fatalf("RunExitCode = %d, want 3", code)
	}
}

func TestRunReportsOnlyNeverResolvedNames(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"unresolved.example"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	prober := &engineTestProber{resolved: []probe.Resolved{{Index: 0, Host: "unresolved.example", LookupErr: errors.New("no such host")}}}
	store := &engineTestHistory{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-unresolved"}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.CurrentRounds != 1 || outcome.Stats.Rounds != 1 || outcome.Stats.Seen != 0 {
		t.Fatalf("unresolved run stats = %+v, current rounds=%d", outcome.Stats, outcome.CurrentRounds)
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0] != "unresolved.example" {
		t.Fatalf("unresolved names = %v", outcome.Unresolved)
	}
	if prober.batchCalls != 0 || len(store.rounds) != 1 || store.rounds[0].Observations != "?" {
		t.Fatalf("batch calls=%d stored rounds=%+v", prober.batchCalls, store.rounds)
	}
}

func TestRunUsesOriginForOneShotAndMeasuresCommitTime(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clock := &engineTestClock{now: start}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	store := &engineTestHistory{clock: clock, commitTime: 2 * time.Second}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-delayed-setup"}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: clock, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) {
			clock.Advance(5 * time.Second)
			return store, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.CurrentRounds != 1 || len(store.rounds) != 1 {
		t.Fatalf("outcome rounds=%d stored=%d", outcome.CurrentRounds, len(store.rounds))
	}
	if !store.rounds[0].Started.Equal(start) || !store.rounds[0].Rows[0].Time.Equal(start.Add(5*time.Second)) {
		t.Fatalf("stored round start=%s rows=%+v", store.rounds[0].Started, store.rounds[0].Rows)
	}
	if len(store.heartbeats) != 1 || !store.heartbeats[0].Equal(start.Add(7*time.Second)) {
		t.Fatalf("completed heartbeat timestamps=%v, want [%s]", store.heartbeats, start.Add(7*time.Second))
	}
	if len(store.elapsed) != 1 || store.elapsed[0] == nil || *store.elapsed[0] != 2*time.Second {
		t.Fatalf("completed elapsed values=%v, want [2s]", store.elapsed)
	}
}

func TestRunDoesNotStartOneShotAfterSetupPassesDuration(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clock := &engineTestClock{now: start}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	store := &engineTestHistory{}
	duration := 10 * time.Second
	plan := RunPlan{Request: request, Duration: &duration, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-setup-overrun"}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: clock, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) {
			clock.Advance(11 * time.Second)
			return store, nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.CurrentRounds != 0 || outcome.Stats.Rounds != 0 || len(store.rounds) != 0 {
		t.Fatalf("run began after duration end: outcome=%+v stored=%d", outcome, len(store.rounds))
	}
	if prober.resolveCalls != 0 || prober.batchCalls != 0 {
		t.Fatalf("probe work began after duration end: resolve=%d batch=%d", prober.resolveCalls, prober.batchCalls)
	}
}

func TestRunChecksActualStartUsingSingleReadingAtDurationBoundary(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	clock := &engineTestClock{now: start, jumpAtNowCall: 6, jumpBy: 10 * time.Second}
	address := netip.MustParseAddr("192.0.2.1")
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: address}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	store := &engineTestHistory{}
	interval, duration := time.Second, 10*time.Second
	plan := RunPlan{Request: request, Interval: &interval, Duration: &duration, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-forward-end"}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: clock, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.CurrentRounds != 1 || len(store.rounds) != 1 {
		t.Fatalf("run count=%d stored=%d", outcome.CurrentRounds, len(store.rounds))
	}
	if !store.rounds[0].Started.Before(start.Add(duration)) {
		t.Fatalf("round started at/past end despite only one start reading: %s >= %s", store.rounds[0].Started, start.Add(duration))
	}
}

func TestRunWarnsOnlyWhenRepeatEstimateExceedsInterval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hosts    int
		interval *time.Duration
		want     bool
	}{
		{name: "one shot", hosts: 101},
		{name: "estimate equals interval", hosts: 200, interval: durationPointer(6 * time.Second)},
		{name: "interval exceeds estimate", hosts: 101, interval: durationPointer(10 * time.Second)},
		{name: "one batch estimate exceeds interval", hosts: 100, interval: durationPointer(2 * time.Second), want: true},
		{name: "multiple batch estimate exceeds interval", hosts: 201, interval: durationPointer(8 * time.Second), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := make([]string, tc.hosts)
			for i := range inputs {
				inputs[i] = fmt.Sprintf("198.51.100.%d", i+1)
			}
			request, rejected, err := target.Build(inputs, nil)
			if err != nil || len(rejected) != 0 {
				t.Fatalf("target.Build rejected=%d err=%v", len(rejected), err)
			}
			store := &engineTestHistory{}
			var notes []string
			plan := RunPlan{Request: request, Interval: tc.interval, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-estimate"}
			_, _ = Run(context.Background(), plan, RunDeps{
				Clock:       &engineTestClock{now: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
				Prober:      &engineTestProber{err: errors.New("stop after warning")},
				OpenHistory: func(context.Context, string) (History, error) { return store, nil },
				Note:        func(message string) { notes = append(notes, message) },
			})
			warned := len(notes) != 0
			if warned != tc.want {
				t.Fatalf("warning = %v, notes=%q, want %v", warned, notes, tc.want)
			}
			if warned && !strings.Contains(notes[0], "estimate") {
				t.Fatalf("warning lacks estimate wording: %q", notes[0])
			}
		})
	}
}

func durationPointer(value time.Duration) *time.Duration { return &value }

func TestRunRoundNeverResolvesNamesThatResolvedEarlier(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"transient.example"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	lookupFailure := errors.New("no such host")
	address := netip.MustParseAddr("192.0.2.7")
	resolveCall := 0
	prober := &engineTestProber{
		resolveFn: func(_ context.Context, _ []string) ([]probe.Resolved, error) {
			resolveCall++
			switch resolveCall {
			case 1, 3:
				return []probe.Resolved{{Index: 0, Host: "transient.example", LookupErr: lookupFailure}}, nil
			case 2:
				return []probe.Resolved{{Index: 0, Host: "transient.example", Address: address}}, nil
			default:
				return nil, errors.New("unexpected resolve")
			}
		},
		probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
			return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}}, nil
		},
	}
	ctx := context.Background()
	clock := &engineTestClock{now: start}
	r := &runner{
		plan: RunPlan{Request: request}, deps: RunDeps{Clock: clock, Prober: prober},
		hardCtx: ctx, workCtx: ctx, clock: newRunClock(clock), origin: start,
		tracker:  NewTracker(request.Hosts, request.Ports, 0),
		resolved: make(map[string]struct{}), unresolved: make(map[string]struct{}),
	}
	cancel := func() {}
	for i := 0; i < 3; i++ {
		when := start.Add(time.Duration(i) * time.Second)
		completed, err := r.runRound(cancel, when, when)
		if err != nil || !completed {
			t.Fatalf("round %d completed=%v err=%v", i, completed, err)
		}
	}
	outcome := r.finishOutcome()
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("ever-resolved name remained unresolved: %v", outcome.Unresolved)
	}
	if outcome.CurrentRounds != 3 || outcome.Stats.Rounds != 3 || outcome.Stats.Seen != 1 {
		t.Fatalf("transition outcome stats=%+v current=%d", outcome.Stats, outcome.CurrentRounds)
	}
}

func TestRunUnexpectedResolverErrorUsesWithoutOwnSpacingFallback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := history.OpenWriter(ctx, path)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build rejected request: %+v %v", rejected, err)
	}
	origin := time.Unix(160, 0).UTC()
	oldID := "prior-one-round"
	if _, err := store.BeginRun(ctx, historyRequest(request), history.RunStart{
		ID: oldID, Started: origin.Add(-time.Hour), Heartbeat: origin, Host: "test", PID: 1,
	}); err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	for _, seconds := range []int64{0, 10, 30, 70, 130} {
		started := origin.Add(time.Duration(seconds-160) * time.Second)
		if _, err := store.RecordRound(ctx, oldID, history.RoundInput{
			Started: started, Observations: "!",
			Rows: []history.ResultRow{{Host: request.Hosts[0], Address: "192.0.2.1", Result: "OK", Time: started, TCP: map[int]string{}}},
		}); err != nil {
			t.Fatalf("RecordRound(%d): %v", seconds, err)
		}
	}
	prober := &engineTestProber{err: errors.New("resolver protocol failure")}
	plan := RunPlan{Request: request, HistoryFile: path, HistoryDays: big.NewInt(30), RunID: "current-one-round"}
	outcome, runErr := Run(ctx, plan, RunDeps{
		Clock: &engineTestClock{now: origin}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "resolver protocol failure") {
		t.Fatalf("Run error = %v, want resolver protocol failure", runErr)
	}
	if outcome.CurrentRounds != 0 || len(outcome.Seen) != 1 || len(outcome.Seen[0].Periods) != 2 {
		t.Fatalf("unexpected-error summary used prospective origin: current=%d summaries=%+v", outcome.CurrentRounds, outcome.Seen)
	}
}

type engineBlockingResolveProber struct {
	started chan struct{}
	finish  chan struct{}
	partial []probe.Resolved
}

func (p *engineBlockingResolveProber) Resolve(ctx context.Context, _ []string) ([]probe.Resolved, error) {
	close(p.started)
	<-ctx.Done()
	<-p.finish
	return p.partial, ctx.Err()
}

func (*engineBlockingResolveProber) ProbeBatch(context.Context, []probe.Resolved, []int) ([]probe.Result, error) {
	return nil, errors.New("ProbeBatch must not run after canceled Resolve")
}

func TestRunCanceledResolveAddsNoPartialLookupFacts(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"partial.example", "other.example"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	stop := make(chan struct{})
	prober := &engineBlockingResolveProber{
		started: make(chan struct{}), finish: make(chan struct{}),
		partial: []probe.Resolved{{Index: 0, Host: "partial.example", LookupErr: errors.New("no such host")}},
	}
	store := &engineTestHistory{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-canceled-resolve"}
	type runResult struct {
		outcome RunOutcome
		err     error
	}
	result := make(chan runResult, 1)
	go func() {
		outcome, err := Run(context.Background(), plan, RunDeps{
			Clock: &engineTestClock{now: start}, Prober: prober, Stop: stop,
			OpenHistory: func(context.Context, string) (History, error) { return store, nil },
		})
		result <- runResult{outcome: outcome, err: err}
	}()
	select {
	case <-prober.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve did not start")
	}
	close(stop)
	close(prober.finish)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Run: %v", got.err)
		}
		if got.outcome.CurrentRounds != 0 || len(got.outcome.Unresolved) != 0 || got.outcome.Stats.Rounds != 0 {
			t.Fatalf("canceled Resolve contributed partial facts: %+v", got.outcome)
		}
		if !got.outcome.Stopped || store.ended != "stopped" {
			t.Fatalf("stop status: outcome stopped=%v history=%q", got.outcome.Stopped, store.ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not join canceled Resolve")
	}
}

func TestRunRoundRetainsSuccessfulFinalBatchRacingStop(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	address := netip.MustParseAddr("192.0.2.1")
	targetRow := probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}
	probeStarted, releaseProbe := make(chan struct{}), make(chan struct{})
	prober := &engineTestProber{
		resolved: []probe.Resolved{targetRow},
		probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
			close(probeStarted)
			<-releaseProbe
			return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2, Err: errors.New("independent execution failure")}}}, nil
		},
	}
	workCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &engineTestClock{now: start}
	r := &runner{
		plan: RunPlan{Request: request}, deps: RunDeps{Clock: clock, Prober: prober},
		hardCtx: context.Background(), workCtx: workCtx, clock: newRunClock(clock), origin: start,
		tracker:  NewTracker(request.Hosts, request.Ports, 0),
		resolved: make(map[string]struct{}), unresolved: make(map[string]struct{}),
	}
	type roundResult struct {
		completed bool
		err       error
	}
	finished := make(chan roundResult, 1)
	go func() {
		completed, err := r.runRound(cancel, start, start)
		finished <- roundResult{completed: completed, err: err}
	}()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("ProbeBatch did not start")
	}
	cancel()
	close(releaseProbe)
	select {
	case got := <-finished:
		if got.err != nil || !got.completed {
			t.Fatalf("final round completed=%v err=%v", got.completed, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runRound did not join final ProbeBatch")
	}
	if r.outcome.CurrentRounds != 1 || r.tracker.Stats().Rounds != 1 || r.tracker.Stats().Seen != 1 {
		t.Fatalf("successful final batch was discarded: outcome=%+v stats=%+v", r.outcome, r.tracker.Stats())
	}
	if !r.outcome.ProbeFailed {
		t.Fatal("completed structured execution failure was not retained")
	}
}

func TestRunRoundDiscardsSuccessfulNonfinalBatchRacingStop(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	inputs := make([]string, 101)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("198.51.100.%d", i+1)
	}
	request, rejected, err := target.Build(inputs, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request rejected=%d err=%v", len(rejected), err)
	}
	resolved := make([]probe.Resolved, len(request.Hosts))
	for i, host := range request.Hosts {
		resolved[i] = probe.Resolved{Index: i, Host: host, Address: netip.MustParseAddr(host)}
	}
	probeStarted, releaseProbe := make(chan struct{}), make(chan struct{})
	prober := &engineTestProber{
		resolved: resolved,
		probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
			close(probeStarted)
			<-releaseProbe
			results := make([]probe.Result, len(targets))
			for i, item := range targets {
				results[i] = probe.Result{Target: item, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2, Err: errors.New("provisional execution failure")}}
			}
			return results, nil
		},
	}
	workCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &engineTestClock{now: start}
	r := &runner{
		plan: RunPlan{Request: request}, deps: RunDeps{Clock: clock, Prober: prober},
		hardCtx: context.Background(), workCtx: workCtx, clock: newRunClock(clock), origin: start,
		tracker:  NewTracker(request.Hosts, request.Ports, 0),
		resolved: make(map[string]struct{}), unresolved: make(map[string]struct{}),
	}
	finished := make(chan bool, 1)
	go func() {
		completed, err := r.runRound(cancel, start, start)
		if err != nil {
			finished <- false
			return
		}
		finished <- completed
	}()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first ProbeBatch did not start")
	}
	cancel()
	close(releaseProbe)
	select {
	case completed := <-finished:
		if completed {
			t.Fatal("nonfinal batch completed a round after stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runRound did not join nonfinal ProbeBatch")
	}
	if r.outcome.CurrentRounds != 0 || r.tracker.Stats().Rounds != 0 || len(r.outcome.Latest) != 0 || r.outcome.ProbeFailed {
		t.Fatalf("nonfinal batch changed committed state: outcome=%+v stats=%+v", r.outcome, r.tracker.Stats())
	}
	if len(r.resolved) != len(request.Hosts) {
		t.Fatalf("successful Resolve facts lost on canceled probe: got %d resolved hosts want %d", len(r.resolved), len(request.Hosts))
	}
}

func TestRunExitCodeUsesCurrentResultAndFailurePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome RunOutcome
		invalid bool
		want    int
	}{
		{name: "prior seen is not current success", outcome: RunOutcome{Stats: Stats{Seen: 2}}, want: 1},
		{name: "prior tcp open is not current success", outcome: RunOutcome{TCPOpen: true, TCPRequired: true}, want: 1},
		{name: "current icmp seen succeeds", outcome: RunOutcome{CurrentRounds: 1, Stats: Stats{Seen: 1}}, want: 0},
		{name: "requested tcp requires open", outcome: RunOutcome{CurrentRounds: 1, TCPRequired: true}, want: 1},
		{name: "requested tcp open succeeds", outcome: RunOutcome{CurrentRounds: 1, TCPRequired: true, TCPOpen: true}, want: 0},
		{name: "probe failure precedes invalid input", outcome: RunOutcome{ProbeFailed: true}, invalid: true, want: 3},
		{name: "history failure precedes invalid input", outcome: RunOutcome{HistoryError: errors.New("history")}, invalid: true, want: 3},
		{name: "invalid input precedes no success", invalid: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RunExitCode(tc.outcome, tc.invalid); got != tc.want {
				t.Fatalf("RunExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRunHardCancellationDuringStartupClosesStoreAndReportsCloseError(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	hardCtx, cancelHard := context.WithCancel(context.Background())
	defer cancelHard()
	closeErr := errors.New("store close failed")
	store := &engineTestHistory{closeErr: closeErr}
	store.pruneFn = func(ctx context.Context) error {
		cancelHard()
		<-ctx.Done()
		return ctx.Err()
	}
	prober := &engineTestProber{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-hard-startup"}
	outcome, runErr := Run(hardCtx, plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if !errors.Is(runErr, context.Canceled) || !errors.Is(runErr, closeErr) {
		t.Fatalf("Run error = %v, want cancellation and close error", runErr)
	}
	if store.closeCalls != 1 || store.beginCalls != 0 || store.ended != "" {
		t.Fatalf("startup cleanup: close=%d begin=%d end=%q", store.closeCalls, store.beginCalls, store.ended)
	}
	if outcome.HistoryError == nil || !errors.Is(outcome.HistoryError, closeErr) {
		t.Fatalf("outcome did not expose close failure: %+v", outcome)
	}
}

type engineBlockingProbeProber struct {
	started  chan struct{}
	finished chan struct{}
	targets  []probe.Resolved
}

func (p *engineBlockingProbeProber) Resolve(_ context.Context, _ []string) ([]probe.Resolved, error) {
	return append([]probe.Resolved(nil), p.targets...), nil
}

func (p *engineBlockingProbeProber) ProbeBatch(ctx context.Context, _ []probe.Resolved, _ []int) ([]probe.Result, error) {
	close(p.started)
	<-ctx.Done()
	close(p.finished)
	return nil, ctx.Err()
}

func TestRunHardCancellationDuringProbeJoinsAndClosesStore(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	targetRow := probe.Resolved{Index: 0, Host: "192.0.2.1", Address: netip.MustParseAddr("192.0.2.1")}
	prober := &engineBlockingProbeProber{started: make(chan struct{}), finished: make(chan struct{}), targets: []probe.Resolved{targetRow}}
	store := &engineTestHistory{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-hard-probe"}
	hardCtx, cancelHard := context.WithCancel(context.Background())
	var notes []string
	type runResult struct {
		outcome RunOutcome
		err     error
	}
	result := make(chan runResult, 1)
	go func() {
		outcome, err := Run(hardCtx, plan, RunDeps{
			Clock: &engineTestClock{now: start}, Prober: prober,
			OpenHistory: func(context.Context, string) (History, error) { return store, nil },
			Note:        func(message string) { notes = append(notes, message) },
		})
		result <- runResult{outcome: outcome, err: err}
	}()
	select {
	case <-prober.started:
	case <-time.After(2 * time.Second):
		cancelHard()
		t.Fatal("ProbeBatch did not start")
	}
	cancelHard()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Run error = %v, want cancellation", got.err)
		}
		select {
		case <-prober.finished:
		default:
			t.Fatal("Run returned before joining ProbeBatch")
		}
		if store.closeCalls != 1 || store.ended != "" {
			t.Fatalf("probe cleanup: close=%d end=%q", store.closeCalls, store.ended)
		}
		if got.outcome.CurrentRounds != 0 {
			t.Fatalf("canceled probe became a completed round: %+v", got.outcome)
		}
		for _, note := range notes {
			if note == "stopping; preparing the final summary" {
				t.Fatalf("hard abort emitted graceful stop note: %q", notes)
			}
		}
	case <-time.After(2 * time.Second):
		cancelHard()
		t.Fatal("Run did not return after joining ProbeBatch")
	}
}

func TestRunHardCancellationDuringFinalizationKeepsCompletedRoundAndClosesOnce(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	address := netip.MustParseAddr("192.0.2.1")
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: address}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	hardCtx, cancelHard := context.WithCancel(context.Background())
	defer cancelHard()
	closeErr := errors.New("final close failed")
	store := &engineTestHistory{closeErr: closeErr}
	store.endFn = func(ctx context.Context) error {
		cancelHard()
		<-ctx.Done()
		return ctx.Err()
	}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-hard-finalize"}
	outcome, runErr := Run(hardCtx, plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if !errors.Is(runErr, context.Canceled) || !errors.Is(runErr, closeErr) {
		t.Fatalf("Run error = %v, want hard cancellation and close error", runErr)
	}
	if outcome.CurrentRounds != 1 || outcome.Stats.Rounds != 1 || outcome.Stats.Seen != 1 {
		t.Fatalf("completed round lost during finalization: %+v", outcome)
	}
	if store.beginCalls != 1 || store.ended != "finished" || store.closeCalls != 1 {
		t.Fatalf("finalization calls: begin=%d status=%q close=%d", store.beginCalls, store.ended, store.closeCalls)
	}
}

func TestRunOrdinaryFinalCloseFailureDegradesWithoutRunError(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	address := netip.MustParseAddr("192.0.2.1")
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: address}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	store := &engineTestHistory{closeErr: errors.New("close failed")}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-close-failure"}
	outcome, runErr := Run(context.Background(), plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
	})
	if runErr != nil {
		t.Fatalf("Run returned unexpected error for ordinary close failure: %v", runErr)
	}
	if outcome.HistoryError == nil || !errors.Is(outcome.HistoryError, store.closeErr) || outcome.CurrentRounds != 1 {
		t.Fatalf("ordinary close degradation outcome = %+v", outcome)
	}
	if store.closeCalls != 1 || RunExitCode(outcome, false) != 3 {
		t.Fatalf("close calls=%d exit=%d", store.closeCalls, RunExitCode(outcome, false))
	}
}

func TestRunGracefulStopNotesBeforeFinalizingHistory(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"stop.example"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	stop := make(chan struct{})
	prober := &engineBlockingResolveProber{started: make(chan struct{}), finish: make(chan struct{})}
	var events []string
	store := &engineTestHistory{events: &events}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-graceful-note"}
	notes := []string{}
	type runResult struct {
		outcome RunOutcome
		err     error
	}
	result := make(chan runResult, 1)
	go func() {
		outcome, err := Run(context.Background(), plan, RunDeps{
			Clock: &engineTestClock{now: start}, Prober: prober, Stop: stop,
			OpenHistory: func(context.Context, string) (History, error) { return store, nil },
			Note: func(message string) {
				notes = append(notes, message)
				events = append(events, "note")
			},
		})
		result <- runResult{outcome: outcome, err: err}
	}()
	select {
	case <-prober.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve did not start")
	}
	close(stop)
	close(prober.finish)
	select {
	case got := <-result:
		if got.err != nil || !got.outcome.Stopped {
			t.Fatalf("graceful outcome stopped=%v err=%v", got.outcome.Stopped, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish after graceful stop")
	}
	if len(notes) != 1 || notes[0] != "stopping; preparing the final summary" {
		t.Fatalf("stop notes = %q", notes)
	}
	if len(events) != 2 || events[0] != "note" || events[1] != "end" {
		t.Fatalf("finalization order = %v, want [note end]", events)
	}
}

func TestRunDoesNotEmitStopNoteForNormalCompletion(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	address := netip.MustParseAddr("192.0.2.1")
	prober := &engineTestProber{
		resolved: []probe.Resolved{{Index: 0, Host: "192.0.2.1", Address: address}},
		results:  []probe.Result{{Target: probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}},
	}
	store := &engineTestHistory{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-no-stop-note"}
	var notes []string
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
		Note:        func(message string) { notes = append(notes, message) },
	})
	if err != nil || outcome.Stopped {
		t.Fatalf("normal outcome stopped=%v err=%v", outcome.Stopped, err)
	}
	if len(notes) != 0 {
		t.Fatalf("normal run emitted stop note %q", notes)
	}
}

func TestRunObservesStopClosedBySuccessfulFinalProbe(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	stop := make(chan struct{})
	resolved := probe.Resolved{Index: 0, Host: request.Hosts[0], Address: netip.MustParseAddr("192.0.2.1")}
	prober := &engineTestProber{
		resolved: []probe.Resolved{resolved},
		probeFn: func(context.Context, []probe.Resolved, []int) ([]probe.Result, error) {
			close(stop)
			return []probe.Result{{Target: resolved, ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}}, nil
		},
	}
	store := &engineTestHistory{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-stop-on-final-probe"}
	var notes []string
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober, Stop: stop,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
		Note:        func(message string) { notes = append(notes, message) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome.CurrentRounds != 1 || outcome.Stats.Rounds != 1 || len(store.rounds) != 1 {
		t.Fatalf("completed final round was lost: outcome=%+v stored=%d", outcome, len(store.rounds))
	}
	if !outcome.Stopped || store.ended != "stopped" {
		t.Fatalf("closed Stop status: stopped=%v history=%q", outcome.Stopped, store.ended)
	}
	if len(notes) != 1 || notes[0] != "stopping; preparing the final summary" {
		t.Fatalf("stop notes = %q", notes)
	}
}

func TestRunHeartbeatDeadlineSpansResolveAndFinalProbe(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request = %+v, rejected=%+v, err=%v", request, rejected, err)
	}
	clock := &engineTestClock{now: start, added: make(chan time.Duration, 8)}
	address := netip.MustParseAddr("192.0.2.1")
	targetRow := probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}
	probeReady, releaseProbe := make(chan struct{}), make(chan struct{})
	prober := &engineTestProber{
		resolveFn: func(_ context.Context, _ []string) ([]probe.Resolved, error) {
			if delay := <-clock.added; delay != 10*time.Second {
				t.Errorf("Resolve heartbeat timer = %s, want initial shared 10s deadline", delay)
			}
			clock.Advance(6 * time.Second)
			return []probe.Resolved{targetRow}, nil
		},
		probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
			if delay := <-clock.added; delay != 4*time.Second {
				t.Errorf("ProbeBatch heartbeat timer = %s, want remaining 4s shared deadline", delay)
			}
			clock.Advance(6 * time.Second)
			close(probeReady)
			<-releaseProbe
			return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}}, nil
		},
	}
	busyHeartbeat := make(chan struct{})
	store := &engineTestHistory{clock: clock}
	store.heartbeatFn = func(ctx context.Context, _ time.Time, elapsed *time.Duration) error {
		if elapsed == nil {
			close(busyHeartbeat)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	stop := make(chan struct{})
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-heartbeat-boundary"}
	type runResult struct {
		outcome RunOutcome
		err     error
	}
	result := make(chan runResult, 1)
	go func() {
		outcome, err := Run(context.Background(), plan, RunDeps{
			Clock: clock, Prober: prober, Stop: stop,
			OpenHistory: func(context.Context, string) (History, error) { return store, nil },
		})
		result <- runResult{outcome: outcome, err: err}
	}()
	select {
	case <-probeReady:
	case <-time.After(2 * time.Second):
		t.Fatal("final ProbeBatch did not start")
	}
	select {
	case <-busyHeartbeat:
	case <-time.After(2 * time.Second):
		close(releaseProbe)
		close(stop)
		t.Fatal("no busy heartbeat across Resolve→ProbeBatch elapsed time")
	}
	close(releaseProbe)
	close(stop)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("Run: %v", got.err)
		}
		if got.outcome.CurrentRounds != 1 || got.outcome.Stats.Rounds != 1 || got.outcome.Stats.Seen != 1 {
			t.Fatalf("successful final probe lost to canceled busy heartbeat: %+v", got.outcome)
		}
		if !got.outcome.Stopped || len(store.rounds) != 1 {
			t.Fatalf("final outcome stopped=%v stored rounds=%d", got.outcome.Stopped, len(store.rounds))
		}
		if len(store.elapsed) != 2 || store.elapsed[0] != nil || store.elapsed[1] == nil {
			t.Fatalf("heartbeat sequence = %v, want busy then completed", store.elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish after stop released busy heartbeat")
	}
}

func TestRunHeartbeatDeadlineSpansWaitIntoNextResolve(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build rejected=%v err=%v", rejected, err)
	}
	clock := &engineTestClock{now: start, added: make(chan time.Duration, 256)}
	address := netip.MustParseAddr("192.0.2.1")
	row := probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}
	var resolveCalls int
	secondResolve := make(chan struct{})
	prober := &engineTestProber{
		resolveFn: func(ctx context.Context, _ []string) ([]probe.Resolved, error) {
			resolveCalls++
			if resolveCalls == 2 {
				close(secondResolve)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []probe.Resolved{row}, nil
		},
		probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
			return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 1, Received: 1}}}, nil
		},
	}
	store := &engineTestHistory{recorded: make(chan time.Time, 2)}
	interval := 19 * time.Second
	plan := RunPlan{Request: request, Interval: &interval, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-wait-heartbeat"}
	stop := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, runErr := Run(context.Background(), plan, RunDeps{
			Clock: clock, Prober: prober, Stop: stop,
			OpenHistory: func(context.Context, string) (History, error) { return store, nil },
		})
		result <- runErr
	}()
	select {
	case <-store.recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("first round was not committed")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-clock.added:
		case <-time.After(2 * time.Second):
			t.Fatal("initial Resolve/ProbeBatch timers were not registered")
		}
	}
	deadline := time.After(2 * time.Second)
	var resolveHeartbeatDelay time.Duration
	var advances int
driveWait:
	for {
		select {
		case <-secondResolve:
			break driveWait
		case delay := <-clock.added:
			if delay > 500*time.Millisecond {
				resolveHeartbeatDelay = delay
				continue
			}
			if delay <= 0 || delay > 500*time.Millisecond {
				continue
			}
			clock.Advance(delay)
			advances++
		case <-deadline:
			select {
			case runErr := <-result:
				t.Fatalf("Run returned before waiting: %v", runErr)
			default:
				t.Fatalf("run did not reach the next Resolve after %d advances (queued %d)", advances, len(clock.added))
			}
		}
	}
	if resolveHeartbeatDelay == 0 {
		select {
		case resolveHeartbeatDelay = <-clock.added:
		case <-time.After(2 * time.Second):
			t.Fatal("next Resolve did not schedule a heartbeat")
		}
	}
	close(stop)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if resolveHeartbeatDelay != time.Second {
		t.Fatalf("next Resolve heartbeat timer = %s, want 1s after shared +10s heartbeat at slot +19s", resolveHeartbeatDelay)
	}
}

func TestRunRepeatingSlotsAlignAfterExactAndOverrunningRounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		interval      time.Duration
		duration      time.Duration
		firstWork     time.Duration
		wantOffsets   []time.Duration
		wantSkipped   int64
		wantWaitSteps int
	}{
		{name: "exact completion", interval: 10 * time.Second, duration: 35 * time.Second, firstWork: 10 * time.Second, wantOffsets: []time.Duration{0, 10 * time.Second, 20 * time.Second, 30 * time.Second}, wantWaitSteps: 40},
		{name: "overrun skips missed slots and excludes end", interval: 10 * time.Second, duration: 35 * time.Second, firstWork: 25 * time.Second, wantOffsets: []time.Duration{0, 30 * time.Second}, wantSkipped: 2, wantWaitSteps: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			clock := &engineTestClock{now: start, added: make(chan time.Duration, 512)}
			request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
			if err != nil || len(rejected) != 0 {
				t.Fatalf("target.Build rejected=%v err=%v", rejected, err)
			}
			address := netip.MustParseAddr("192.0.2.1")
			row := probe.Resolved{Index: 0, Host: "192.0.2.1", Address: address}
			var batches int
			prober := &engineTestProber{
				resolved: []probe.Resolved{row},
				probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
					batches++
					if batches == 1 && tc.firstWork > 0 {
						clock.Advance(tc.firstWork)
					}
					return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 1, Received: 1}}}, nil
				},
			}
			store := &engineTestHistory{recorded: make(chan time.Time, 8)}
			interval, duration := tc.interval, tc.duration
			plan := RunPlan{Request: request, Interval: &interval, Duration: &duration, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-aligned-slots"}
			type result struct {
				outcome RunOutcome
				err     error
			}
			finished := make(chan result, 1)
			go func() {
				outcome, runErr := Run(context.Background(), plan, RunDeps{
					Clock: clock, Prober: prober,
					OpenHistory: func(context.Context, string) (History, error) { return store, nil },
				})
				finished <- result{outcome: outcome, err: runErr}
			}()
			var starts []time.Time
			waitSteps := 0
			deadline := time.After(3 * time.Second)
			var got result
			for {
				select {
				case at := <-store.recorded:
					starts = append(starts, at)
				case delay := <-clock.added:
					if delay == 500*time.Millisecond {
						clock.Advance(delay)
						waitSteps++
					}
				case got = <-finished:
					for {
						select {
						case at := <-store.recorded:
							starts = append(starts, at)
						default:
							goto complete
						}
					}
				case <-deadline:
					t.Fatalf("Run did not finish; completed starts=%v", starts)
				}
			}
		complete:
			if got.err != nil {
				t.Fatalf("Run: %v", got.err)
			}
			if got.outcome.CurrentRounds != len(tc.wantOffsets) || int64(got.outcome.SkippedSlots) != tc.wantSkipped {
				clock.mu.Lock()
				mono := clock.mono
				clock.mu.Unlock()
				t.Fatalf("outcome rounds=%d skipped=%d, want rounds=%d skipped=%d; starts=%v wait steps=%d fake mono=%s", got.outcome.CurrentRounds, got.outcome.SkippedSlots, len(tc.wantOffsets), tc.wantSkipped, starts, waitSteps, mono)
			}
			if len(starts) != len(tc.wantOffsets) {
				t.Fatalf("round starts=%v, want %d rounds", starts, len(tc.wantOffsets))
			}
			for i, offset := range tc.wantOffsets {
				want := start.Add(offset)
				if !starts[i].Equal(want) {
					t.Errorf("round %d started %s, want aligned slot %s", i+1, starts[i], want)
				}
			}
			if waitSteps != tc.wantWaitSteps {
				t.Fatalf("advanced %d wait slices, want %d slices of at most 500ms", waitSteps, tc.wantWaitSteps)
			}
		})
	}
}

func TestRunRechecksAlignedSlotAfterForwardJumpDuringWait(t *testing.T) {
	for _, tc := range []struct {
		name          string
		duration      time.Duration
		initialWork   time.Duration
		noteAdvance   time.Duration
		jumpAt        time.Duration
		jumpBy        time.Duration
		advanceUntil  time.Duration
		wantOffsets   []time.Duration
		wantSkipped   int64
		wantWaitSteps int
		wantNotes     []string
	}{
		{name: "resumed at +25 selects +30 and skips both covered slots", duration: 35 * time.Second, jumpAt: 10 * time.Second, jumpBy: 15 * time.Second, advanceUntil: 30 * time.Second, wantOffsets: []time.Duration{0, 30 * time.Second}, wantSkipped: 2, wantWaitSteps: 30, wantNotes: []string{"skipped 2 scheduled round slot(s)"}},
		{name: "ordinary timer jitter retains selected slot", duration: 11 * time.Second, jumpAt: 10 * time.Second, jumpBy: 100 * time.Millisecond, advanceUntil: 10100 * time.Millisecond, wantOffsets: []time.Duration{0, 10100 * time.Millisecond}, wantWaitSteps: 20},
		{name: "wake at exact following slot", duration: 25 * time.Second, jumpAt: 10 * time.Second, jumpBy: 10 * time.Second, advanceUntil: 20 * time.Second, wantOffsets: []time.Duration{0, 20 * time.Second}, wantSkipped: 1, wantWaitSteps: 20, wantNotes: []string{"skipped 1 scheduled round slot(s)"}},
		{name: "skip note crosses exclusive cutoff before launch", duration: 25 * time.Second, noteAdvance: 20 * time.Second, jumpAt: 10 * time.Second, jumpBy: 10 * time.Second, advanceUntil: 20 * time.Second, wantOffsets: []time.Duration{0}, wantSkipped: 2, wantWaitSteps: 20, wantNotes: []string{"skipped 1 scheduled round slot(s)", "skipped 1 scheduled round slot(s)"}},
		{name: "cutoff counts only earlier missed slot", duration: 15 * time.Second, jumpAt: 10 * time.Second, jumpBy: 6 * time.Second, advanceUntil: 16 * time.Second, wantOffsets: []time.Duration{0}, wantSkipped: 1, wantWaitSteps: 20, wantNotes: []string{"skipped 1 scheduled round slot(s)"}},
		{name: "reselection does not recount previous overrun", duration: 45 * time.Second, initialWork: 25 * time.Second, jumpAt: 30 * time.Second, jumpBy: 10 * time.Second, advanceUntil: 40 * time.Second, wantOffsets: []time.Duration{0, 40 * time.Second}, wantSkipped: 3, wantWaitSteps: 10, wantNotes: []string{"skipped 2 scheduled round slot(s)", "skipped 1 scheduled round slot(s)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runForwardWaitScenario(t, tc.duration, tc.initialWork, tc.noteAdvance, tc.jumpAt, tc.jumpBy, tc.advanceUntil, tc.wantOffsets, tc.wantSkipped, tc.wantWaitSteps, tc.wantNotes)
		})
	}
}

func runForwardWaitScenario(t *testing.T, duration, initialWork, noteAdvance, jumpAt, jumpBy, advanceUntil time.Duration, wantOffsets []time.Duration, wantSkipped int64, wantWaitSteps int, wantNotes []string) {
	t.Helper()
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	clock := &engineTestClock{now: start, added: make(chan time.Duration, 512)}
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build rejected=%v err=%v", rejected, err)
	}
	row := probe.Resolved{Index: 0, Host: request.Hosts[0], Address: netip.MustParseAddr("192.0.2.1")}
	var batches int
	prober := &engineTestProber{
		resolved: []probe.Resolved{row},
		probeFn: func(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
			batches++
			if batches == 1 && initialWork > 0 {
				clock.Advance(initialWork)
			}
			return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 1, Received: 1}}}, nil
		},
	}
	store := &engineTestHistory{recorded: make(chan time.Time, 8)}
	interval := 10 * time.Second
	plan := RunPlan{Request: request, Interval: &interval, Duration: &duration, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-wait-forward-jump"}
	var notes []string
	noteAdvanced := false
	type result struct {
		outcome RunOutcome
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		outcome, runErr := Run(context.Background(), plan, RunDeps{
			Clock: clock, Prober: prober,
			OpenHistory: func(context.Context, string) (History, error) { return store, nil },
			Note: func(message string) {
				notes = append(notes, message)
				if noteAdvance > 0 && !noteAdvanced {
					clock.Advance(noteAdvance)
					noteAdvanced = true
				}
			},
		})
		finished <- result{outcome: outcome, err: runErr}
	}()
	var starts []time.Time
	waitSteps := 0
	jumped := false
	deadline := time.After(3 * time.Second)
	var got result
	for {
		select {
		case at := <-store.recorded:
			starts = append(starts, at)
		case delay := <-clock.added:
			if delay != 500*time.Millisecond {
				continue
			}
			clock.mu.Lock()
			mono := clock.mono
			clock.mu.Unlock()
			if !jumped && mono < jumpAt {
				clock.Advance(delay)
				waitSteps++
				clock.mu.Lock()
				mono = clock.mono
				clock.mu.Unlock()
				if mono == jumpAt {
					clock.Advance(jumpBy)
					jumped = true
				}
			} else if mono < advanceUntil {
				clock.Advance(delay)
				waitSteps++
			}
		case got = <-finished:
			for {
				select {
				case at := <-store.recorded:
					starts = append(starts, at)
				default:
					goto complete
				}
			}
		case <-deadline:
			t.Fatalf("Run did not finish; starts=%v wait steps=%d", starts, waitSteps)
		}
	}
complete:
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}
	wantStarts := make([]time.Time, len(wantOffsets))
	for index, offset := range wantOffsets {
		wantStarts[index] = start.Add(offset)
	}
	if len(starts) != len(wantStarts) {
		t.Fatalf("round starts=%v, want %v", starts, wantStarts)
	}
	for i := range wantStarts {
		if !starts[i].Equal(wantStarts[i]) {
			t.Errorf("round %d started %s, want %s", i+1, starts[i], wantStarts[i])
		}
		if !starts[i].Before(start.Add(duration)) {
			t.Errorf("round %d started at/past exclusive end: %s >= %s", i+1, starts[i], start.Add(duration))
		}
	}
	if got.outcome.CurrentRounds != len(wantOffsets) || got.outcome.SkippedSlots != wantSkipped || batches != len(wantOffsets) {
		t.Fatalf("outcome rounds=%d skipped=%d batches=%d, want %d/%d/%d", got.outcome.CurrentRounds, got.outcome.SkippedSlots, batches, len(wantOffsets), wantSkipped, len(wantOffsets))
	}
	if len(notes) != len(wantNotes) {
		t.Fatalf("skip notes=%q, want %q", notes, wantNotes)
	}
	for i := range wantNotes {
		if notes[i] != wantNotes[i] {
			t.Errorf("skip note %d=%q, want %q", i+1, notes[i], wantNotes[i])
		}
	}
	if jumped != (jumpBy > 0) || waitSteps != wantWaitSteps {
		t.Fatalf("jumped=%v wait steps=%d, want jumped=%v steps=%d", jumped, waitSteps, jumpBy > 0, wantWaitSteps)
	}
}

func TestRunNotesCompletedProbeFailuresOnceAfterPublication(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"beta.example", "alpha.example"}, []int{443, 80})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build rejected=%v err=%v", rejected, err)
	}
	if got := strings.Join(request.Hosts, ","); got != "alpha.example,beta.example" {
		t.Fatalf("canonical hosts = %q", got)
	}
	resolved := make([]probe.Resolved, len(request.Hosts))
	results := make([]probe.Result, len(request.Hosts))
	for index, host := range request.Hosts {
		address := netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", index+1))
		resolved[index] = probe.Resolved{Index: index, Host: host, Address: address}
		results[index] = probe.Result{Target: resolved[index], TCP: map[int]probe.TCPResult{
			80:  {State: "closed"},
			443: {State: "timeout"},
		}}
	}
	results[0].ICMP = probe.ICMPResult{State: "OK", Sent: 2, Received: 1, Err: errors.New("ICMP execution detail\nsecond line")}
	results[0].TCP[443] = probe.TCPResult{State: "error", Err: errors.New("tcp execution error")}
	results[1].ICMP = probe.ICMPResult{State: "ERROR: send failed", Err: fmt.Errorf("socket denied: %w", os.ErrPermission)}
	results[1].TCP[80] = probe.TCPResult{State: "open", Err: errors.New("masked TCP execution error")}
	prober := &engineTestProber{resolved: resolved, results: results}
	store := &engineTestHistory{}
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-probe-note"}
	var notes []string
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return store, nil },
		Note: func(message string) {
			if len(store.rounds) != 1 {
				t.Errorf("probe note emitted before completed round persistence: rounds=%d", len(store.rounds))
			}
			notes = append(notes, message)
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !outcome.ProbeFailed || outcome.CurrentRounds != 1 || len(store.rounds) != 1 {
		t.Fatalf("completed failure outcome=%+v stored=%d", outcome, len(store.rounds))
	}
	if len(notes) != 1 {
		t.Fatalf("probe diagnostic notes=%q, want exactly one", notes)
	}
	note := notes[0]
	for _, fragment := range []string{`host "alpha.example"`, "ICMP", "ICMP execution detail", `\n`, "3 additional failing host/protocol attempts"} {
		if !strings.Contains(note, fragment) {
			t.Errorf("diagnostic %q does not contain %q", note, fragment)
		}
	}
	if strings.ContainsAny(note, "\r\n") || len(note) > 4096 {
		t.Fatalf("diagnostic is not one line/within size bound: bytes=%d %q", len(note), note)
	}
	if !strings.Contains(note, "CAP_NET_RAW") && !strings.Contains(note, "Administrator") {
		t.Fatalf("permission remedy missing: %q", note)
	}
}

func TestRunDoesNotNoteReachabilityResults(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request, rejected, err := target.Build([]string{"192.0.2.1"}, []int{443})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build rejected=%v err=%v", rejected, err)
	}
	address := netip.MustParseAddr("192.0.2.1")
	resolved := probe.Resolved{Index: 0, Host: request.Hosts[0], Address: address}
	prober := &engineTestProber{
		resolved: []probe.Resolved{resolved},
		results: []probe.Result{{Target: resolved, ICMP: probe.ICMPResult{State: "NO RESPONSE"}, TCP: map[int]probe.TCPResult{
			443: {State: "closed"},
		}}},
	}
	var notes []string
	plan := RunPlan{Request: request, OwnRunOnly: true, HistoryFile: "history.db", HistoryDays: big.NewInt(30), RunID: "run-no-probe-note"}
	outcome, err := Run(context.Background(), plan, RunDeps{
		Clock: &engineTestClock{now: start}, Prober: prober,
		OpenHistory: func(context.Context, string) (History, error) { return &engineTestHistory{}, nil },
		Note:        func(message string) { notes = append(notes, message) },
	})
	if err != nil || outcome.ProbeFailed {
		t.Fatalf("ordinary reachability outcome=%+v err=%v", outcome, err)
	}
	if len(notes) != 0 {
		t.Fatalf("ordinary no-response/closed outcomes emitted notes: %q", notes)
	}
}

func TestProbeDiagnosticTruncatesFieldsAndKeepsRequiredSuffix(t *testing.T) {
	diagnostic := probeDiagnostic{
		host: "host" + strings.Repeat("h", 2048), protocol: "TCP port 443",
		cause: errors.New(strings.Repeat("💥", 8192)), additional: 2, permission: true,
	}
	message := formatProbeDiagnostic(diagnostic)
	if len(message) > 4096 || strings.ContainsAny(message, "\r\n") {
		t.Fatalf("diagnostic length/line = %d %q", len(message), message)
	}
	for _, fragment := range []string{"TCP port 443", "[truncated]", "2 additional failing host/protocol attempts"} {
		if !strings.Contains(message, fragment) {
			t.Errorf("truncated diagnostic lost %q: %q", fragment, message)
		}
	}
	if !strings.Contains(message, "CAP_NET_RAW") && !strings.Contains(message, "Administrator") {
		t.Fatalf("truncated diagnostic lost permission remedy: %q", message)
	}
}
