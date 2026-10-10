//go:build linux

package history

import (
	"syscall"
	"testing"
)

func TestPIDStateFromKillError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want PIDState
	}{
		{name: "success", err: nil, want: PIDAlive},
		{name: "missing", err: syscall.ESRCH, want: PIDDead},
		{name: "permission denied is indeterminate", err: syscall.EPERM, want: PIDUnknown},
		{name: "unexpected error", err: syscall.EIO, want: PIDUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pidStateFromKillError(tc.err); got != tc.want {
				t.Fatalf("pidStateFromKillError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
