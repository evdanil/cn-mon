//go:build !windows

package probe

import (
	"syscall"
	"testing"
)

func testPlatformTCPErrorMappings(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		err  error
		want string
	}{
		{syscall.ECONNREFUSED, "closed"},
		{syscall.ECONNRESET, "closed"},
		{syscall.ETIMEDOUT, "timeout"},
		{syscall.ENETUNREACH, "unreachable"},
		{syscall.EHOSTUNREACH, "unreachable"},
		{syscall.EAFNOSUPPORT, "unreachable"},
	} {
		if got := classifyTCPError(tc.err); got != tc.want {
			t.Errorf("classifyTCPError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
