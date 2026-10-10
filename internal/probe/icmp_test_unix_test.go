//go:build !windows

package probe

import (
	"fmt"
	"syscall"
)

func testNetworkUnreachableError() error {
	return fmt.Errorf("wrapped route error: %w", syscall.ENETUNREACH)
}
