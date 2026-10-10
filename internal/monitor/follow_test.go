// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
)

type followCountReader struct {
	Reader
	views  int
	closed int
	after  func()
	before func(int)
}

func (r *followCountReader) View(ctx context.Context, callback func(*history.Snapshot) error) error {
	r.views++
	if r.before != nil {
		r.before(r.views)
	}
	err := r.Reader.View(ctx, callback)
	if r.after != nil {
		r.after()
	}
	return err
}

type followStepClock struct {
	wall time.Time
	mono time.Duration
}

func (c *followStepClock) Now() ClockReading {
	return ClockReading{Wall: c.wall.Add(c.mono), Mono: c.mono}
}
func (c *followStepClock) After(delay time.Duration) <-chan time.Time {
	c.mono += delay
	ready := make(chan time.Time, 1)
	ready <- c.wall.Add(c.mono)
	return ready
}

func (r *followCountReader) Close() error {
	r.closed++
	return r.Reader.Close()
}

func openFollowReader(t *testing.T, path string) *followCountReader {
	t.Helper()
	reader, err := history.OpenReader(context.Background(), path)
	if err != nil || reader == nil {
		t.Fatalf("OpenReader: reader=%v err=%v", reader, err)
	}
	return &followCountReader{Reader: reader}
}

func TestFollowTransitionsOnceBeforeDrawAndPerformsFinalRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	seedReadHistory(t, path, request, "follow-open-run", started, nil, nil, "running")
	reader := openFollowReader(t, path)
	stop := make(chan struct{})
	deps := readDeps(started.Add(time.Second))
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.Stop = stop
	order := []string{}
	deps.EnterFollowing = func() {
		order = append(order, "enter")
		close(stop)
	}
	deps.Live = &LiveHooks{Draw: func(update LiveUpdate) {
		order = append(order, "draw")
		if update.Phase != LiveFollowing && update.Phase != LiveEnded {
			t.Errorf("follow phase = %v", update.Phase)
		}
	}}

	out, err := followRead(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil {
		t.Fatalf("followRead: %v", err)
	}
	if len(order) != 3 || order[0] != "enter" || order[1] != "draw" || order[2] != "draw" {
		t.Fatalf("transition/draw order = %v, want enter then initial and ended frames", order)
	}
	if reader.views != 2 || reader.closed != 1 {
		t.Fatalf("follow views/close=%d/%d, want initial+final read and one close", reader.views, reader.closed)
	}
	if !out.Found || !out.Running {
		t.Fatalf("final follow outcome found/running=%t/%t", out.Found, out.Running)
	}
}

func TestFollowNoLiveRunDoesNotEnterFollowing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-no-live.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	seedReadHistory(t, path, request, "follow-finished-run", started, nil, nil, "finished")
	reader := openFollowReader(t, path)
	deps := readDeps(started.Add(time.Second))
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	entered, noted := 0, 0
	deps.EnterFollowing = func() { entered++ }
	deps.Note = func(string) { noted++ }
	out, err := followRead(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil || entered != 0 || noted != 1 || reader.views != 1 || reader.closed != 1 || !out.Found || out.Running {
		t.Fatalf("no-live follow outcome=%+v err=%v entered=%d noted=%d views=%d closed=%d", out, err, entered, noted, reader.views, reader.closed)
	}
}

func TestFollowReturnsAfterSuccessfulPollWhenAllRunsEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-ended.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	endedRound := history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started,
	}}}
	seedReadHistory(t, path, request, "follow-ended-run", started, nil, []history.RoundInput{endedRound}, "running")
	reader := openFollowReader(t, path)
	reader.before = func(view int) {
		if view != 2 {
			return
		}
		writer, err := history.OpenWriter(context.Background(), path)
		if err != nil {
			t.Fatalf("open ending writer: %v", err)
		}
		if err := writer.EndRun(context.Background(), "follow-ended-run", started.Add(time.Second), "finished"); err != nil {
			t.Fatalf("finish run: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	clock := &followStepClock{wall: started}
	deps := readDeps(started)
	deps.Clock = clock
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.EnterFollowing = func() {}
	frames := []LivePhase{}
	deps.Live = &LiveHooks{Draw: func(update LiveUpdate) { frames = append(frames, update.Phase) }}
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil || !out.Found || out.Running || reader.views != 2 || reader.closed != 1 {
		t.Fatalf("ended follow outcome=%+v err=%v views=%d closed=%d", out, err, reader.views, reader.closed)
	}
	if len(frames) == 0 || frames[len(frames)-1] != LiveEnded {
		t.Fatalf("final phases = %v, want LiveEnded", frames)
	}
}

func TestFollowTransitionDoesNotDependOnDrawing(t *testing.T) {
	for _, mode := range []string{"absent", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "follow-draw-"+mode+".db")
			started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			request := readRequest(t, []string{"192.0.2.10"}, nil)
			seedReadHistory(t, path, request, "follow-draw-run", started, nil, nil, "running")
			reader := openFollowReader(t, path)
			stop := make(chan struct{})
			deps := readDeps(started.Add(time.Second))
			deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
			deps.Stop = stop
			entered, draws := 0, 0
			deps.EnterFollowing = func() { entered++; close(stop) }
			if mode == "disabled" {
				deps.Live = &LiveHooks{Enabled: func() bool { return false }, Draw: func(LiveUpdate) { draws++ }}
			}
			out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
			if err != nil || entered != 1 || draws != 0 || !out.Found {
				t.Fatalf("mode=%s outcome=%+v err=%v entered=%d draws=%d", mode, out, err, entered, draws)
			}
		})
	}
}

func TestFollowChecksHardContextBeforeTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-canceled.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	seedReadHistory(t, path, request, "follow-canceled-run", started, nil, nil, "running")
	reader := openFollowReader(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	reader.after = cancel
	deps := readDeps(started.Add(time.Second))
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	entered := 0
	deps.EnterFollowing = func() { entered++ }
	out, err := followRead(ctx, ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err == nil || entered != 0 || out.Found || reader.views != 1 || reader.closed != 1 {
		t.Fatalf("hard-canceled follow outcome=%+v err=%v entered=%d views=%d closed=%d", out, err, entered, reader.views, reader.closed)
	}
}

func TestFollowLateCommitChangeIsEmittedOnceAcrossFinalRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-late-commit.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	interval := 10 * time.Second
	seedReadHistory(t, path, request, "follow-late-run", started, &interval, []history.RoundInput{
		{Started: started, Observations: "."},
		{Started: started.Add(20 * time.Second), Observations: "."},
	}, "running")
	reader := openFollowReader(t, path)
	stop := make(chan struct{})
	reader.before = func(view int) {
		if view != 2 {
			return
		}
		writer, err := history.OpenWriter(context.Background(), path)
		if err != nil {
			t.Fatalf("open concurrent writer: %v", err)
		}
		round := history.RoundInput{Started: started.Add(10 * time.Second), Observations: "!", Rows: []history.ResultRow{{Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started.Add(10 * time.Second)}}}
		if _, err := writer.RecordRound(context.Background(), "follow-late-run", round); err != nil {
			t.Fatalf("commit late earlier-started round: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close concurrent writer: %v", err)
		}
	}
	clock := &followStepClock{wall: started}
	deps := readDeps(started)
	deps.Clock = clock
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.Stop = stop
	deps.EnterFollowing = func() {}
	changes := []Change{}
	deps.Live = &LiveHooks{Change: func(change Change) {
		changes = append(changes, change)
		if len(changes) == 1 {
			close(stop)
		}
	}}
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil {
		t.Fatalf("Read follow: %v", err)
	}
	if len(changes) != 1 || changes[0].Host != request.Hosts[0] || !changes[0].Seen {
		t.Fatalf("change notes = %+v, want the one newly committed observation exactly once", changes)
	}
	if reader.views != 3 || reader.closed != 1 {
		t.Fatalf("view/close count = %d/%d, want initial, changed poll, final poll and one close", reader.views, reader.closed)
	}
	if out.Run.CurrentRounds != 3 || out.Run.Stats.Rounds != 3 || len(out.Run.Seen) != 1 {
		t.Fatalf("final late-commit aggregate = %+v", out.Run)
	}
}

func TestFollowNewLatestRoundOwnerControlsPeriodLiveness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-owner.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	now := started.Add(20 * time.Second)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	first := history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started,
	}}}
	seedReadHistory(t, path, request, "follow-old-owner", started, nil, []history.RoundInput{first}, "running")
	seedReadHistory(t, path, request, "follow-new-owner", started.Add(10*time.Second), nil, nil, "running")
	reader := openFollowReader(t, path)
	reader.before = func(view int) {
		if view != 2 {
			return
		}
		writer, err := history.OpenWriter(context.Background(), path)
		if err != nil {
			t.Fatalf("open owner writer: %v", err)
		}
		if err := writer.EndRun(context.Background(), "follow-old-owner", now, "finished"); err != nil {
			t.Fatalf("finish previous owner: %v", err)
		}
		second := history.RoundInput{Started: now, Observations: "!", Rows: []history.ResultRow{{
			Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: now,
		}}}
		if _, err := writer.RecordRound(context.Background(), "follow-new-owner", second); err != nil {
			t.Fatalf("commit new owner's round: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	clock := &followStepClock{wall: now}
	deps := readDeps(now)
	deps.Clock = clock
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.Stop = stop
	deps.EnterFollowing = func() { close(stop) }
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil {
		t.Fatalf("Read follow: %v", err)
	}
	if !out.Running || len(out.Run.Seen) != 1 || !out.Run.Seen[0].Ongoing || out.Run.CurrentRounds != 2 {
		t.Fatalf("new latest owner liveness = running %t rounds %d seen %+v", out.Running, out.Run.CurrentRounds, out.Run.Seen)
	}
}

func TestFollowDoesNotReusePreviousOwnerLivenessForNewEndedOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-ended-new-owner.db")
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	now := started.Add(20 * time.Second)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	first := history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started,
	}}}
	seedReadHistory(t, path, request, "follow-live-old-owner", started, nil, []history.RoundInput{first}, "running")
	seedReadHistory(t, path, request, "follow-ended-new-owner", started.Add(10*time.Second), nil, nil, "running")
	reader := openFollowReader(t, path)
	reader.before = func(view int) {
		if view != 2 {
			return
		}
		writer, err := history.OpenWriter(context.Background(), path)
		if err != nil {
			t.Fatalf("open owner writer: %v", err)
		}
		second := history.RoundInput{Started: now, Observations: "!", Rows: []history.ResultRow{{
			Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: now,
		}}}
		if _, err := writer.RecordRound(context.Background(), "follow-ended-new-owner", second); err != nil {
			t.Fatalf("commit new owner's round: %v", err)
		}
		if err := writer.EndRun(context.Background(), "follow-ended-new-owner", now, "finished"); err != nil {
			t.Fatalf("finish new owner: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	clock := &followStepClock{wall: now}
	deps := readDeps(now)
	deps.Clock = clock
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.Stop = stop
	deps.EnterFollowing = func() { close(stop) }
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil {
		t.Fatalf("Read follow: %v", err)
	}
	if !out.Running || len(out.Run.Seen) != 1 || out.Run.Seen[0].Ongoing || out.Run.CurrentRounds != 2 {
		t.Fatalf("ended latest owner liveness = running %t rounds %d seen %+v", out.Running, out.Run.CurrentRounds, out.Run.Seen)
	}
}

func TestFollowPruneWithoutNewCommitRebuildsWithoutRepeatingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-prune.db")
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	newer := started.Add(14 * 24 * time.Hour)
	now := started.Add(20 * 24 * time.Hour)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	rounds := []history.RoundInput{
		{Started: started, Observations: "!", Rows: []history.ResultRow{{Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started}}},
		{Started: newer, Observations: "!", Rows: []history.ResultRow{{Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: newer}}},
	}
	seedReadHistory(t, path, request, "follow-prune-run", started, nil, rounds, "running")
	writer, err := history.OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Heartbeat(context.Background(), "follow-prune-run", now, nil); err != nil {
		t.Fatalf("refresh run heartbeat: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader := openFollowReader(t, path)
	reader.before = func(view int) {
		if view != 2 {
			return
		}
		pruner, err := history.OpenWriter(context.Background(), path)
		if err != nil {
			t.Fatalf("open pruner: %v", err)
		}
		if _, err := pruner.Prune(context.Background(), now, big.NewInt(15)); err != nil {
			t.Fatalf("prune old selected round: %v", err)
		}
		if err := pruner.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	clock := &followStepClock{wall: now}
	deps := readDeps(now)
	deps.Clock = clock
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.Stop = stop
	deps.EnterFollowing = func() { close(stop) }
	changes := 0
	deps.Live = &LiveHooks{Change: func(Change) { changes++ }}
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil {
		t.Fatalf("Read follow after prune: %v", err)
	}
	if changes != 0 {
		t.Fatalf("prune rebuild repeated %d old-round changes", changes)
	}
	if reader.views != 2 || out.Run.CurrentRounds != 1 || out.Run.Stats.Rounds != 1 || len(out.Run.Seen) != 1 || out.Run.Seen[0].SeenRounds != 1 {
		t.Fatalf("pruned final view=%d aggregate=%+v", reader.views, out.Run)
	}
}

func TestFollowRebuildsAfterSameKeyRequestRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "follow-recreated.db")
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := started.Add(48 * time.Hour)
	request := readRequest(t, []string{"192.0.2.10"}, nil)
	oldRound := history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started,
	}}}
	seedReadHistory(t, path, request, "follow-recreated-old", started, nil, []history.RoundInput{oldRound}, "running")
	reader := openFollowReader(t, path)
	reader.before = func(view int) {
		if view != 2 {
			return
		}
		writer, err := history.OpenWriter(context.Background(), path)
		if err != nil {
			t.Fatalf("open recreate writer: %v", err)
		}
		if err := writer.EndRun(context.Background(), "follow-recreated-old", now, "finished"); err != nil {
			t.Fatalf("finish old run: %v", err)
		}
		if _, err := writer.Prune(context.Background(), now, big.NewInt(1)); err != nil {
			t.Fatalf("prune old request: %v", err)
		}
		storedRequest := history.Request{RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports}, Hosts: request.Hosts}
		if _, err := writer.BeginRun(context.Background(), storedRequest, history.RunStart{ID: "follow-recreated-new", Started: now, Heartbeat: now, Host: "reader-test", PID: 1234}); err != nil {
			t.Fatalf("recreate same-key request: %v", err)
		}
		round := history.RoundInput{Started: now, Observations: "!", Rows: []history.ResultRow{{Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: now}}}
		if _, err := writer.RecordRound(context.Background(), "follow-recreated-new", round); err != nil {
			t.Fatalf("record recreated request round: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	deps := readDeps(started.Add(time.Second))
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	deps.Stop = stop
	deps.EnterFollowing = func() { close(stop) }
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Follow: true}, deps)
	if err != nil {
		t.Fatalf("Read after request recreation: %v", err)
	}
	if reader.views != 2 || !out.Found || out.Request.Key != request.Key || out.Run.CurrentRounds != 1 || out.Run.Stats.Rounds != 1 || !out.Run.Stats.FirstRound.Equal(now) {
		t.Fatalf("same-key recreation result: views=%d outcome=%+v", reader.views, out)
	}
}
