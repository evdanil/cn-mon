//go:build windows

package probe

import "syscall"

func isLocalNoRouteError(err error, ipv6 bool) bool {
	const (
		wsaEHostUnreach = syscall.Errno(10065)
		wsaENetUnreach  = syscall.Errno(10051)
		wsaEAfNoSupport = syscall.Errno(10047)
	)
	errno, ok := err.(syscall.Errno)
	if !ok {
		return false
	}
	if errno == wsaEHostUnreach || errno == wsaENetUnreach {
		return true
	}
	return ipv6 && errno == wsaEAfNoSupport
}
