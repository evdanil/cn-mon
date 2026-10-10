package history

import (
	"context"
	"math"
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
		{"stopped empty run deleted", "stopped", now, nil, false},
		{"failed empty run deleted", "failed", now, nil, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := strings.Repeat(string("123456abcdef"[i]), 64)
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

func TestPruneRejectsMalformedStoredRunStatusWithoutMutation(t *testing.T) {
	statuses := []string{"bogus", "", "lost"}
	for statusIndex, status := range statuses {
		for _, hasOldRound := range []bool{false, true} {
			name := "empty_run"
			if hasOldRound {
				name = "run_with_old_round"
			}
			t.Run(status+"/"+name, func(t *testing.T) {
				store := writeTestOpen(t)
				ctx := context.Background()
				now := writeTestTime(7000)
				request := writeTestRequest(strings.Repeat(string("abcdef"[statusIndex]), 64))
				runID := "prune-invalid-status-" + name
				requestID, err := store.BeginRun(ctx, request, writeTestRun(runID, now.Add(-48*time.Hour)))
				if err != nil {
					t.Fatalf("BeginRun: %v", err)
				}
				if hasOldRound {
					pruneTestRecord(t, store, runID, now.Add(-48*time.Hour))
				}
				validRunID := runID + "-valid-prunable"
				validRequest := writeTestRequest(strings.Repeat(string("012345"[statusIndex]), 64))
				if _, err := store.BeginRun(ctx, validRequest, writeTestRun(validRunID, now.Add(-48*time.Hour))); err != nil {
					t.Fatalf("BeginRun otherwise-prunable fixture: %v", err)
				}
				pruneTestRecord(t, store, validRunID, now.Add(-48*time.Hour))
				if err := store.EndRun(ctx, validRunID, now.Add(-47*time.Hour), "finished"); err != nil {
					t.Fatalf("EndRun otherwise-prunable fixture: %v", err)
				}
				if _, err := store.db.ExecContext(ctx, `UPDATE run SET status = ? WHERE id = ?`, status, runID); err != nil {
					t.Fatalf("inject stored status %q: %v", status, err)
				}
				before := pruneTestRawState(t, store)
				deleted, err := store.Prune(ctx, now, big.NewInt(1))
				if err == nil {
					t.Fatalf("Prune accepted malformed stored status %q and deleted %d rounds", status, deleted)
				}
				if deleted != 0 {
					t.Fatalf("Prune reported %d deleted rounds on malformed stored status", deleted)
				}
				after := pruneTestRawState(t, store)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("Prune mutated related raw history on malformed status %q: before=%#v after=%#v", status, before, after)
				}
				if requestID <= 0 {
					t.Fatalf("invalid request fixture ID %d", requestID)
				}
			})
		}
	}
}

func TestPruneRejectsMalformedRetentionScalarsBeforeMutation(t *testing.T) {
	tests := []struct {
		name         string
		table        string
		column       string
		value        any
		sqlType      string
		withOldRound bool
	}{
		{"round-before-year-one", "round", "started", float64(-62135596801), "real", true},
		{"round-negative-infinity", "round", "started", math.Inf(-1), "real", true},
		{"round-text", "round", "started", "nonsense", "text", true},
		{"round-numeric-blob", "round", "started", []byte("1"), "blob", true},
		{"round-year-ten-thousand", "round", "started", float64(253402300800), "real", true},
		{"heartbeat-before-year-one", "run", "heartbeat", float64(-62135596801), "real", false},
		{"heartbeat-negative-infinity", "run", "heartbeat", math.Inf(-1), "real", false},
		{"heartbeat-text", "run", "heartbeat", "nonsense", "text", false},
		{"heartbeat-numeric-blob", "run", "heartbeat", []byte("1"), "blob", false},
		{"heartbeat-invalid-with-old-round", "run", "heartbeat", math.Inf(-1), "real", true},
		{"interval-negative", "run", "interval", float64(-1), "real", false},
		{"interval-negative-infinity", "run", "interval", math.Inf(-1), "real", false},
		{"interval-text", "run", "interval", "nonsense", "text", false},
		{"interval-numeric-blob", "run", "interval", []byte("1"), "blob", false},
		{"interval-over-duration", "run", "interval", float64(9223372037), "real", false},
		{"interval-rounds-past-duration-endpoint", "run", "interval", float64(9223372036.854776), "real", false},
		{"interval-invalid-with-old-round", "run", "interval", float64(9223372037), "real", true},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := writeTestOpen(t)
			ctx := context.Background()
			now := writeTestTime(8000)
			old := now.Add(-45 * 24 * time.Hour)
			runID := "prune-scalar-suspect-" + test.name
			request := writeTestRequest(strings.Repeat(string("abcdef0123456789"[i%16]), 64))
			if _, err := store.BeginRun(ctx, request, writeTestRun(runID, old)); err != nil {
				t.Fatalf("BeginRun suspect: %v", err)
			}
			if test.withOldRound {
				pruneTestRecord(t, store, runID, old)
				if test.table == "round" {
					if err := store.EndRun(ctx, runID, old, "finished"); err != nil {
						t.Fatalf("EndRun malformed-round fixture: %v", err)
					}
				}
			}
			validRequest := writeTestRequest(strings.Repeat(string("0123456789abcdef"[(i+1)%16]), 64))
			validRunID := "prune-scalar-valid-prunable"
			if _, err := store.BeginRun(ctx, validRequest, writeTestRun(validRunID, old)); err != nil {
				t.Fatalf("BeginRun otherwise-prunable fixture: %v", err)
			}
			pruneTestRecord(t, store, validRunID, old)
			if err := store.EndRun(ctx, validRunID, old, "finished"); err != nil {
				t.Fatalf("EndRun otherwise-prunable fixture: %v", err)
			}
			var injectErr error
			if test.table == "round" {
				_, injectErr = store.db.ExecContext(ctx, `UPDATE round SET started = ? WHERE run_id = ?`, test.value, runID)
			} else {
				_, injectErr = store.db.ExecContext(ctx, `UPDATE run SET `+test.column+` = ? WHERE id = ?`, test.value, runID)
			}
			if injectErr != nil {
				t.Fatalf("inject %s.%s=%#v: %v", test.table, test.column, test.value, injectErr)
			}
			rowKey := "id"
			if test.table == "round" {
				rowKey = "run_id"
			}
			var gotType string
			if err := store.db.QueryRowContext(ctx, `SELECT typeof(`+test.column+`) FROM `+test.table+` WHERE `+rowKey+` = ?`, runID).Scan(&gotType); err != nil {
				t.Fatalf("read injected scalar type: %v", err)
			}
			if gotType != test.sqlType {
				t.Fatalf("SQLite stored %s.%s as %s, want %s", test.table, test.column, gotType, test.sqlType)
			}
			before := pruneTestRawState(t, store)
			deleted, err := store.Prune(ctx, now, big.NewInt(30))
			if err == nil {
				t.Fatalf("Prune accepted malformed %s.%s=%#v and deleted %d rounds", test.table, test.column, test.value, deleted)
			}
			if deleted != 0 {
				t.Fatalf("Prune reported %d deleted rounds for malformed retention scalars", deleted)
			}
			after := pruneTestRawState(t, store)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("Prune changed raw history for malformed %s.%s=%#v: before=%#v after=%#v", test.table, test.column, test.value, before, after)
			}
		})
	}
}

func TestPruneAcceptsIntegerRealAndLargeValidRetentionScalars(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	now := writeTestTime(9000)
	old := now.Add(-45 * 24 * time.Hour)
	request := writeTestRequest(strings.Repeat("a", 64))
	finishedRunID := "prune-valid-numeric-rounds"
	if _, err := store.BeginRun(ctx, request, writeTestRun(finishedRunID, old)); err != nil {
		t.Fatalf("BeginRun finished fixture: %v", err)
	}
	intRoundID, err := store.RecordRound(ctx, finishedRunID, writeTestRound(old))
	if err != nil {
		t.Fatalf("RecordRound integer fixture: %v", err)
	}
	if _, err := store.RecordRound(ctx, finishedRunID, writeTestRound(old.Add(time.Hour))); err != nil {
		t.Fatalf("RecordRound real fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE round SET started = ? WHERE id = ?`, old.Unix(), intRoundID); err != nil {
		t.Fatalf("set integer round timestamp: %v", err)
	}
	if err := store.EndRun(ctx, finishedRunID, old.Add(2*time.Hour), "finished"); err != nil {
		t.Fatalf("EndRun finished fixture: %v", err)
	}
	integerRequest := writeTestRequest(strings.Repeat("b", 64))
	integerRunID := "prune-valid-integer-empty-run"
	if _, err := store.BeginRun(ctx, integerRequest, writeTestRun(integerRunID, old)); err != nil {
		t.Fatalf("BeginRun integer empty fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE run SET heartbeat = ?, interval = ? WHERE id = ?`, old.Unix(), int64(1), integerRunID); err != nil {
		t.Fatalf("set integer running scalars: %v", err)
	}
	largeRequest := writeTestRequest(strings.Repeat("c", 64))
	largeRunID := "prune-valid-9999-weeks"
	largeInterval := 9999 * 7 * 24 * time.Hour
	if _, err := store.BeginRun(ctx, largeRequest, func() RunStart {
		run := writeTestRun(largeRunID, old)
		run.Interval = &largeInterval
		return run
	}()); err != nil {
		t.Fatalf("BeginRun large interval fixture: %v", err)
	}
	var integerArgumentType, realType, integerIntervalArgumentType string
	if err := store.db.QueryRowContext(ctx, `SELECT typeof(started) FROM round WHERE id = ?`, intRoundID).Scan(&integerArgumentType); err != nil {
		t.Fatalf("read integer-argument timestamp type: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT typeof(started) FROM round WHERE run_id = ? AND started > ?`, finishedRunID, old.Unix()).Scan(&realType); err != nil {
		t.Fatalf("read real timestamp type: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT typeof(interval) FROM run WHERE id = ?`, integerRunID).Scan(&integerIntervalArgumentType); err != nil {
		t.Fatalf("read integer-argument interval type: %v", err)
	}
	if integerArgumentType != "real" || realType != "real" || integerIntervalArgumentType != "real" {
		t.Fatalf("REAL-column affinity types integer-argument-round=%q real-round=%q integer-argument-interval=%q", integerArgumentType, realType, integerIntervalArgumentType)
	}
	deleted, err := store.Prune(ctx, now, big.NewInt(30))
	if err != nil || deleted != 2 {
		t.Fatalf("Prune valid numeric scalars deleted=%d err=%v, want two rounds", deleted, err)
	}
	if got := writeTestRequestAndRuns(t, store, request.Key); got.request != nil {
		t.Fatalf("Prune retained fully expired finalized run: %+v", got)
	}
	if got := writeTestRequestAndRuns(t, store, integerRequest.Key); got.request != nil {
		t.Fatalf("Prune retained stale empty integer run: %+v", got)
	}
	if got := writeTestRequestAndRuns(t, store, largeRequest.Key); got.request == nil || got.request.ID <= 0 || len(got.runs) != 1 || got.runs[0].Interval == nil || *got.runs[0].Interval != largeInterval {
		t.Fatalf("Prune did not preserve supported 9,999-week live interval: %+v", got)
	}
}

func TestPruneSkipsUnusedMalformedScalarsOnRunningRunsWithSurvivingRounds(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
		age   time.Duration
	}{
		// Equality matters here: the retention predicate preserves rounds at
		// the cutoff, so malformed heartbeat data on that still-live run is
		// unused and must not block pruning elsewhere.
		{"heartbeat", "nonsense", 30 * 24 * time.Hour},
		{"interval", []byte("nonsense"), time.Hour},
	} {
		t.Run(tc.field, func(t *testing.T) {
			store := writeTestOpen(t)
			ctx := context.Background()
			now := writeTestTime(10000)
			surviving := writeTestRequest(strings.Repeat("d", 64))
			liveRunID := "prune-malformed-retained-" + tc.field
			if _, err := store.BeginRun(ctx, surviving, writeTestRun(liveRunID, now)); err != nil {
				t.Fatalf("BeginRun live fixture: %v", err)
			}
			pruneTestRecord(t, store, liveRunID, now.Add(-tc.age))
			if _, err := store.db.ExecContext(ctx, `UPDATE run SET `+tc.field+` = ? WHERE id = ?`, tc.value, liveRunID); err != nil {
				t.Fatalf("inject retained run scalar: %v", err)
			}
			eligible := writeTestRequest(strings.Repeat("e", 64))
			eligibleRunID := "prune-valid-eligible-retained-scalar"
			old := now.Add(-45 * 24 * time.Hour)
			if _, err := store.BeginRun(ctx, eligible, writeTestRun(eligibleRunID, old)); err != nil {
				t.Fatalf("BeginRun eligible fixture: %v", err)
			}
			pruneTestRecord(t, store, eligibleRunID, old)
			if err := store.EndRun(ctx, eligibleRunID, old, "finished"); err != nil {
				t.Fatalf("EndRun eligible fixture: %v", err)
			}
			deleted, err := store.Prune(ctx, now, big.NewInt(30))
			if err != nil || deleted != 1 {
				t.Fatalf("Prune with unused malformed scalar deleted=%d err=%v, want unrelated round deleted", deleted, err)
			}
			var liveRounds int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM round WHERE run_id = ?`, liveRunID).Scan(&liveRounds); err != nil {
				t.Fatalf("count surviving round: %v", err)
			}
			if liveRounds != 1 {
				t.Fatalf("Prune removed surviving round: count=%d", liveRounds)
			}
			if got := writeTestReadRequest(t, store, eligible.Key); got != nil {
				t.Fatalf("Prune retained unrelated eligible request: %+v", got)
			}
		})
	}
}

func TestPruneHugeRetentionSkipsUnusedMalformedScalars(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	now := writeTestTime(11000)
	withRound := writeTestRequest(strings.Repeat("1", 64))
	oldRunID := "prune-huge-malformed-round"
	old := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := store.BeginRun(ctx, withRound, writeTestRun(oldRunID, old)); err != nil {
		t.Fatalf("BeginRun malformed round fixture: %v", err)
	}
	pruneTestRecord(t, store, oldRunID, old)
	if err := store.EndRun(ctx, oldRunID, old, "finished"); err != nil {
		t.Fatalf("EndRun malformed round fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE round SET started = 'nonsense' WHERE run_id = ?`, oldRunID); err != nil {
		t.Fatalf("inject unused malformed round timestamp: %v", err)
	}
	running := writeTestRequest(strings.Repeat("2", 64))
	runningID := "prune-huge-malformed-running"
	if _, err := store.BeginRun(ctx, running, writeTestRun(runningID, old)); err != nil {
		t.Fatalf("BeginRun malformed running fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE run SET heartbeat = -62135596801, interval = 'nonsense' WHERE id = ?`, runningID); err != nil {
		t.Fatalf("inject unused malformed running scalars: %v", err)
	}
	finalized := writeTestRequest(strings.Repeat("3", 64))
	if _, err := store.BeginRun(ctx, finalized, writeTestRun("prune-huge-finalized-empty", now)); err != nil {
		t.Fatalf("BeginRun finalized empty fixture: %v", err)
	}
	if err := store.EndRun(ctx, "prune-huge-finalized-empty", now, "finished"); err != nil {
		t.Fatalf("EndRun finalized empty fixture: %v", err)
	}
	days := new(big.Int).Exp(big.NewInt(10), big.NewInt(100), nil)
	deleted, err := store.Prune(ctx, now, days)
	if err != nil || deleted != 0 {
		t.Fatalf("Prune huge retention deleted=%d err=%v, want success with no round deletion", deleted, err)
	}
	var retainedRounds int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM round WHERE run_id = ?`, oldRunID).Scan(&retainedRounds); err != nil {
		t.Fatalf("count retained malformed round: %v", err)
	}
	if retainedRounds != 1 {
		t.Fatalf("huge retention removed round whose timestamp was not interpreted: rounds=%d", retainedRounds)
	}
	if got := writeTestReadRequest(t, store, running.Key); got == nil {
		t.Fatal("huge retention deleted empty running run whose malformed fields were unused")
	}
	if got := writeTestReadRequest(t, store, finalized.Key); got != nil {
		t.Fatalf("huge retention failed to delete finalized empty request: %+v", got)
	}
}

type pruneTestState struct {
	requests [][]any
	runs     [][]any
	rounds   [][]any
	results  [][]any
}

func pruneTestRawState(t *testing.T, store *Store) pruneTestState {
	t.Helper()
	return pruneTestState{
		requests: pruneTestQueryRows(t, store, `SELECT id, key, targets, tcp_ports, hosts FROM request ORDER BY id`, 5),
		runs:     pruneTestQueryRows(t, store, `SELECT id, request_id, source, started, interval, until, host, pid, heartbeat, round_seconds, ended, status FROM run ORDER BY id`, 12),
		rounds:   pruneTestQueryRows(t, store, `SELECT id, run_id, started, observations, answered FROM round ORDER BY id`, 5),
		results:  pruneTestQueryRows(t, store, `SELECT round_id, host, address, t, result, tcp FROM result ORDER BY round_id, host`, 6),
	}
}

func pruneTestQueryRows(t *testing.T, store *Store, query string, columns int) [][]any {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("query raw history: %v", err)
	}
	defer rows.Close()
	var found [][]any
	for rows.Next() {
		values := make([]any, columns)
		pointers := make([]any, columns)
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("scan raw history: %v", err)
		}
		found = append(found, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate raw history: %v", err)
	}
	return found
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
