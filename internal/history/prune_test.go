package history

import (
	"context"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPruneDeletesStrictlyOlderRoundsAndCascadesDetails(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	now := writeTestTime(1000)
	request := writeTestRequest(strings.Repeat("2", 64))
	requestID, err := store.BeginRun(ctx, request, writeTestRun("prune-boundary", now))
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	cutoff := now.Add(-24 * time.Hour)
	pruneTestRecord(t, store, "prune-boundary", cutoff.Add(-time.Second))
	pruneTestRecord(t, store, "prune-boundary", cutoff)
	pruneTestRecord(t, store, "prune-boundary", cutoff.Add(time.Second))

	deleted, err := store.Prune(ctx, now, big.NewInt(1))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Prune deleted %d rounds, want only the round strictly older than cutoff", deleted)
	}
	stored := writeTestReadRounds(t, store, requestID)
	if len(stored) != 2 || !stored[0].Started.Equal(cutoff) || !stored[1].Started.Equal(cutoff.Add(time.Second)) {
		t.Fatalf("round cutoff equality/order = %+v", stored)
	}
	for _, round := range stored {
		if round.Answered != 2 || len(round.Rows) != 2 {
			t.Fatalf("surviving round lost cascade details: %+v", round)
		}
	}
	var detailCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM result`).Scan(&detailCount); err != nil {
		t.Fatalf("count surviving detail rows: %v", err)
	}
	if detailCount != 4 {
		t.Fatalf("surviving detail rows=%d, want 4; expired round details must be removed by cascade", detailCount)
	}
}

func TestPruneConfiguredNinetyDaysAndThirtyDayOverride(t *testing.T) {
	now := writeTestTime(2000)
	for _, tc := range []struct {
		name       string
		days       int64
		wantRounds int
	}{
		{"configured 90 day retention", 90, 1},
		{"explicit 30 day retention", 30, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeTestOpen(t)
			request := writeTestRequest(strings.Repeat("3", 64))
			requestID, err := store.BeginRun(context.Background(), request, writeTestRun("prune-days", now))
			if err != nil {
				t.Fatalf("BeginRun: %v", err)
			}
			pruneTestRecord(t, store, "prune-days", now.Add(-45*24*time.Hour))
			deleted, err := store.Prune(context.Background(), now, big.NewInt(tc.days))
			if err != nil {
				t.Fatalf("Prune: %v", err)
			}
			wantDeleted := int64(1 - tc.wantRounds)
			if deleted != wantDeleted {
				t.Fatalf("Prune deleted %d rounds, want %d", deleted, wantDeleted)
			}
			if got := len(writeTestReadRounds(t, store, requestID)); got != tc.wantRounds {
				t.Fatalf("remaining rounds=%d, want %d", got, tc.wantRounds)
			}
		})
	}
}

func TestPruneClampsDaysAndDoesNotMutateInput(t *testing.T) {
	now := writeTestTime(3000)
	for _, days := range []int64{0, -7} {
		t.Run("days_"+big.NewInt(days).String(), func(t *testing.T) {
			store := writeTestOpen(t)
			request := writeTestRequest(strings.Repeat("4", 64))
			requestID, err := store.BeginRun(context.Background(), request, writeTestRun("prune-clamp", now))
			if err != nil {
				t.Fatalf("BeginRun: %v", err)
			}
			pruneTestRecord(t, store, "prune-clamp", now.Add(-2*24*time.Hour))
			input := big.NewInt(days)
			before := new(big.Int).Set(input)
			deleted, err := store.Prune(context.Background(), now, input)
			if err != nil {
				t.Fatalf("Prune: %v", err)
			}
			if deleted != 1 || !reflect.DeepEqual(input, before) {
				t.Fatalf("clamped prune deleted=%d input-days=%s, want one deletion and unchanged input %s", deleted, input, before)
			}
			if got := len(writeTestReadRounds(t, store, requestID)); got != 0 {
				t.Fatalf("remaining rounds=%d after one-day clamp", got)
			}
		})
	}
}

func TestPruneRejectsNilDaysWithoutChangingHistory(t *testing.T) {
	store := writeTestOpen(t)
	now := writeTestTime(3500)
	request := writeTestRequest(strings.Repeat("a", 64))
	requestID, err := store.BeginRun(context.Background(), request, writeTestRun("prune-nil-days", now))
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	pruneTestRecord(t, store, "prune-nil-days", now.Add(-48*time.Hour))
	if _, err := store.Prune(context.Background(), now, nil); err == nil {
		t.Fatal("Prune accepted nil days")
	}
	if rounds := writeTestReadRounds(t, store, requestID); len(rounds) != 1 {
		t.Fatalf("nil retention error changed history: rounds=%d", len(rounds))
	}
}

func TestPruneHugeBigIntegerPreservesRoundsAndRemovesFinalizedEmptyRequest(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	now := writeTestTime(4000)
	retained := writeTestRequest(strings.Repeat("5", 64))
	retainedID, err := store.BeginRun(ctx, retained, writeTestRun("prune-huge-round", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("BeginRun retained: %v", err)
	}
	pruneTestRecord(t, store, "prune-huge-round", time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC))

	finalized := writeTestRequest(strings.Repeat("6", 64))
	if _, err := store.BeginRun(ctx, finalized, writeTestRun("prune-huge-empty", now)); err != nil {
		t.Fatalf("BeginRun finalized empty: %v", err)
	}
	if err := store.EndRun(ctx, "prune-huge-empty", now, "finished"); err != nil {
		t.Fatalf("EndRun: %v", err)
	}

	days := new(big.Int).Exp(big.NewInt(10), big.NewInt(100), nil)
	before := new(big.Int).Set(days)
	deleted, err := store.Prune(ctx, now, days)
	if err != nil {
		t.Fatalf("Prune huge days: %v", err)
	}
	if deleted != 0 || days.Cmp(before) != 0 {
		t.Fatalf("Prune huge days deleted=%d mutated input=%v", deleted, days.Cmp(before) != 0)
	}
	if got := len(writeTestReadRounds(t, store, retainedID)); got != 1 {
		t.Fatalf("huge retention removed a round with an unrepresentably old cutoff: rounds=%d", got)
	}
	if writeTestReadRequest(t, store, retained.Key) == nil {
		t.Fatal("huge retention removed request whose round remains")
	}
	if writeTestReadRequest(t, store, finalized.Key) != nil {
		t.Fatal("Prune retained a finalized zero-round run/request under huge retention")
	}
}

func TestPruneEmptyRunRequiresBothStrictStalenessConditions(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	now := writeTestTime(5000)
	cutoff := now.Add(-24 * time.Hour)
	cases := []struct {
		name       string
		status     string
		heartbeat  time.Time
		interval   *time.Duration
		wantRemain bool
	}{
		{"heartbeat equality survives", "running", cutoff, nil, true},
		{"gap equality survives after stale heartbeat", "running", now.Add(-3 * 24 * time.Hour), pruneTestDuration(28*time.Hour + 48*time.Minute), true},
		{"expired gap deletes after stale heartbeat", "running", now.Add(-3 * 24 * time.Hour), pruneTestDuration(28*time.Hour + 47*time.Minute + 59*time.Second), false},
		{"stale running empty run deleted", "running", cutoff.Add(-time.Second), nil, false},
		{"long interval keeps empty live run", "running", now.Add(-3 * 24 * time.Hour), pruneTestDuration(2 * 24 * time.Hour), true},
		{"finalized empty run deleted", "finished", now, nil, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := strings.Repeat(string("789abc"[i]), 64)
			request := writeTestRequest(key)
			run := writeTestRun("prune-empty-"+tc.name, tc.heartbeat)
			run.Interval = tc.interval
			id, err := store.BeginRun(ctx, request, run)
			if err != nil {
				t.Fatalf("BeginRun: %v", err)
			}
			if tc.status != "running" {
				if err := store.EndRun(ctx, run.ID, now, tc.status); err != nil {
					t.Fatalf("EndRun: %v", err)
				}
			}
			deletedRounds, err := store.Prune(ctx, now, big.NewInt(1))
			if err != nil || deletedRounds != 0 {
				t.Fatalf("Prune empty run deletedRounds=%d err=%v", deletedRounds, err)
			}
			got := writeTestRequestAndRuns(t, store, request.Key)
			if tc.wantRemain {
				if got.request == nil || got.request.ID != id || len(got.runs) != 1 || got.runs[0].ID != run.ID {
					t.Fatalf("live empty run/request not retained: %+v", got)
				}
			} else if got.request != nil {
				t.Fatalf("expired/finalized empty run left request behind: %+v", got)
			}
		})
	}
}

func TestPruneRecreateRequestKeepsCommittedRoundIDsIncreasing(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	now := writeTestTime(6000)
	request := writeTestRequest(strings.Repeat("c", 64))
	oldRunID := "prune-recreate-old"
	oldRequestID, err := store.BeginRun(ctx, request, writeTestRun(oldRunID, now.Add(-48*time.Hour)))
	if err != nil {
		t.Fatalf("BeginRun old request: %v", err)
	}
	oldRound, err := store.RecordRound(ctx, oldRunID, writeTestRound(now.Add(-48*time.Hour)))
	if err != nil {
		t.Fatalf("RecordRound old request: %v", err)
	}
	if err := store.EndRun(ctx, oldRunID, now.Add(-47*time.Hour), "finished"); err != nil {
		t.Fatalf("EndRun old request: %v", err)
	}
	deleted, err := store.Prune(ctx, now, big.NewInt(1))
	if err != nil || deleted != 1 {
		t.Fatalf("Prune old request deleted=%d err=%v, want one old committed round", deleted, err)
	}
	var oldRequests, oldRuns int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request WHERE id = ?`, oldRequestID).Scan(&oldRequests); err != nil {
		t.Fatalf("count pruned request: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run WHERE id = ?`, oldRunID).Scan(&oldRuns); err != nil {
		t.Fatalf("count pruned run: %v", err)
	}
	if oldRequests != 0 || oldRuns != 0 {
		t.Fatalf("full prune retained request/run: requests=%d runs=%d", oldRequests, oldRuns)
	}
	if err := store.View(ctx, func(snapshot *Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info != nil {
			t.Errorf("public Request saw pruned identity: %+v", info)
		}
		page, err := snapshot.Rounds(oldRequestID, RoundFilter{})
		if err != nil {
			return err
		}
		if len(page.Items) != 0 {
			t.Errorf("public Rounds saw pruned history: %+v", page.Items)
		}
		return nil
	}); err != nil {
		t.Fatalf("read after full prune: %v", err)
	}

	newRunID := "prune-recreate-new"
	newRequestID, err := store.BeginRun(ctx, request, writeTestRun(newRunID, now))
	if err != nil {
		t.Fatalf("BeginRun recreated request: %v", err)
	}
	if newRequestID <= oldRequestID {
		t.Fatalf("recreated request ID=%d, want a fresh ID above committed prior ID %d", newRequestID, oldRequestID)
	}
	newRound, err := store.RecordRound(ctx, newRunID, writeTestRound(now))
	if err != nil {
		t.Fatalf("RecordRound recreated request: %v", err)
	}
	if newRound <= oldRound {
		t.Fatalf("new committed round ID=%d, want above prior committed round ID %d", newRound, oldRound)
	}
	if err := store.View(ctx, func(snapshot *Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.ID != newRequestID || info.Key != request.Key || !reflect.DeepEqual(info.Targets, request.Targets) || !reflect.DeepEqual(info.TCPPorts, request.TCPPorts) || !reflect.DeepEqual(info.Hosts, request.Hosts) {
			t.Errorf("public Request after recreation = %+v, want ID %d and original metadata", info, newRequestID)
			return nil
		}
		page, err := snapshot.Rounds(newRequestID, RoundFilter{})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].ID != newRound || page.Items[0].RunID != newRunID || len(page.Items[0].Rows) != 2 {
			t.Errorf("public Rounds after recreation = %+v, want committed round %d for %s", page.Items, newRound, newRunID)
		}
		return nil
	}); err != nil {
		t.Fatalf("read recreated history: %v", err)
	}
}

func pruneTestRecord(t *testing.T, store *Store, runID string, started time.Time) {
	t.Helper()
	if _, err := store.RecordRound(context.Background(), runID, writeTestRound(started)); err != nil {
		t.Fatalf("RecordRound(%s at %s): %v", runID, started, err)
	}
}

func writeTestRound(started time.Time) RoundInput {
	return RoundInput{Started: started, Observations: "!!", Rows: []ResultRow{
		{Host: "host-a", Address: "192.0.2.1", Result: "!", Time: started},
		{Host: "host-b", Address: "192.0.2.2", Result: "!", Time: started},
	}}
}

func pruneTestDuration(value time.Duration) *time.Duration { return &value }
