//go:build linux

// Local PID classification adapts the POSIX branch of
// cn_tool/utils/oscompat.py in cn-tool 0.8.0, commit
// 2e2f3c40ef6718486f1ca4477f9145029ec495fa.
package history

import (
	"errors"
	"math"
	"syscall"
)

// LocalPIDState checks a local PID without sending a signal.
func LocalPIDState(pid int64) PIDState {
	if pid <= 0 || pid > math.MaxInt32 {
		return PIDDead
	}
	err := syscall.Kill(int(pid), 0)
	return pidStateFromKillError(err)
}

func pidStateFromKillError(err error) PIDState {
	if err == nil {
		return PIDAlive
	}
	if errors.Is(err, syscall.ESRCH) {
		return PIDDead
	}
	return PIDUnknown
}
