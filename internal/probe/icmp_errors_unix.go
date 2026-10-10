//go:build !windows

package probe

import "syscall"

func isLocalNoRouteError(err error, ipv6 bool) bool {
	errno, ok := err.(syscall.Errno)
	if !ok {
		return false
	}
	if errno == syscall.EHOSTUNREACH || errno == syscall.ENETUNREACH {
		return true
	}
	return ipv6 && errno == syscall.EAFNOSUPPORT
}
