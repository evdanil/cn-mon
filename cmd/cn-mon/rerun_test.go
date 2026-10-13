// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/probe"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestSelectRerunUsesNewestRunAndSavedNumericSchedule(t *testing.T) {
	database := filepath.Join(t.TempDir(), "history.db")
	request, _, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	seedRerunRequest(t, database, request, []history.RunStart{
		{ID: "older", Started: started, Heartbeat: started, Host: "seed", PID: 1},
		{ID: "newest-rowid", Started: started, Heartbeat: started, Host: "seed", PID: 1, Interval: rerunDuration(60 * time.Second), Until: rerunTime(started.Add(30 * time.Second)), Source: rerunString("removed-input.txt")},
	})

	selected, err := selectRerun(context.Background(), database, request.Key[:8], nil, nil, monitor.ReadDeps{})
	if err != nil {
		t.Fatalf("select newest run: %v", err)
	}
	if selected.request.Key != request.Key || fmt.Sprint(selected.request.Tokens) != fmt.Sprint(request.Tokens) || fmt.Sprint(selected.request.Hosts) != fmt.Sprint(request.Hosts) {
		t.Fatalf("restored request differs from saved identity: %#v", selected.request)
	}
	if selected.interval == nil || *selected.interval != 60*time.Second || selected.duration == nil || *selected.duration != 30*time.Second || selected.source == nil || *selected.source != "removed-input.txt" {
		t.Fatalf("selected saved run schedule/source = %#v", selected)
	}
	longer := 90 * time.Second
	selected, err = selectRerun(context.Background(), database, request.Key[:8], &longer, nil, monitor.ReadDeps{})
	if err != nil || selected.interval == nil || *selected.interval != longer || selected.duration == nil || *selected.duration != 30*time.Second {
		t.Fatalf("every-only override must preserve a shorter inherited duration: selected=%#v err=%v", selected, err)
	}
}

func TestSelectRerunOverrideProvenanceAndLegacySingleShot(t *testing.T) {
	database := filepath.Join(t.TempDir(), "history.db")
	request, _, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	seedRerunRequest(t, database, request, []history.RunStart{{ID: "single-shot", Started: started, Heartbeat: started, Host: "seed", PID: 1}})

	longer := 90 * time.Second
	selected, err := selectRerun(context.Background(), database, request.Key[:8], &longer, nil, monitor.ReadDeps{})
	if err != nil || selected.interval == nil || *selected.interval != longer || selected.duration != nil {
		t.Fatalf("every-only override should retain nil duration: selected=%#v err=%v", selected, err)
	}
	newDuration := 45 * time.Second
	selected, err = selectRerun(context.Background(), database, request.Key[:8], nil, &newDuration, monitor.ReadDeps{})
	if err != nil || selected.interval == nil || *selected.interval != time.Minute || selected.duration == nil || *selected.duration != newDuration {
		t.Fatalf("for-only should establish implicit 60-second interval: selected=%#v err=%v", selected, err)
	}
}

func TestRerunStoredScheduleValidationPreservesFractionsAndRejectsOverflow(t *testing.T) {
	started := time.Date(2026, 10, 10, 12, 0, 0, 123456789, time.UTC)
	fractionalEnd := started.Add(1500*time.Millisecond + 123*time.Microsecond)
	got, err := validatedStoredDuration(started, &fractionalEnd)
	if err != nil || got == nil || *got != 1500*time.Millisecond+123*time.Microsecond {
		t.Fatalf("fractional stored duration = %v, %v", got, err)
	}
	tooFar := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if _, err := validatedStoredDuration(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), &tooFar); err == nil {
		t.Fatal("unrepresentable stored duration accepted")
	}
	zero := time.Duration(0)
	if _, err := validatedStoredInterval(&zero); err == nil {
		t.Fatal("zero stored interval accepted")
	}
}

func TestRerunSelectionFailuresHaveNoLoggerOrHistorySideEffects(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "missing.db"), filepath.Join(root, "rerun.log")
	var stdout, stderr bytes.Buffer
	runtime := commandTestRuntime(root, logPath, monitor.RunDeps{})
	code := runCLIWithRuntime([]string{"--rerun", "deadbeef", "--history-file", database, "--log-file", logPath, "--format", "json"}, nil, &stdout, &stderr, runtime)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "no saved request matches") {
		t.Fatalf("missing rerun selection: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only history was created: stat error=%v", err)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logger opened before selection: stat error=%v", err)
	}
}

func TestRerunSnapshotCleanupFailureOverridesNoMatch(t *testing.T) {
	database := filepath.Join(t.TempDir(), "history.db")
	request, _, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	seedRerunRequest(t, database, request, []history.RunStart{{ID: "source", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
	closeFailure := errors.New("reader close failed")
	deps := monitor.ReadDeps{OpenHistory: func(ctx context.Context, path string) (monitor.Reader, error) {
		reader, err := openReadOnlyHistory(ctx, path)
		if err != nil {
			return nil, err
		}
		return rerunCloseErrorReader{Reader: reader, err: closeFailure}, nil
	}}
	_, err = selectRerun(context.Background(), database, "deadbeef", nil, nil, deps)
	if err == nil || errors.Is(err, closeFailure) == false {
		t.Fatalf("cleanup error did not override no-match result: %v", err)
	}
	var selectionFailure *rerunSelectionFailure
	if errors.As(err, &selectionFailure) {
		t.Fatalf("cleanup failure was classified as selection exit 2: %v", err)
	}
}

func TestRerunSelectionOutcomeMatrixFromSQLite(t *testing.T) {
	t.Run("ambiguous prefix", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "history.db")
		first, _, _ := target.Build([]string{"192.0.2.1"}, nil)
		second, _, _ := target.Build([]string{"192.0.2.2"}, nil)
		started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		seedRerunRequest(t, database, first, []history.RunStart{{ID: "one", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
		seedRerunRequest(t, database, second, []history.RunStart{{ID: "two", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
		mutateRerunDB(t, database, `UPDATE request SET key = CASE id WHEN 1 THEN ? ELSE ? END`, strings.Repeat("a", 64), strings.Repeat("a", 63)+"b")
		assertRerunPreflightCode(t, database, strings.Repeat("a", 4), 2, "ambiguous")
	})

	t.Run("request without runs", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "history.db")
		request, _, _ := target.Build([]string{"192.0.2.3"}, nil)
		started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		seedRerunRequest(t, database, request, []history.RunStart{{ID: "remove-me", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
		mutateRerunDB(t, database, `DELETE FROM run`)
		assertRerunPreflightCode(t, database, request.Key[:8], 2, "no stored run")
	})

	t.Run("valid empty batch", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "history.db")
		key := sha256.Sum256([]byte(`[[],[],[]]`))
		request := target.Request{Key: hex.EncodeToString(key[:]), Tokens: []string{}, Ports: []int{}, Hosts: []string{}}
		started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		seedRerunRequest(t, database, request, []history.RunStart{{ID: "empty", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
		assertRerunPreflightCode(t, database, request.Key[:8], 2, "no runnable targets")
	})

	for _, tc := range []struct {
		name, hosts string
	}{
		{name: "malformed host JSON", hosts: `not-json`},
		{name: "invalid probe host", hosts: ` ["not a host"] `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "history.db")
			request, _, _ := target.Build([]string{"192.0.2.4"}, nil)
			started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			seedRerunRequest(t, database, request, []history.RunStart{{ID: "corrupt", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
			mutateRerunDB(t, database, `UPDATE request SET hosts = ?`, tc.hosts)
			assertRerunPreflightCode(t, database, request.Key[:8], 3, "restore saved request")
		})
	}

	t.Run("key digest mismatch", func(t *testing.T) {
		database := filepath.Join(t.TempDir(), "history.db")
		request, _, _ := target.Build([]string{"192.0.2.5"}, nil)
		started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		seedRerunRequest(t, database, request, []history.RunStart{{ID: "bad-key", Started: started, Heartbeat: started, Host: "seed", PID: 1}})
		mutateRerunDB(t, database, `UPDATE request SET key = ?`, strings.Repeat("c", 64))
		assertRerunPreflightCode(t, database, strings.Repeat("c", 8), 3, "restore saved request")
	})

	t.Run("invalid schedule before overrides", func(t *testing.T) {
		for _, tc := range []struct {
			name, sql, override string
		}{
			{name: "zero interval", sql: `UPDATE run SET interval = 0`, override: "--every"},
			{name: "end not after start", sql: `UPDATE run SET until = started`, override: "--for"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				database := filepath.Join(t.TempDir(), "history.db")
				request, _, _ := target.Build([]string{"192.0.2.6"}, nil)
				started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
				seedRerunRequest(t, database, request, []history.RunStart{{ID: "bad-schedule", Started: started, Heartbeat: started, Host: "seed", PID: 1, Interval: rerunDuration(20 * time.Second), Until: rerunTime(started.Add(time.Minute))}})
				mutateRerunDB(t, database, tc.sql)
				assertRerunPreflightCodeWithArgs(t, database, request.Key[:8], 3, "restore saved request", tc.override, "30s")
			})
		}
	})
}

func TestRerunSQLiteNewestStatusAndRowIDSelection(t *testing.T) {
	for _, status := range []string{"running", "finished", "stopped", "failed"} {
		t.Run(status, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "history.db")
			request, _, _ := target.Build([]string{"192.0.2.7"}, nil)
			started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			seedRerunRequest(t, database, request, []history.RunStart{
				{ID: "older-row", Started: started, Heartbeat: started, Host: "seed", PID: 1},
				{ID: "newest-row", Started: started, Heartbeat: started, Host: "seed", PID: 1},
			})
			if status != "running" {
				ctx := context.Background()
				store, err := history.OpenWriter(ctx, database)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.EndRun(ctx, "newest-row", started.Add(time.Second), status); err != nil {
					_ = store.Close()
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			selected, err := selectRerun(context.Background(), database, request.Key[:8], nil, nil, monitor.ReadDeps{})
			if err != nil || selected.source != nil {
				t.Fatalf("select status %s: selected=%#v err=%v", status, selected, err)
			}
			reader, err := history.OpenReader(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if err := reader.View(context.Background(), func(snapshot *history.Snapshot) error {
				info, err := snapshot.Request(request.Key)
				if err != nil {
					return err
				}
				if info == nil || info.NewestRun == nil || info.NewestRun.ID != "newest-row" || info.NewestRun.Status != status || info.NewestRun.Rounds != 0 {
					return fmt.Errorf("newest run = %#v", info)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommandRerunPinnedPythonFixtureWithInjectedClockAndProbes(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "fixture.db")
	fixture := filepath.Join("..", "..", "internal", "history", "testdata", "python-source-v1", "cn-tool-schema1.db")
	contents, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	const key = "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff"
	clock := &rerunImmediateClock{now: time.Now().UTC().Truncate(time.Second)}
	origin := clock.now
	prober := &rerunFixtureProber{}
	runtime := commandTestRuntime(root, filepath.Join(root, "rerun.log"), monitor.RunDeps{Clock: clock, Prober: prober})
	var stdout, stderr bytes.Buffer
	args := []string{"--rerun", key[:8], "--every", "10s", "--for", "10s", "--since", "0m", "--history-file", database, "--history-days", "9999", "--log-file", filepath.Join(root, "rerun.log"), "--format", "json"}
	code := runCLIWithRuntime(args, nil, &stdout, &stderr, runtime)
	if code != 0 {
		t.Fatalf("fixture rerun exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	wantHosts := []string{"api.example.com", "db.example.com", "legacy.example.com", "timeout.example.com", "unreachable.example.com", "unresolved.example.com", "future.example.com"}
	if fmt.Sprint(prober.hosts) != fmt.Sprint(wantHosts) {
		t.Fatalf("fixture resolver hosts=%v, want stored order %v", prober.hosts, wantHosts)
	}
	var report struct {
		Latest []struct {
			Host   string `json:"host"`
			Result string `json:"result"`
		} `json:"latest"`
		Summary []struct {
			Rounds        int `json:"rounds"`
			EarlierRounds int `json:"earlier_rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode fixture rerun report: %v; stdout=%q", err, stdout.String())
	}
	if len(report.Latest) != len(wantHosts) || len(report.Summary) != 1 || report.Summary[0].Rounds != 1 || report.Summary[0].EarlierRounds != 0 {
		t.Fatalf("fixture rerun report summary/latest count mismatch: summary=%+v latest=%+v", report.Summary, report.Latest)
	}
	for i, row := range report.Latest {
		if row.Host != wantHosts[i] || row.Result != "OK" {
			t.Fatalf("latest row %d=%+v, want %q/OK", i, row, wantHosts[i])
		}
	}
	reader, err := history.OpenReader(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info == nil || info.Runs != 4 || info.NewestRun == nil || info.NewestRun.ID == "fixture-running-zero-round" || fmt.Sprint(info.Hosts) != fmt.Sprint(wantHosts) {
			return fmt.Errorf("fixture request metadata/runs = %#v", info)
		}
		if !info.NewestRun.Started.Equal(origin) || info.NewestRun.PID != int64(os.Getpid()) || info.NewestRun.Host == "fixture-linux-or-windows" || info.NewestRun.Interval == nil || *info.NewestRun.Interval != 10*time.Second || info.NewestRun.Until == nil || info.NewestRun.Until.Sub(info.NewestRun.Started) != 10*time.Second {
			return fmt.Errorf("new run metadata/schedule = %#v", info.NewestRun)
		}
		page, err := snapshot.Runs(info.ID, false, nil, 10)
		if err != nil {
			return err
		}
		foundOldActive, foundNewResults := false, false
		for _, run := range page.Items {
			if run.ID == "fixture-running-zero-round" && run.Status == "running" && run.Rounds == 0 {
				foundOldActive = true
			}
		}
		rounds, err := snapshot.Rounds(info.ID, history.RoundFilter{Limit: 100})
		if err != nil {
			return err
		}
		for _, round := range rounds.Items {
			if round.RunID == info.NewestRun.ID && equalHostSet(rowHosts(round.Rows), wantHosts) {
				foundNewResults = true
			}
		}
		if !foundOldActive || !foundNewResults {
			var details []string
			for _, round := range rounds.Items {
				details = append(details, fmt.Sprintf("run=%s hosts=%v", round.RunID, rowHosts(round.Rows)))
			}
			return fmt.Errorf("active source unchanged=%t new seven-host results=%t rounds=%v", foundOldActive, foundNewResults, details)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify fixture rerun history: %v", err)
	}
}

func TestCommandRerunSurvivesDeletionAfterSnapshotCloseAndRetention(t *testing.T) {
	for _, mode := range []string{"delete-after-close", "retention"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			database := filepath.Join(root, "history.db")
			request, _, err := target.Build([]string{"192.0.2.88"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			if mode == "retention" {
				started = time.Now().UTC().Add(-45 * 24 * time.Hour).Truncate(time.Second)
			}
			seedRerunRequest(t, database, request, []history.RunStart{{
				ID: "detached-source", Source: rerunString("historical-input.txt"), Started: started,
				Heartbeat: started, Host: "source-host", PID: 1,
			}})
			seedStore, err := history.OpenWriter(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			if err := seedStore.EndRun(context.Background(), "detached-source", started.Add(time.Second), "finished"); err != nil {
				_ = seedStore.Close()
				t.Fatal(err)
			}
			if err := seedStore.Close(); err != nil {
				t.Fatal(err)
			}

			runtime := commandTestRuntime(root, filepath.Join(root, "rerun.log"), monitor.RunDeps{Clock: &rerunImmediateClock{now: time.Now().UTC().Truncate(time.Second)}, Prober: commandTestProber{}})
			deletedAfterClose := false
			if mode == "delete-after-close" {
				runtime.readDeps.OpenHistory = func(ctx context.Context, path string) (monitor.Reader, error) {
					reader, err := openReadOnlyHistory(ctx, path)
					if err != nil {
						return nil, err
					}
					return rerunDeleteAfterCloseReader{Reader: reader, path: path, key: request.Key, didDelete: &deletedAfterClose}, nil
				}
			}
			args := []string{"--rerun", request.Key[:8], "--history-file", database, "--history-days", "30", "--log-file", filepath.Join(root, "rerun.log"), "--format", "json", "--since", "0m"}
			var stdout, stderr bytes.Buffer
			code := runCLIWithRuntime(args, nil, &stdout, &stderr, runtime)
			if code != 0 {
				t.Fatalf("rerun after %s exit=%d stderr=%q stdout=%q", mode, code, stderr.String(), stdout.String())
			}
			if mode == "delete-after-close" && !deletedAfterClose {
				t.Fatal("test did not delete the source after the read snapshot closed")
			}
			reader, err := history.OpenReader(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if err := reader.View(context.Background(), func(snapshot *history.Snapshot) error {
				info, err := snapshot.Request(request.Key)
				if err != nil {
					return err
				}
				if info == nil || info.Runs != 1 || info.NewestRun == nil || info.NewestRun.ID == "detached-source" || info.NewestRun.Source == nil || *info.NewestRun.Source != "historical-input.txt" {
					return fmt.Errorf("detached rerun was not recreated with provenance: %#v", info)
				}
				return nil
			}); err != nil {
				t.Fatalf("verify %s rerun: %v", mode, err)
			}
		})
	}
}

type rerunDeleteAfterCloseReader struct {
	monitor.Reader
	path      string
	key       string
	didDelete *bool
}

func (r rerunDeleteAfterCloseReader) Close() error {
	if err := r.Reader.Close(); err != nil {
		return err
	}
	store, err := history.OpenWriter(context.Background(), r.path)
	if err != nil {
		return err
	}
	_, deleteErr := store.DeleteRequest(context.Background(), r.key, history.DeleteGuard{})
	closeErr := store.Close()
	if deleteErr != nil {
		return deleteErr
	}
	if closeErr != nil {
		return closeErr
	}
	*r.didDelete = true
	return nil
}

func equalHostSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]struct{}, len(got))
	for _, host := range got {
		set[host] = struct{}{}
	}
	for _, host := range want {
		if _, ok := set[host]; !ok {
			return false
		}
	}
	return true
}

func rowHosts(rows []history.ResultRow) []string {
	hosts := make([]string, len(rows))
	for i := range rows {
		hosts[i] = rows[i].Host
	}
	return hosts
}

type rerunImmediateClock struct {
	mu   sync.Mutex
	now  time.Time
	mono time.Duration
}

func (c *rerunImmediateClock) Now() monitor.ClockReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	return monitor.ClockReading{Wall: c.now, Mono: c.mono}
}

func (c *rerunImmediateClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mono += delay
	now := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

type rerunFixtureProber struct{ hosts []string }

func (p *rerunFixtureProber) Resolve(_ context.Context, hosts []string) ([]probe.Resolved, error) {
	p.hosts = append([]string(nil), hosts...)
	resolved := make([]probe.Resolved, len(hosts))
	for i, host := range hosts {
		resolved[i] = probe.Resolved{Index: i, Host: host, Address: netip.MustParseAddr("192.0.2.1")}
	}
	return resolved, nil
}

func (p *rerunFixtureProber) ProbeBatch(_ context.Context, destinations []probe.Resolved, _ []int) ([]probe.Result, error) {
	results := make([]probe.Result, len(destinations))
	for i, destination := range destinations {
		results[i] = probe.Result{Target: destination, ICMP: probe.ICMPResult{State: "OK", Sent: 1, Received: 1}}
	}
	return results, nil
}

func assertRerunPreflightCode(t *testing.T, database, prefix string, code int, diagnostic string) {
	t.Helper()
	assertRerunPreflightCodeWithArgs(t, database, prefix, code, diagnostic)
}

func assertRerunPreflightCodeWithArgs(t *testing.T, database, prefix string, code int, diagnostic string, extra ...string) {
	t.Helper()
	root := filepath.Dir(database)
	logPath := filepath.Join(root, "preflight.log")
	args := []string{"--rerun", prefix, "--history-file", database, "--log-file", logPath, "--format", "json"}
	args = append(args, extra...)
	var stdout, stderr bytes.Buffer
	runtime := commandTestRuntime(root, logPath, monitor.RunDeps{})
	got := runCLIWithRuntime(args, nil, &stdout, &stderr, runtime)
	if got != code || stdout.Len() != 0 || !strings.Contains(stderr.String(), diagnostic) {
		t.Fatalf("rerun preflight code=%d want=%d stdout=%q stderr=%q", got, code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight opened logger: %v", err)
	}
}

func mutateRerunDB(t *testing.T, database, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite3", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("mutate SQLite fixture with %q: %v", query, err)
	}
}

type rerunCloseErrorReader struct {
	monitor.Reader
	err error
}

func (r rerunCloseErrorReader) Close() error {
	_ = r.Reader.Close()
	return r.err
}

func TestCommandRerunUsesSharedRunnerAndDoesNotReadSourceOrStdin(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "rerun.log")
	request, _, err := target.Build([]string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	seedRerunRequest(t, database, request, []history.RunStart{{
		ID: "still-active-source", Source: rerunString("removed-input.txt"), Started: started,
		Heartbeat: started, Host: "seed-host", PID: 1,
	}})
	stdin := &rerunCountingReader{reader: strings.NewReader("192.0.2.99\n")}
	var stdout, stderr bytes.Buffer
	runtime := commandTestRuntime(root, logPath, monitor.RunDeps{Prober: commandTestProber{}})
	code := runCLIWithRuntime([]string{"--rerun", request.Key[:8], "--history-file", database, "--log-file", logPath, "--format", "json", "--since", "0m"}, stdin, &stdout, &stderr, runtime)
	if code != 0 {
		t.Fatalf("rerun exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdin.reads != 0 {
		t.Fatalf("rerun read stdin %d times", stdin.reads)
	}
	reader, err := history.OpenReader(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	err = reader.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Key != request.Key || info.Runs != 2 || info.NewestRun == nil {
			return fmt.Errorf("rerun did not append to the saved request: %#v", info)
		}
		if info.NewestRun.ID == "still-active-source" || info.NewestRun.Source == nil || *info.NewestRun.Source != "removed-input.txt" {
			return fmt.Errorf("new run identity/source = %#v", info.NewestRun)
		}
		if info.NewestRun.Interval != nil || info.NewestRun.Until != nil {
			return fmt.Errorf("single-shot source schedule changed: %#v", info.NewestRun)
		}
		page, err := snapshot.Runs(info.ID, false, nil, 10)
		if err != nil {
			return err
		}
		for _, run := range page.Items {
			if run.ID == "still-active-source" {
				if run.Status != "running" || run.Rounds != 0 {
					return fmt.Errorf("active source changed: %#v", run)
				}
				return nil
			}
		}
		return errors.New("active source run disappeared")
	})
	if err != nil {
		t.Fatalf("verify rerun history: %v", err)
	}
}

type rerunCountingReader struct {
	reader *strings.Reader
	reads  int
}

func (r *rerunCountingReader) Read(buffer []byte) (int, error) {
	r.reads++
	return r.reader.Read(buffer)
}

func seedRerunRequest(t *testing.T, database string, request target.Request, runs []history.RunStart) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := history.OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open rerun fixture: %v", err)
	}
	for _, run := range runs {
		if _, err := store.BeginRun(ctx, history.Request{RequestMeta: history.RequestMeta{
			Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports,
		}, Hosts: request.Hosts}, run); err != nil {
			_ = store.Close()
			t.Fatalf("seed rerun run %q: %v", run.ID, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close rerun fixture: %v", err)
	}
}

func rerunDuration(value time.Duration) *time.Duration { return &value }
func rerunTime(value time.Time) *time.Time             { return &value }
func rerunString(value string) *string                 { return &value }
