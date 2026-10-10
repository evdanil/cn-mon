package history

import (
	"testing"
	"time"
)

func TestRunStateUsesStrictHeartbeatAndIntervalThreshold(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	interval := 10 * time.Second
	state := func(heartbeat time.Time) string {
		return RunState(RunInfo{
			RunStart: RunStart{Heartbeat: heartbeat, Interval: &interval, Host: "host", PID: 7},
			Status:   "running",
		}, now, "other-host", nil)
	}

	if got := state(now.Add(-60 * time.Second)); got != "running" {
		t.Fatalf("at the strict 60-second boundary: got %q, want running", got)
	}
	if got := state(now.Add(-60*time.Second - time.Millisecond)); got != "lost" {
		t.Fatalf("past the strict 60-second boundary: got %q, want lost", got)
	}
}

func TestRunStateLargeIntervalDoesNotOverflowDurationArithmetic(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	heartbeat := time.Date(1726, 10, 10, 12, 0, 0, 0, time.UTC)
	interval := time.Duration(9999 * 7 * 24 * float64(time.Hour))
	got := RunState(RunInfo{
		RunStart: RunStart{Heartbeat: heartbeat, Interval: &interval, Host: "remote", PID: 1},
		Status:   "running",
	}, now, "local", nil)
	if got != "running" {
		t.Fatalf("large supported interval state = %q, want running", got)
	}

	// More than the large interval's 2.5x threshold is stale, even though the
	// elapsed time is beyond time.Duration's saturating Sub range.
	oldHeartbeat := time.Date(1426, 10, 10, 12, 0, 0, 0, time.UTC)
	run := RunInfo{
		RunStart: RunStart{Heartbeat: oldHeartbeat, Interval: &interval, Host: "remote", PID: 1},
		Status:   "running",
	}
	if got := RunState(run, now, "local", nil); got != "lost" {
		t.Fatalf("elapsed time beyond large interval threshold = %q, want lost", got)
	}
}

func TestRunStatePIDChecksAreConservativeAndConditional(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	interval := time.Second
	base := RunInfo{
		RunStart: RunStart{Heartbeat: now, Interval: &interval, Host: "local", PID: 42},
		Status:   "running",
	}

	calls := 0
	check := func(state PIDState) func(int64) PIDState {
		return func(int64) PIDState { calls++; return state }
	}
	if got := RunState(base, now, "local", check(PIDUnknown)); got != "running" || calls != 1 {
		t.Fatalf("unknown PID state: got %q after %d checks", got, calls)
	}
	calls = 0
	if got := RunState(base, now, "local", check(PIDAlive)); got != "running" || calls != 1 {
		t.Fatalf("alive PID state: got %q after %d checks", got, calls)
	}
	calls = 0
	if got := RunState(base, now, "local", func(int64) PIDState { calls++; return PIDDead }); got != "lost" || calls != 1 {
		t.Fatalf("dead PID state: got %q after %d checks", got, calls)
	}
	calls = 0
	base.Host = "remote"
	if got := RunState(base, now, "local", func(int64) PIDState { calls++; return PIDDead }); got != "running" || calls != 0 {
		t.Fatalf("foreign-host PID state: got %q after %d checks", got, calls)
	}
	calls = 0
	base.Host = "local"
	base.Heartbeat = now.Add(-61 * time.Second)
	if got := RunState(base, now, "local", func(int64) PIDState { calls++; return PIDDead }); got != "lost" || calls != 0 {
		t.Fatalf("stale heartbeat PID state: got %q after %d checks", got, calls)
	}
	calls = 0
	for _, status := range []string{"finished", "stopped", "failed", "custom"} {
		base.Status = status
		calls = 0
		if got := RunState(base, now, "local", func(int64) PIDState { calls++; return PIDDead }); got != status || calls != 0 {
			t.Errorf("finalized status %q: got %q after %d checks", status, got, calls)
		}
	}
}

func TestRunStateHandlesYearOneHeartbeat(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	run := RunInfo{RunStart: RunStart{Heartbeat: time.Time{}, Host: "remote"}, Status: "running"}
	if got := RunState(run, now, "local", nil); got != "lost" {
		t.Fatalf("year-one stale heartbeat = %q, want lost", got)
	}
}

func TestRunStateKeepsIndeterminateStoredValuesRunning(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	badInterval := -time.Second
	for _, tc := range []struct {
		name string
		run  RunInfo
	}{
		{
			name: "unsupported heartbeat",
			run:  RunInfo{RunStart: RunStart{Heartbeat: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Host: "local"}, Status: "running"},
		},
		{
			name: "invalid interval",
			run:  RunInfo{RunStart: RunStart{Heartbeat: now.Add(-time.Hour), Interval: &badInterval, Host: "local"}, Status: "running"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidChecks := 0
			got := RunState(tc.run, now, "local", func(int64) PIDState { pidChecks++; return PIDDead })
			if got != "running" || pidChecks != 0 {
				t.Fatalf("indeterminate row = %q after %d PID checks, want running without a PID check", got, pidChecks)
			}
		})
	}
}
