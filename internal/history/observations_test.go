// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests cover Snapshot.Observations, the M13 whole-history stream that
// adapts PingStore.round_observations from cn-tool merge
// a739948c69ada9bab27d25058c1d695ca06d5aba (PR #178). The cn-tool store tests
// test_a_stored_round_that_does_not_fit_is_a_store_error and
// test_a_stored_round_with_an_unknown_character_is_a_store_error map to the
// malformed-row case; test_a_failed_fold_closes_the_round_stream_so_it_holds_no_read_mark
// maps to the prompt-View assertions; the context error replaces cn-tool's
// stop callback (test_stop_ends_the_fold, test_read_coverage_stops_when_asked).

type observedRound struct {
	started      time.Time
	observations string
}

func obsTestRound(started time.Time, observations string, hosts []string) RoundInput {
	round := RoundInput{Started: started, Observations: observations}
	for i, state := range []byte(observations) {
		if state == '!' || state == 'R' || state == 'T' {
			round.Rows = append(round.Rows, ResultRow{Host: hosts[i], Address: "192.0.2.1", Result: string(state), Time: started})
		}
	}
	return round
}

func obsTestBegin(t *testing.T, store *Store, key string, hosts []string, runID string, started time.Time) {
	t.Helper()
	request := Request{RequestMeta: RequestMeta{Key: key, Targets: []string{"fixture"}, TCPPorts: []int{}}, Hosts: hosts}
	if _, err := store.BeginRun(context.Background(), request, writeTestRun(runID, started)); err != nil {
		t.Fatalf("BeginRun(%s): %v", runID, err)
	}
}

func obsTestRecord(t *testing.T, store *Store, runID string, hosts []string, started time.Time, observations string) {
	t.Helper()
	if _, err := store.RecordRound(context.Background(), runID, obsTestRound(started, observations, hosts)); err != nil {
		t.Fatalf("RecordRound(%s, %s): %v", runID, observations, err)
	}
}

func obsTestRequestID(t *testing.T, store *Store, key string) int64 {
	t.Helper()
	return writeTestReadRequest(t, store, key).ID
}

// obsTestSeeded returns a writer and reader on one file holding request A (two
// runs whose rounds were committed out of start order) and an unrelated
// request B.
func obsTestSeeded(t *testing.T) (writer, reader *Store, hosts []string, idA, idB int64) {
	t.Helper()
	writer, reader, _ = readTestOpenPair(t)
	hosts = []string{"a.example", "b.example", "c.example"}
	keyA, keyB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	obsTestBegin(t, writer, keyA, hosts, "obs-run-1", writeTestTime(0))
	obsTestBegin(t, writer, keyA, hosts, "obs-run-2", writeTestTime(10))
	obsTestBegin(t, writer, keyB, []string{"x.example", "y.example"}, "obs-run-b", writeTestTime(5))
	obsTestRecord(t, writer, "obs-run-1", hosts, writeTestTime(300), "!.?")
	obsTestRecord(t, writer, "obs-run-2", hosts, writeTestTime(100), "U!.")
	obsTestRecord(t, writer, "obs-run-b", []string{"x.example", "y.example"}, writeTestTime(150), "!.")
	obsTestRecord(t, writer, "obs-run-1", hosts, writeTestTime(200), "..E")
	return writer, reader, hosts, obsTestRequestID(t, writer, keyA), obsTestRequestID(t, writer, keyB)
}

func obsTestCollect(t *testing.T, snapshot *Snapshot, requestID int64) []observedRound {
	t.Helper()
	var got []observedRound
	err := snapshot.Observations(requestID, func(started time.Time, observations string) error {
		got = append(got, observedRound{started, observations})
		return nil
	})
	if err != nil {
		t.Fatalf("Observations(%d): %v", requestID, err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].started.Before(got[j].started) })
	return got
}

// obsTestViewReturns fails the test when View does not finish promptly. An
// unclosed row set keeps the snapshot's connection busy, so closing it blocks.
func obsTestViewReturns(t *testing.T, store *Store, ctx context.Context, callback func(*Snapshot) error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- store.View(ctx, callback) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("View did not return: the observation rows were left open")
		return nil
	}
}

func TestObservationsStreamEveryRoundOfTheRequestWhateverTheOrder(t *testing.T) {
	_, reader, _, idA, idB := obsTestSeeded(t)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		want := []observedRound{
			{writeTestTime(100), "U!."},
			{writeTestTime(200), "..E"},
			{writeTestTime(300), "!.?"},
		}
		if got := obsTestCollect(t, snapshot, idA); !reflect.DeepEqual(got, want) {
			t.Fatalf("request A observations = %v, want %v", got, want)
		}
		wantB := []observedRound{{writeTestTime(150), "!."}}
		if got := obsTestCollect(t, snapshot, idB); !reflect.DeepEqual(got, wantB) {
			t.Fatalf("request B observations = %v, want %v", got, wantB)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}

// This proves snapshot isolation, not the query's bound: SQLite's read
// transaction already hides a round committed after View began, so the test
// also passes with the `AND rd.id<=?` clause removed. The clause is defensive
// (it keeps Observations consistent with Window and Rounds, which apply the same
// bound) and TestObservationsAppliesTheHighWaterBoundToTheQuery proves it.
func TestObservationsAreIsolatedFromRoundsCommittedAfterViewBegan(t *testing.T) {
	writer, reader, hosts, idA, _ := obsTestSeeded(t)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		obsTestRecord(t, writer, "obs-run-2", hosts, writeTestTime(400), "!!!")
		if got := obsTestCollect(t, snapshot, idA); len(got) != 3 {
			t.Fatalf("a round committed after View began is visible: %v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		if got := obsTestCollect(t, snapshot, idA); len(got) != 4 {
			t.Fatalf("a later View lacks the committed round: %v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("second View: %v", err)
	}
}

// A white-box proof of the bound: lowering the snapshot's high-water mark, which
// View fixes at the newest round ID, must hide the rounds above it. The seeded
// rounds follow the three rounds of the copied fixture file, so with newest as
// the highest ID the IDs are newest-3 for request A at 300, newest-2 for A at
// 100, newest-1 for request B and newest for A at 200.
func TestObservationsAppliesTheHighWaterBoundToTheQuery(t *testing.T) {
	_, reader, _, idA, _ := obsTestSeeded(t)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		newest := snapshot.through
		for _, tc := range []struct {
			through int64
			want    []observedRound
		}{
			{newest, []observedRound{{writeTestTime(100), "U!."}, {writeTestTime(200), "..E"}, {writeTestTime(300), "!.?"}}},
			{newest - 1, []observedRound{{writeTestTime(100), "U!."}, {writeTestTime(300), "!.?"}}},
			{newest - 3, []observedRound{{writeTestTime(300), "!.?"}}},
			{newest - 4, nil},
		} {
			snapshot.through = tc.through
			if got := obsTestCollect(t, snapshot, idA); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("through=%d (newest-%d): observations = %v, want %v", tc.through, newest-tc.through, got, tc.want)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
}

func TestObservationsOfAnUnknownRequestCallNothing(t *testing.T) {
	_, reader, _, _, _ := obsTestSeeded(t)
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		return snapshot.Observations(999, func(time.Time, string) error {
			t.Fatal("callback called for an unknown request")
			return nil
		})
	}); err != nil {
		t.Fatalf("unknown request: %v", err)
	}
}

func TestObservationsReportMalformedRowsAsHistoryErrorsAndCloseTheRows(t *testing.T) {
	for _, tc := range []struct {
		name         string
		started      string
		observations string
	}{
		{"short row", "1800000050.0", "!."},
		{"long row", "1800000050.0", "!.??"},
		{"unknown byte", "1800000050.0", "!x."},
		{"non-finite start", "9e999", "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer, reader, _, idA, _ := obsTestSeeded(t)
			readTestExec(t, writer, `INSERT INTO round(run_id,started,observations,answered) VALUES('obs-run-1',`+tc.started+`,?,0)`, tc.observations)
			var streamErr error
			viewErr := obsTestViewReturns(t, reader, context.Background(), func(snapshot *Snapshot) error {
				streamErr = snapshot.Observations(idA, func(time.Time, string) error { return nil })
				return nil
			})
			if viewErr != nil {
				t.Fatalf("View after the stream failure: %v", viewErr)
			}
			if streamErr == nil || !strings.Contains(streamErr.Error(), "invalid stored round") {
				t.Fatalf("Observations = %v, want an invalid stored round history error", streamErr)
			}
			if CancellationOnly(streamErr, context.Canceled) || errors.Is(streamErr, context.Canceled) {
				t.Fatalf("a malformed row was reported as cancellation: %v", streamErr)
			}
		})
	}
}

func TestObservationsEndWithTheContextErrorWhenCancelledFromTheCallback(t *testing.T) {
	_, reader, _, idA, _ := obsTestSeeded(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var streamErr error
	viewErr := obsTestViewReturns(t, reader, ctx, func(snapshot *Snapshot) error {
		streamErr = snapshot.Observations(idA, func(time.Time, string) error {
			calls++
			cancel()
			return nil
		})
		return streamErr
	})
	if calls != 1 {
		t.Fatalf("callback ran %d times, want 1: no call may follow the cancellation", calls)
	}
	if !errors.Is(streamErr, context.Canceled) || !CancellationOnly(streamErr, context.Canceled) {
		t.Fatalf("Observations = %v, want only the context error", streamErr)
	}
	if !CancellationOnly(viewErr, context.Canceled) {
		t.Fatalf("View = %v, want only the context error", viewErr)
	}
}

func TestObservationsReturnTheCallbackErrorAndCloseTheRows(t *testing.T) {
	_, reader, _, idA, _ := obsTestSeeded(t)
	sentinel := errors.New("fold refused the round")
	calls := 0
	var streamErr error
	viewErr := obsTestViewReturns(t, reader, context.Background(), func(snapshot *Snapshot) error {
		streamErr = snapshot.Observations(idA, func(time.Time, string) error {
			calls++
			return sentinel
		})
		return nil
	})
	if viewErr != nil {
		t.Fatalf("View after the callback error: %v", viewErr)
	}
	if !errors.Is(streamErr, sentinel) || calls != 1 {
		t.Fatalf("Observations = %v after %d calls, want the callback error after exactly one", streamErr, calls)
	}
}

// TestObservationsStreamASixtyFiveThousandHostRequestOneRowAtATime records, without
// guaranteeing it, that the retained heap while streaming 200 rounds of a
// 65,536-host request stays well below the 13 MiB of observations. cn-tool
// measured a /16 pinged hourly for 180 days at 406 MB, which must stream.
func TestObservationsStreamASixtyFiveThousandHostRequestOneRowAtATime(t *testing.T) {
	if testing.Short() {
		t.Skip("bounded-memory observation skipped in short mode")
	}
	const hostCount, roundCount = 65536, 200
	hosts := make([]string, hostCount)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("10.%d.%d.%d", 1+i/65536, (i/256)%256, i%256)
	}
	writer, reader, _ := readTestOpenPair(t)
	key := strings.Repeat("c", 64)
	obsTestBegin(t, writer, key, hosts, "obs-run-big", writeTestTime(0))
	silent := strings.Repeat(".", hostCount)
	if err := writer.withTx(context.Background(), true, func(conn *sql.Conn) error {
		for i := 0; i < roundCount; i++ {
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO round(run_id,started,observations,answered) VALUES(?,?,?,0)`,
				"obs-run-big", 1_800_000_000+float64(i)*3600, silent); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed rounds: %v", err)
	}
	id := obsTestRequestID(t, writer, key)

	heap := func() uint64 {
		var stats runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	baseline := heap()
	var peak uint64
	rounds := 0
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		return snapshot.Observations(id, func(_ time.Time, observations string) error {
			if len(observations) != hostCount {
				t.Fatalf("observation length = %d, want %d", len(observations), hostCount)
			}
			rounds++
			if rounds%50 == 1 {
				if used := heap(); used > peak {
					peak = used
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if rounds != roundCount {
		t.Fatalf("streamed %d rounds, want %d", rounds, roundCount)
	}
	retained := int64(peak) - int64(baseline)
	total := int64(hostCount) * roundCount
	t.Logf("observation: peak retained heap %d bytes while streaming %d bytes of observations (%d rounds of %d hosts)", retained, total, roundCount, hostCount)
	if retained > total/2 {
		t.Fatalf("retained heap %d bytes is not small beside %d bytes streamed: the history is being loaded, not streamed", retained, total)
	}
}
