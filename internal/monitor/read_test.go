// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

type readFixedClock struct{ now time.Time }

func (c readFixedClock) Now() ClockReading                    { return ClockReading{Wall: c.now, Mono: 0} }
func (c readFixedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

type readObservedReader struct {
	Reader
	views, closed     int
	viewErr, closeErr error
}

func (r *readObservedReader) View(ctx context.Context, callback func(*history.Snapshot) error) error {
	r.views++
	return errors.Join(r.Reader.View(ctx, callback), r.viewErr)
}

func (r *readObservedReader) Close() error {
	r.closed++
	return errors.Join(r.Reader.Close(), r.closeErr)
}

func readRequest(t *testing.T, hosts []string, ports []int) target.Request {
	t.Helper()
	objects := append([]string(nil), hosts...)
	request, rejected, err := target.Build(objects, ports)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	return request
}

func seedReadHistory(t *testing.T, path string, request target.Request, runID string, started time.Time, interval *time.Duration, rounds []history.RoundInput, status string) {
	t.Helper()
	store, err := history.OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	source := "targets.txt"
	_, err = store.BeginRun(context.Background(), history.Request{RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports}, Hosts: request.Hosts}, history.RunStart{
		ID: runID, Source: &source, Started: started, Heartbeat: started, Interval: interval,
		Host: "reader-test", PID: 1234,
	})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	for _, round := range rounds {
		if _, err := store.RecordRound(context.Background(), runID, round); err != nil {
			t.Fatalf("RecordRound: %v", err)
		}
	}
	if status != "running" {
		if err := store.EndRun(context.Background(), runID, started.Add(time.Second), status); err != nil {
			t.Fatalf("EndRun: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
}

func readDeps(now time.Time) ReadDeps {
	return ReadDeps{
		Clock: readFixedClock{now: now}, Hostname: "reader-test",
		PIDState: func(int64) history.PIDState { return history.PIDAlive },
	}
}

func TestListReadsRequestRowsFromOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10", "192.0.2.11"}, []int{443, 80})
	seedReadHistory(t, path, request, "read-list-run", started, nil, []history.RoundInput{{
		Started: started, Observations: "!.", Rows: []history.ResultRow{{Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started, TCP: map[int]string{80: "open", 443: "closed"}}},
	}}, "finished")

	rows, err := List(context.Background(), path, readDeps(started.Add(time.Minute)))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("List returned %d rows, want one: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.ID != request.Key[:8] || row.Targets != "targets.txt: 192.0.2.10, 192.0.2.11" || row.TCP != "80,443" || row.Runs != 1 || row.Rounds != 1 || row.State != "finished" || row.Detail != "" {
		t.Fatalf("request row = %+v", row)
	}
	if row.LastRound == nil || !row.LastRound.Equal(started) {
		t.Fatalf("LastRound = %v, want %v", row.LastRound, started)
	}
}

func TestListPrefersNewestGenuinelyLiveRunAndIncludesZeroRoundRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader-live.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	seedReadHistory(t, path, request, "reader-old-live", started, nil, nil, "running")
	newer := started.Add(10 * time.Second)
	seedReadHistory(t, path, request, "reader-new-finished", newer, nil, []history.RoundInput{{
		Started: newer, Observations: ".",
	}}, "finished")
	rows, err := List(context.Background(), path, readDeps(started.Add(20*time.Second)))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Runs != 2 || rows[0].Rounds != 1 || rows[0].State != "running" || rows[0].Detail != "one round, in progress" {
		t.Fatalf("List did not select the older active zero-round run: %+v", rows)
	}
}

func TestReadReconstructsStoredRequestAndSparseLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10", "192.0.2.11"}, []int{443})
	seedReadHistory(t, path, request, "read-show-run", started, nil, []history.RoundInput{{
		Started: started, Observations: "!.", Rows: []history.ResultRow{{Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started, TCP: map[int]string{443: "open"}}},
	}}, "finished")

	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: strings.ToUpper(request.Key[:8])}, readDeps(started.Add(time.Minute)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !out.Found || out.Running || out.Request.Key != request.Key || len(out.Request.Hosts) != 2 {
		t.Fatalf("read identity/found/running = %q/%t/%t", out.Request.Key, out.Found, out.Running)
	}
	if out.Run.CurrentRounds != 1 || out.Run.EarlierRounds != 0 || len(out.Run.Latest) != 2 || len(out.Run.Seen) != 1 {
		t.Fatalf("read aggregate = rounds %d/%d latest=%+v seen=%+v", out.Run.CurrentRounds, out.Run.EarlierRounds, out.Run.Latest, out.Run.Seen)
	}
	noResponse, _ := ResultText(NoResponse)
	if out.Run.Latest[0].Host != request.Hosts[0] || out.Run.Latest[1].Host != request.Hosts[1] || out.Run.Latest[1].Row.Result != noResponse {
		t.Fatalf("sparse full latest vector = %+v", out.Run.Latest)
	}
	if got := ReadExitCode(out); got != 0 {
		t.Fatalf("ReadExitCode = %d, want 0 for seen requested open TCP", got)
	}
}

func TestReadOpenPeriodRequiresLatestRoundOwnerToBeLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader-owner.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	ownerRound := history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started,
	}}}
	seedReadHistory(t, path, request, "reader-ended-owner", started, nil, []history.RoundInput{ownerRound}, "finished")
	newer := started.Add(time.Second)
	seedReadHistory(t, path, request, "reader-new-live", newer, nil, nil, "running")
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8]}, readDeps(newer.Add(time.Second)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !out.Running || len(out.Run.Seen) != 1 || out.Run.Seen[0].Ongoing {
		t.Fatalf("live run/last-round-owner state = Running %t Seen %+v", out.Running, out.Run.Seen)
	}
}

func TestReadMissingPrefixIsNotHistoryFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader.db")
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	seedReadHistory(t, path, request, "read-prefix-run", time.Now().UTC(), nil, nil, "finished")

	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: strings.Repeat("f", 8)}, readDeps(time.Now().UTC()))
	if err != nil || out.Found || out.Problem == "" || ReadExitCode(out) != 2 {
		t.Fatalf("missing prefix outcome=%+v err=%v exit=%d", out, err, ReadExitCode(out))
	}
}

func TestListMissingHistoryDoesNotCreateIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "history.db")
	rows, err := List(context.Background(), path, readDeps(time.Now().UTC()))
	if err != nil || len(rows) != 0 {
		t.Fatalf("List missing history = %+v, err=%v", rows, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("List created missing history path: stat err=%v", err)
	}
}

func TestReadExitCodeUsesSelectedReachabilityOnly(t *testing.T) {
	if got := ReadExitCode(ReadOutcome{Found: true, Run: RunOutcome{TCPRequired: true, Stats: Stats{Seen: 1}, ProbeFailed: true}}); got != 1 {
		t.Fatalf("closed-only selected history exit = %d, want 1", got)
	}
	if got := ReadExitCode(ReadOutcome{Found: true, Run: RunOutcome{TCPRequired: true, TCPOpen: true, ProbeFailed: true}}); got != 0 {
		t.Fatalf("open selected history exit = %d, want 0", got)
	}
	if got := ReadExitCode(ReadOutcome{Found: true, Run: RunOutcome{Stats: Stats{Seen: 1}}}); got != 0 {
		t.Fatalf("seen no-port history exit = %d, want 0", got)
	}
	if got := ReadExitCode(ReadOutcome{Found: true}); got != 1 {
		t.Fatalf("empty no-port history exit = %d, want 1", got)
	}
}

func TestRunningDetailUsesOverflowSafeSlotArithmetic(t *testing.T) {
	start := time.Date(1900, 1, 1, 23, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	interval := 9999 * 7 * 24 * time.Hour
	detail, err := runningDetail(history.RunInfo{RunStart: history.RunStart{Started: start, Interval: &interval}, Rounds: 1}, now)
	if err != nil || !strings.HasPrefix(detail, "round 1 until stopped, next at ") {
		t.Fatalf("large interval detail=%q err=%v", detail, err)
	}
	zero := time.Duration(0)
	if _, err := runningDetail(history.RunInfo{RunStart: history.RunStart{Started: start, Interval: &zero}}, now); err == nil {
		t.Fatal("zero running interval was accepted")
	}
}

func TestRunningDetailUsesCommittedRoundAndExclusiveUntilBoundary(t *testing.T) {
	start := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	interval := 10 * time.Second
	until := start.Add(30 * time.Second)
	run := history.RunInfo{RunStart: history.RunStart{Started: start, Interval: &interval, Until: &until}, Rounds: 1}
	got, err := runningDetail(run, start.Add(25*time.Second))
	if err != nil || got != "round 1 of up to 3" {
		t.Fatalf("at 12:00:25 detail=%q err=%v, want committed count and no exclusive-until slot", got, err)
	}
	got, err = runningDetail(run, start.Add(20*time.Second))
	if err != nil || got != "round 1 of up to 3, next at 12:00:20" {
		t.Fatalf("at exact slot detail=%q err=%v, want next slot retained at 12:00:20", got, err)
	}
	got, err = runningDetail(run, start.Add(-25*time.Second))
	if err != nil || got != "round 1 of up to 3, next at 11:59:40" {
		t.Fatalf("before started detail=%q err=%v, want signed ceil next slot at 11:59:40", got, err)
	}
}

func TestRequestRowPreservesRawStoppedState(t *testing.T) {
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	summary := history.RequestSummary{RequestMeta: history.RequestMeta{Key: strings.Repeat("a", 64)}}
	run := &history.RunInfo{RunStart: history.RunStart{ID: "stopped", Started: started}, Status: "stopped"}
	row, err := requestRow(summary, run, started, readDeps(started))
	if err != nil || row.State != "stopped" || row.Detail != "" {
		t.Fatalf("raw stopped request row=%+v err=%v", row, err)
	}
}

func TestReadUsesOneViewAcrossPagesAndPublishesOnlyAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader-pages.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	rounds := make([]history.RoundInput, 205)
	for i := range rounds {
		stamp := started.Add(time.Duration(i) * time.Second)
		rounds[i] = history.RoundInput{Started: stamp, Observations: "!", Rows: []history.ResultRow{{
			Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: stamp,
		}}}
	}
	seedReadHistory(t, path, request, "read-paged-run", started, nil, rounds, "finished")
	since := started.Add(101 * time.Second)
	for _, mode := range []string{"success", "view-error", "close-error"} {
		t.Run(mode, func(t *testing.T) {
			reader, err := history.OpenReader(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			observed := &readObservedReader{Reader: reader}
			marker := errors.New("injected reader failure")
			if mode == "view-error" {
				observed.viewErr = marker
			} else if mode == "close-error" {
				observed.closeErr = marker
			}
			deps := readDeps(started.Add(210 * time.Second))
			deps.OpenHistory = func(context.Context, string) (Reader, error) { return observed, nil }
			out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Since: &since}, deps)
			if observed.views != 1 || observed.closed != 1 {
				t.Fatalf("view/close count=%d/%d, want exactly one each", observed.views, observed.closed)
			}
			if mode != "success" {
				if !errors.Is(err, marker) || out.Found || len(out.Run.Seen) != 0 || len(out.Run.Latest) != 0 {
					t.Fatalf("error published partial reader outcome=%+v err=%v", out, err)
				}
				return
			}
			if err != nil || out.Run.CurrentRounds != 104 || len(out.Run.Seen) != 1 || !out.Run.Seen[0].FirstSeen.Equal(since) {
				t.Fatalf("selected paged aggregate=%+v err=%v", out.Run, err)
			}
		})
	}
}
