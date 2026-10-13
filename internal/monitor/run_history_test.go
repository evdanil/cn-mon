package monitor

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestSpacingMatchesExpectedSpacingForGeneratedSamples(t *testing.T) {
	cases := []struct {
		name   string
		starts []float64
		since  *time.Time
		before *time.Time
		extra  *float64
	}{
		{name: "ties-and-zero-gaps", starts: []float64{0, 1, 1, 3, 4}, since: runHistoryTime(0), before: runHistoryTime(100)},
		{name: "all-zero-gaps", starts: []float64{0, 0, 0}, since: runHistoryTime(0), before: runHistoryTime(100)},
		{name: "too-few", starts: []float64{0, 10}, since: runHistoryTime(0), before: runHistoryTime(100)},
		{name: "filtered-window", starts: []float64{0, 1, 1, 3, 4}, since: runHistoryTime(1), before: runHistoryTime(4)},
		{name: "extra-start", starts: []float64{0, 10}, since: runHistoryTime(0), before: runHistoryTime(100), extra: runHistoryFloat(20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "spacing.db"))
			if err != nil {
				t.Fatalf("OpenWriter: %v", err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})

			request := history.Request{
				RequestMeta: history.RequestMeta{
					Key:      strings.Repeat("a", 64),
					Targets:  []string{"spacing.example"},
					TCPPorts: []int{},
				},
				Hosts: []string{"spacing.example"},
			}
			origin := time.Unix(0, 0).UTC()
			requestID, err := store.BeginRun(ctx, request, history.RunStart{
				ID: "spacing-run", Started: origin, Heartbeat: origin, Host: "test",
			})
			if err != nil {
				t.Fatalf("BeginRun: %v", err)
			}
			for _, seconds := range tc.starts {
				started := time.Unix(int64(seconds), int64((seconds-float64(int64(seconds)))*float64(time.Second))).UTC()
				if _, err := store.RecordRound(ctx, "spacing-run", history.RoundInput{Started: started, Observations: "?"}); err != nil {
					t.Fatalf("RecordRound(%v): %v", seconds, err)
				}
			}

			var starts []time.Time
			for _, seconds := range tc.starts {
				if tc.since != nil && seconds < float64(tc.since.Unix()) {
					continue
				}
				if tc.before != nil && seconds >= float64(tc.before.Unix()) {
					continue
				}
				starts = append(starts, time.Unix(int64(seconds), 0).UTC())
			}
			if tc.extra != nil {
				starts = append(starts, time.Unix(int64(*tc.extra), 0).UTC())
			}
			want := ExpectedSpacing(starts)
			err = store.View(ctx, func(snapshot *history.Snapshot) error {
				var extra *time.Time
				if tc.extra != nil {
					value := time.Unix(int64(*tc.extra), 0).UTC()
					extra = &value
				}
				got, err := snapshot.Spacing(requestID, history.WindowFilter{Since: tc.since, Before: tc.before}, extra)
				if err != nil {
					return err
				}
				if got != want {
					t.Errorf("Spacing = %s, ExpectedSpacing = %s", got, want)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("View: %v", err)
			}
		})
	}
}

func runHistoryTime(seconds int64) *time.Time {
	value := time.Unix(seconds, 0).UTC()
	return &value
}

func runHistoryFloat(value float64) *float64 { return &value }

type bridgeFixedClock struct{ reading ClockReading }

func (c bridgeFixedClock) Now() ClockReading                          { return c.reading }
func (c bridgeFixedClock) After(delay time.Duration) <-chan time.Time { return time.After(delay) }

type bridgeSequenceClock struct {
	readings []ClockReading
	index    int
}

func (c *bridgeSequenceClock) Now() ClockReading {
	index := c.index
	if index >= len(c.readings) {
		index = len(c.readings) - 1
	}
	c.index++
	return c.readings[index]
}

func (c *bridgeSequenceClock) After(delay time.Duration) <-chan time.Time { return time.After(delay) }

type bridgeViewError struct {
	History
	err error
}

func (h bridgeViewError) View(ctx context.Context, callback func(*history.Snapshot) error) error {
	if err := h.History.View(ctx, callback); err != nil {
		return err
	}
	return h.err
}

type bridgePruneCapture struct {
	History
	pruneAt time.Time
	views   int
}

func (h *bridgePruneCapture) Prune(ctx context.Context, at time.Time, days *big.Int) (int64, error) {
	h.pruneAt = at
	return h.History.Prune(ctx, at, days)
}

func (h *bridgePruneCapture) View(ctx context.Context, callback func(*history.Snapshot) error) error {
	h.views++
	return h.History.View(ctx, callback)
}

func bridgeRequest(key string) history.Request {
	return history.Request{
		RequestMeta: history.RequestMeta{Key: key, Targets: []string{"node.example"}, TCPPorts: []int{443}},
		Hosts:       []string{"node.example"},
	}
}

func bridgeRunner(store History, request history.Request, origin time.Time) *runner {
	ctx := context.Background()
	reading := ClockReading{Wall: origin, Mono: 0}
	return &runner{
		plan:    RunPlan{Request: targetRequest(request), RunID: "bridge-current-run", HistoryFile: "ignored-by-test-opener", Hostname: "bridge-test", HistoryDays: bigOne()},
		deps:    RunDeps{OpenHistory: func(context.Context, string) (History, error) { return store, nil }},
		hardCtx: ctx, workCtx: ctx, clock: newRunClock(bridgeFixedClock{reading: reading}), origin: origin, store: store,
	}
}

func TestOpenHistoryRecordsRoundAndFinalizesAgainstOneStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bridge-lifecycle.db")
	store, err := history.OpenWriter(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	request := bridgeRequest(strings.Repeat("d", 64))
	start := time.Unix(100, 0).UTC()
	if _, err := store.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: start.Add(-20 * time.Second), Heartbeat: start, Host: "bridge-test"}); err != nil {
		t.Fatal(err)
	}
	old := start.Add(-10 * time.Second)
	if _, err := store.RecordRound(ctx, "bridge-old", history.RoundInput{Started: old, Observations: "."}); err != nil {
		t.Fatal(err)
	}
	r := bridgeRunner(store, request, start)
	if err := r.openHistory(); err != nil {
		t.Fatalf("openHistory: %v", err)
	}
	if !r.runRecorded || r.store != store || r.outcome.EarlierRounds != 1 {
		t.Fatalf("startup state: runRecorded=%t storePresent=%t earlier=%d", r.runRecorded, r.store != nil, r.outcome.EarlierRounds)
	}
	input := history.RoundInput{Started: start, Observations: "!", Rows: []history.ResultRow{{Host: "node.example", Address: "192.0.2.9", Result: "OK", Time: start, TCP: map[int]string{443: "open"}}}}
	if err := r.recordCompleted(input); err != nil {
		t.Fatalf("recordCompleted: %v", err)
	}
	elapsed := 7 * time.Second
	if err := r.heartbeat(&elapsed, true); err != nil {
		t.Fatalf("completed heartbeat: %v", err)
	}
	if err := r.endHistory("stopped"); err != nil {
		t.Fatalf("endHistory: %v", err)
	}
	if r.store != nil {
		t.Fatal("endHistory retained its store")
	}
	readStore, err := history.OpenReader(ctx, path)
	if err != nil || readStore == nil {
		t.Fatalf("OpenReader: store=%v err=%v", readStore, err)
	}
	defer readStore.Close()
	err = readStore.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Rounds != 2 || info.NewestRun == nil || info.NewestRun.ID != r.plan.RunID || info.NewestRun.Status != "stopped" || info.NewestRun.RoundSeconds == nil || *info.NewestRun.RoundSeconds != elapsed {
			t.Errorf("persisted startup/current/final state = %+v", info)
		}
		page, err := snapshot.Rounds(info.ID, history.RoundFilter{Limit: 10})
		if err != nil {
			return err
		}
		if len(page.Items) != 2 || page.Items[1].Observations != "!" {
			t.Errorf("persisted rounds = %+v", page.Items)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read persisted state: %v", err)
	}
}

func TestOwnRunOnlyKeepsOriginAsPruneAnchorAndSkipsSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bridge-own-only.db")
	base, err := history.OpenWriter(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	request := bridgeRequest(strings.Repeat("f", 64))
	origin := time.Unix(500000, 0).UTC()
	oldStart := origin.Add(-200000 * time.Second)
	if _, err := base.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: oldStart, Heartbeat: oldStart, Host: "bridge-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := base.RecordRound(ctx, "bridge-old", history.RoundInput{Started: oldStart, Observations: "."}); err != nil {
		t.Fatal(err)
	}
	captured := &bridgePruneCapture{History: base}
	r := bridgeRunner(captured, request, origin)
	r.plan.OwnRunOnly = true
	clock := &bridgeSequenceClock{readings: []ClockReading{
		{Wall: origin, Mono: 0},
		{Wall: origin.Add(25 * time.Second), Mono: 25 * time.Second},
	}}
	r.clock = newRunClock(clock)
	r.origin = r.clock.Now().Run
	if err := r.openHistory(); err != nil {
		t.Fatalf("openHistory own-run-only: %v", err)
	}
	if !captured.pruneAt.Equal(origin) {
		t.Fatalf("Prune anchor = %s, want captured origin %s", captured.pruneAt, origin)
	}
	if captured.views != 0 {
		t.Fatalf("OwnRunOnly performed %d history View calls", captured.views)
	}
	if r.outcome.EarlierRounds != 0 || r.outcome.Latest == nil || len(r.outcome.Latest) != 0 || r.outcome.TCPOpen {
		t.Fatalf("OwnRunOnly published earlier history: %+v", r.outcome)
	}
	if err := r.endHistory("finished"); err != nil {
		t.Fatalf("endHistory: %v", err)
	}
	reader, err := history.OpenReader(ctx, path)
	if err != nil || reader == nil {
		t.Fatalf("OpenReader after OwnRunOnly: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	if err := reader.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Rounds != 0 || info.NewestRun == nil || !info.NewestRun.Started.Equal(origin) || !info.NewestRun.Heartbeat.Equal(origin.Add(25*time.Second)) {
			t.Errorf("OwnRunOnly persisted request/run after prune: %+v", info)
		}
		return nil
	}); err != nil {
		t.Fatalf("read OwnRunOnly history: %v", err)
	}
}

func TestReadEarlierOwnRunOnlySkipsHistoricalFold(t *testing.T) {
	ctx := context.Background()
	store, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge-own-only-view.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	request := bridgeRequest(strings.Repeat("1", 64))
	start := time.Unix(0, 0).UTC()
	if _, err := store.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: start, Heartbeat: start, Host: "bridge-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordRound(ctx, "bridge-old", history.RoundInput{Started: start, Observations: "."}); err != nil {
		t.Fatal(err)
	}
	r := bridgeRunner(store, request, start.Add(time.Minute))
	r.plan.OwnRunOnly = true
	if err := r.readEarlier(); err != nil {
		t.Fatalf("readEarlier OwnRunOnly: %v", err)
	}
	if r.outcome.EarlierRounds != 0 || r.tracker.Stats().Rounds != 0 || r.outcome.Latest == nil || len(r.outcome.Latest) != 0 {
		t.Fatalf("OwnRunOnly imported history: earlier=%d stats=%+v latest=%+v", r.outcome.EarlierRounds, r.tracker.Stats(), r.outcome.Latest)
	}
}

type bridgeFaultHistory struct {
	History
	recordErr, heartbeatErr, endErr, closeErr error
	closeCalls                                int
	endCalls                                  int
}

func (h *bridgeFaultHistory) RecordRound(ctx context.Context, id string, round history.RoundInput) (int64, error) {
	if h.recordErr != nil {
		return 0, h.recordErr
	}
	return h.History.RecordRound(ctx, id, round)
}

func (h *bridgeFaultHistory) Heartbeat(ctx context.Context, id string, at time.Time, elapsed *time.Duration) error {
	if h.heartbeatErr != nil {
		return h.heartbeatErr
	}
	return h.History.Heartbeat(ctx, id, at, elapsed)
}

func (h *bridgeFaultHistory) EndRun(ctx context.Context, id string, at time.Time, status string) error {
	h.endCalls++
	if h.endErr != nil {
		return h.endErr
	}
	return h.History.EndRun(ctx, id, at, status)
}

func TestCompletedHistoryCancellationIsNotMistakenForGracefulWorkStop(t *testing.T) {
	for _, operation := range []string{"record", "heartbeat", "end"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			base, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge-hard-op-cancel.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = base.Close() })
			request := bridgeRequest(strings.Repeat("9", 64))
			start := time.Unix(900, 0).UTC()
			if _, err := base.BeginRun(ctx, request, history.RunStart{ID: "bridge-hard-op-cancel", Started: start, Heartbeat: start, Host: "bridge-test"}); err != nil {
				t.Fatal(err)
			}
			store := &bridgeFaultHistory{History: base, recordErr: context.Canceled, heartbeatErr: context.Canceled, endErr: context.Canceled}
			hardCtx, cancelHard := context.WithCancel(context.Background())
			defer cancelHard()
			workCtx, cancelWork := context.WithCancel(hardCtx)
			cancelWork()
			r := bridgeRunner(store, request, start.Add(time.Second))
			r.hardCtx, r.workCtx, r.runRecorded = hardCtx, workCtx, true

			var opErr error
			switch operation {
			case "record":
				opErr = r.recordCompleted(history.RoundInput{Started: start.Add(time.Second), Observations: "."})
			case "heartbeat":
				opErr = r.heartbeat(nil, true)
			case "end":
				opErr = r.endHistory("finished")
			}
			if opErr != nil {
				t.Fatalf("hard-context operation returned error instead of recording a history failure: %v", opErr)
			}
			if r.outcome.HistoryError == nil || !errors.Is(r.outcome.HistoryError, context.Canceled) || r.store != nil || store.closeCalls != 1 {
				t.Fatalf("hard-context cancellation was suppressed as graceful: historyErr=%v store=%v closeCalls=%d", r.outcome.HistoryError, r.store != nil, store.closeCalls)
			}
			if operation == "end" && store.endCalls != 1 {
				t.Fatalf("EndRun calls = %d, want 1 while hard context is active", store.endCalls)
			}
		})
	}
}

func TestEndHistoryClosesAfterHardCancellationWithoutCallingEndRun(t *testing.T) {
	ctx := context.Background()
	base, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge-end-hard-cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	request := bridgeRequest(strings.Repeat("8", 64))
	start := time.Unix(800, 0).UTC()
	if _, err := base.BeginRun(ctx, request, history.RunStart{ID: "bridge-end-hard-cancel", Started: start, Heartbeat: start, Host: "bridge-test"}); err != nil {
		t.Fatal(err)
	}
	closeFailure := errors.New("close after hard cancellation")
	store := &bridgeFaultHistory{History: base, closeErr: closeFailure}
	hardCtx, cancelHard := context.WithCancel(context.Background())
	cancelHard()
	r := bridgeRunner(store, request, start.Add(time.Second))
	r.hardCtx, r.workCtx, r.runRecorded = hardCtx, hardCtx, true

	err = r.endHistory("failed")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeFailure) {
		t.Fatalf("endHistory error = %v, want hard cancellation and close failure", err)
	}
	if store.endCalls != 0 || store.closeCalls != 1 || r.store != nil {
		t.Fatalf("hard-canceled finalization: EndRun calls=%d close calls=%d attached=%t", store.endCalls, store.closeCalls, r.store != nil)
	}
}

func (h *bridgeFaultHistory) Close() error {
	h.closeCalls++
	closeErr := h.History.Close()
	return errors.Join(closeErr, h.closeErr)
}

func TestHistoryFailureDetachesOnceAndKeepsInMemoryRoundPath(t *testing.T) {
	ctx := context.Background()
	base, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge-write-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	request := bridgeRequest(strings.Repeat("e", 64))
	start := time.Unix(300, 0).UTC()
	if _, err := base.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: start, Heartbeat: start, Host: "bridge-test"}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected record failure")
	closeFailure := errors.New("injected close failure")
	store := &bridgeFaultHistory{History: base, recordErr: failure, closeErr: closeFailure}
	notes := 0
	r := bridgeRunner(store, request, start.Add(time.Second))
	r.deps.Note = func(string) { notes++ }
	r.runRecorded = true
	if err := r.recordCompleted(history.RoundInput{Started: start.Add(time.Second), Observations: "."}); err != nil {
		t.Fatalf("ordinary storage failure escaped instead of using memory path: %v", err)
	}
	if r.store != nil || store.closeCalls != 1 || r.outcome.HistoryError == nil || !errors.Is(r.outcome.HistoryError, failure) || !errors.Is(r.outcome.HistoryError, closeFailure) || notes != 1 {
		t.Fatalf("failed store state: store=%v closeCalls=%d historyErr=%v notes=%d", r.store, store.closeCalls, r.outcome.HistoryError, notes)
	}
	if err := r.recordCompleted(history.RoundInput{Started: start.Add(2 * time.Second), Observations: "."}); err != nil || store.closeCalls != 1 || notes != 1 {
		t.Fatalf("detached store was retried: err=%v closeCalls=%d notes=%d", err, store.closeCalls, notes)
	}
}

func TestGracefulHistoryCancellationRequiresWorkStopAndActiveHardContext(t *testing.T) {
	hard, cancelHard := context.WithCancel(context.Background())
	work, cancelWork := context.WithCancel(hard)
	cancelWork()
	if !gracefulHistoryCancellation(work, hard, context.Canceled) {
		t.Fatal("active hard context with canceled work context was not classified graceful")
	}
	cancelHard()
	if gracefulHistoryCancellation(work, hard, context.Canceled) {
		t.Fatal("hard cancellation was misclassified as graceful stop")
	}
	activeHard := context.Background()
	activeWork, stop := context.WithCancel(activeHard)
	stop()
	withCleanup := errors.Join(context.Canceled, errors.New("cleanup"))
	if gracefulHistoryCancellation(activeWork, activeHard, withCleanup) {
		t.Fatal("graceful stop hid an independent cleanup failure")
	}
}

func TestStoredLatestReconstructsFullCanonicalObservationVector(t *testing.T) {
	request := target.Request{Hosts: []string{
		"alpha.example", "beta.example", "delta.example", "epsilon.example", "gamma.example", "theta.example", "zeta.example",
	}, Ports: []int{80, 443}}
	stored := history.StoredRound{RoundInput: history.RoundInput{Observations: "!RTU.E?", Rows: []history.ResultRow{
		{Host: "delta.example", Address: "192.0.2.3", Result: "TCP seen", TCP: map[int]string{80: "closed", 443: "error"}},
		{Host: "alpha.example", Address: "192.0.2.1", Result: "custom OK text", TCP: map[int]string{443: "open"}},
		{Host: "beta.example", Address: "192.0.2.2", Result: "REJECTED", TCP: map[int]string{}},
	}}}
	got, err := storedLatest(request, stored)
	if err != nil {
		t.Fatal(err)
	}
	emptyTCP := map[int]string{80: "", 443: ""}
	want := []LatestRow{
		{Host: "alpha.example", Row: Row{Address: "192.0.2.1", Result: "custom OK text", TCP: map[int]string{443: "open"}}},
		{Host: "beta.example", Row: Row{Address: "192.0.2.2", Result: "REJECTED", TCP: map[int]string{}}},
		{Host: "delta.example", Row: Row{Address: "192.0.2.3", Result: "TCP seen", TCP: map[int]string{80: "closed", 443: "error"}}},
		{Host: "epsilon.example", Row: Row{Result: "UNREACHABLE", TCP: cloneTCPStates(emptyTCP)}},
		{Host: "gamma.example", Row: Row{Result: "NO RESPONSE", TCP: cloneTCPStates(emptyTCP)}},
		{Host: "theta.example", Row: Row{Result: "ERROR", TCP: cloneTCPStates(emptyTCP)}},
		{Host: "zeta.example", Row: Row{Result: "no such host", TCP: cloneTCPStates(emptyTCP)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storedLatest = %+v, want %+v", got, want)
	}
}

func targetRequest(request history.Request) target.Request {
	return target.Request{Tokens: append([]string(nil), request.Targets...), Ports: append([]int(nil), request.TCPPorts...), Hosts: append([]string(nil), request.Hosts...), Key: request.Key}
}

func bigOne() *big.Int { return big.NewInt(1) }

func TestReadEarlierPublishesFoldedHistoryAndOneRoundFallback(t *testing.T) {
	ctx := context.Background()
	store, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	request := bridgeRequest(strings.Repeat("b", 64))
	start := time.Unix(0, 0).UTC()
	if _, err := store.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: start, Heartbeat: start, Host: "test"}); err != nil {
		t.Fatal(err)
	}
	for i, second := range []int64{0, 10, 30} {
		at := time.Unix(second, 0).UTC()
		input := history.RoundInput{Started: at, Observations: "!", Rows: []history.ResultRow{{Host: "node.example", Address: "192.0.2.1", Result: "OK", Time: at, TCP: map[int]string{443: "open"}}}}
		if _, err := store.RecordRound(ctx, "bridge-old", input); err != nil {
			t.Fatalf("RecordRound %d: %v", i, err)
		}
	}
	r := bridgeRunner(store, request, time.Unix(45, 0).UTC())
	if err := r.readEarlier(); err != nil {
		t.Fatalf("readEarlier: %v", err)
	}
	if r.outcome.EarlierRounds != 3 || r.tracker.Stats().Rounds != 3 || r.withoutOwn == nil || r.withoutOwn.Stats().Rounds != 3 {
		t.Fatalf("published earlier trackers/count: earlier=%d tracker=%+v fallback=%+v", r.outcome.EarlierRounds, r.tracker.Stats(), r.withoutOwn)
	}
	if r.tracker.oneRoundSpacing != 15*time.Second || r.withoutOwn.oneRoundSpacing != 10*time.Second {
		t.Fatalf("one-round spacing trackers: with-own=%s without-own=%s", r.tracker.oneRoundSpacing, r.withoutOwn.oneRoundSpacing)
	}
	if !r.outcome.TCPOpen || len(r.outcome.Latest) != 1 || r.outcome.Latest[0].Host != "node.example" || r.outcome.Latest[0].Row.TCP[443] != "open" {
		t.Fatalf("prior TCP/latest state = open:%t latest:%+v", r.outcome.TCPOpen, r.outcome.Latest)
	}
}

func TestReadEarlierDoesNotPublishWhenViewCompletionFails(t *testing.T) {
	ctx := context.Background()
	store, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge-view-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	request := bridgeRequest(strings.Repeat("c", 64))
	start := time.Unix(0, 0).UTC()
	if _, err := store.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: start, Heartbeat: start, Host: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordRound(ctx, "bridge-old", history.RoundInput{Started: start, Observations: "."}); err != nil {
		t.Fatal(err)
	}
	initialTracker := NewTracker(request.Hosts, request.TCPPorts, 0)
	failure := errors.New("view transaction failed after callback")
	r := bridgeRunner(bridgeViewError{History: store, err: failure}, request, start.Add(time.Minute))
	r.tracker = initialTracker
	if err := r.readEarlier(); !errors.Is(err, failure) {
		t.Fatalf("readEarlier error=%v, want view completion failure", err)
	}
	if r.tracker != initialTracker || r.withoutOwn != nil || r.outcome.EarlierRounds != 0 || r.outcome.Latest != nil {
		t.Fatalf("partial history escaped failed View: trackerChanged=%t fallback=%v outcome=%+v", r.tracker != initialTracker, r.withoutOwn, r.outcome)
	}
}

func TestOpenHistoryViewFailureKeepsCleanInMemoryTracker(t *testing.T) {
	ctx := context.Background()
	store, err := history.OpenWriter(ctx, filepath.Join(t.TempDir(), "bridge-open-view-error.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	request := bridgeRequest(strings.Repeat("2", 64))
	start := time.Unix(0, 0).UTC()
	if _, err := store.BeginRun(ctx, request, history.RunStart{ID: "bridge-old", Started: start, Heartbeat: start, Host: "bridge-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordRound(ctx, "bridge-old", history.RoundInput{Started: start, Observations: "!", Rows: []history.ResultRow{{Host: "node.example", Address: "192.0.2.1", Result: "OK", Time: start, TCP: map[int]string{443: "open"}}}}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("snapshot commit failed")
	r := bridgeRunner(bridgeViewError{History: store, err: failure}, request, start.Add(time.Minute))
	if err := r.openHistory(); err != nil {
		t.Fatalf("ordinary snapshot failure escaped the memory path: %v", err)
	}
	if r.tracker == nil || r.tracker.Stats().Rounds != 0 || r.withoutOwn != nil || r.outcome.EarlierRounds != 0 || r.runRecorded || r.store != nil {
		t.Fatalf("failed initial snapshot left invalid bridge state: tracker=%+v fallback=%v outcome=%+v runRecorded=%t store=%v", r.tracker, r.withoutOwn, r.outcome, r.runRecorded, r.store)
	}
	if !errors.Is(r.outcome.HistoryError, failure) {
		t.Fatalf("snapshot failure missing from HistoryError: %v", r.outcome.HistoryError)
	}
}
