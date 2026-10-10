//go:build !windows

package probe

import (
	"errors"
	"syscall"
)

func classifySocketError(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
		return "closed"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "timeout"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EAFNOSUPPORT):
		return "unreachable"
	default:
		return "error"
	}
}
