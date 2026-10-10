package probe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"
)

func probeTCP(ctx context.Context, address netip.Addr, port int) TCPResult {
	return probeTCPWithDial(ctx, address, port, (&net.Dialer{}).DialContext)
}

func probeTCPWithDial(ctx context.Context, address netip.Addr, port int, dial dialFunc) TCPResult {
	if err := ctx.Err(); err != nil {
		return TCPResult{State: "error", Err: err}
	}
	if !address.IsValid() || port < 1 || port > 65535 {
		return TCPResult{State: "error", Err: errors.New("invalid TCP probe address or port")}
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(deadlineCtx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(port)))
	if err == nil {
		_ = conn.Close()
		return TCPResult{State: "open"}
	}
	if ctx.Err() != nil {
		return TCPResult{State: "error", Err: ctx.Err()}
	}
	state := classifyTCPError(err)
	var executionErr error
	if state == "error" {
		executionErr = err
	}
	return TCPResult{State: state, Err: executionErr}
}

func classifyTCPError(err error) string {
	if err == nil {
		return "open"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return "timeout"
	}
	return classifySocketError(err)
}
