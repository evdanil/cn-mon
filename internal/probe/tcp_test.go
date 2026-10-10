package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestProbeTCPConnectsThenClosesWithoutWriting(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			accepted <- conn
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	got := probeTCP(context.Background(), netip.MustParseAddr("127.0.0.1"), addr.Port)
	if got.State != "open" || got.Err != nil {
		t.Fatalf("probeTCP = %+v, want open", got)
	}
	select {
	case conn := <-accepted:
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 1)
		n, err := conn.Read(buf)
		if n != 0 || err == nil {
			t.Fatalf("server read (%d, %v), want immediate EOF and no payload", n, err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not connect")
	}
}

func TestProbeTCPReportsLocalRefusalAsClosed(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	got := probeTCP(context.Background(), netip.MustParseAddr("127.0.0.1"), port)
	if got.State != "closed" || got.Err != nil {
		t.Fatalf("probeTCP to locally refused port = %+v, want closed", got)
	}
}

func TestTCPAttemptGetsItsOwnDeadlineAndReportsTimeoutAsReachability(t *testing.T) {
	var remaining time.Duration
	got := probeTCPWithDial(context.Background(), netip.MustParseAddr("192.0.2.1"), 443,
		func(ctx context.Context, _, _ string) (net.Conn, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("TCP dial context has no per-attempt deadline")
			}
			remaining = time.Until(deadline)
			return nil, os.ErrDeadlineExceeded
		})
	if remaining < 2*time.Second || remaining > 3*time.Second {
		t.Fatalf("new TCP attempt deadline has %v remaining, expected about three seconds", remaining)
	}
	if got.State != "timeout" || got.Err != nil {
		t.Fatalf("deadline result = %+v, want timeout reachability outcome", got)
	}
}

func TestClassifyTCPErrorUsesSocketCodesAndDeadline(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", context.DeadlineExceeded, "timeout"},
		{"wrapped network timeout", fmt.Errorf("dial: %w", &net.DNSError{IsTimeout: true}), "timeout"},
		{"resource error", errors.New("synthetic resource exhaustion"), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTCPError(tc.err)
			if got != tc.want {
				t.Fatalf("classifyTCPError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
	testPlatformTCPErrorMappings(t)
}
