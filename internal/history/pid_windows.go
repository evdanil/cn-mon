//go:build windows

// Local PID classification adapts the Windows branch of
// cn_tool/utils/oscompat.py in cn-tool 0.8.0, commit
// 2e2f3c40ef6718486f1ca4477f9145029ec495fa.
package history

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

// LocalPIDState checks a process handle without generating console events.
func LocalPIDState(pid int64) PIDState {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return PIDDead
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return pidStateFromOpenError(err)
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return PIDUnknown
	}
	if exitCode == windowsStillActive {
		return PIDAlive
	}
	return PIDDead
}

func pidStateFromOpenError(err error) PIDState {
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return PIDDead
	}
	return PIDUnknown
}
