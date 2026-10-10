//go:build windows

package history

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPIDStateFromOpenError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want PIDState
	}{
		{name: "unexpected error", err: windows.ERROR_GEN_FAILURE, want: PIDUnknown},
		{name: "access denied is indeterminate", err: windows.ERROR_ACCESS_DENIED, want: PIDUnknown},
		{name: "invalid process", err: windows.ERROR_INVALID_PARAMETER, want: PIDDead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pidStateFromOpenError(tc.err); got != tc.want {
				t.Fatalf("pidStateFromOpenError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
