//go:build windows

package probe

import (
	"errors"
	"syscall"
)

func classifySocketError(err error) string {
	const (
		wsaEAFNOSUPPORT = syscall.Errno(10047)
		wsaECONNRESET   = syscall.Errno(10054)
		wsaECONNREFUSED = syscall.Errno(10061)
		wsaETIMEDOUT    = syscall.Errno(10060)
		wsaEHOSTUNREACH = syscall.Errno(10065)
		wsaENETUNREACH  = syscall.Errno(10051)
	)
	switch {
	case errors.Is(err, wsaECONNREFUSED), errors.Is(err, wsaECONNRESET):
		return "closed"
	case errors.Is(err, wsaETIMEDOUT):
		return "timeout"
	case errors.Is(err, wsaEHOSTUNREACH), errors.Is(err, wsaENETUNREACH), errors.Is(err, wsaEAFNOSUPPORT):
		return "unreachable"
	default:
		return "error"
	}
}
