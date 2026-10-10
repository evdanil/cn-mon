//go:build windows

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
		{syscall.Errno(10061), "closed"},      // WSAECONNREFUSED
		{syscall.Errno(10054), "closed"},      // WSAECONNRESET
		{syscall.Errno(10060), "timeout"},     // WSAETIMEDOUT
		{syscall.Errno(10051), "unreachable"}, // WSAENETUNREACH
		{syscall.Errno(10065), "unreachable"}, // WSAEHOSTUNREACH
		{syscall.Errno(10047), "unreachable"}, // WSAEAFNOSUPPORT
		{syscall.Errno(10022), "error"},       // WSAEINVAL is not a route diagnosis
	} {
		if got := classifyTCPError(tc.err); got != tc.want {
			t.Errorf("classifyTCPError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
