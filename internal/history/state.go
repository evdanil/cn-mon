// RunState adapts cn-tool's derived run-liveness logic from
// cn_tool/utils/ping_history.py:433-449 at cn-tool 0.8.0,
// commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa. It uses the history
// package's checked epoch/duration conversions and never persists the result.
package history

import (
	"math"
	"time"
)

const (
	minimumStaleAge = 60 * time.Second
	statusRunning   = "running"
	statusLost      = "lost"
)

// RunState reports whether a persisted running row is still plausibly active.
// It does not write the derived state back to history.
func RunState(run RunInfo, now time.Time, thisHost string, pidState func(int64) PIDState) string {
	if run.Status != statusRunning {
		return run.Status
	}

	nowSeconds, err := epochSeconds(now)
	if err != nil {
		return statusRunning
	}
	heartbeatSeconds, err := epochSeconds(run.Heartbeat)
	if err != nil {
		return statusRunning
	}
	intervalSeconds := float64(0)
	if run.Interval != nil {
		intervalSeconds, err = durationSeconds(*run.Interval)
		if err != nil {
			return statusRunning
		}
	}
	staleAfter := math.Max(2.5*intervalSeconds, minimumStaleAge.Seconds())
	if nowSeconds-heartbeatSeconds > staleAfter {
		return statusLost
	}
	if run.Host == thisHost && pidState != nil && pidState(run.PID) == PIDDead {
		return statusLost
	}
	return statusRunning
}
