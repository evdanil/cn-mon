package probe

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

type nativeRequest struct {
	ID  int
	Seq int
}

func TestNativeICMPGate(t *testing.T) {
	if os.Getenv("CNMON_NATIVE") != "1" {
		t.Skip("set CNMON_NATIVE=1 only in the disposable native CI harness")
	}
	phase := os.Getenv("CNMON_NATIVE_PHASE")
	if phase == "" {
		t.Fatal("native phase is required")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatalf("identify native test user: %v", err)
	}
	t.Logf("CNMON_NATIVE_IDENTITY phase=%s goos=%s goarch=%s uid=%s username=%s", phase, runtime.GOOS, runtime.GOARCH, current.Uid, current.Username)
	privilegeFacts := nativePrivilegeFacts()
	t.Logf("CNMON_NATIVE_PRIVILEGES %s", privilegeFacts)
	if phase == "denied" {
		if runtime.GOOS == "linux" && current.Uid != "65534" {
			t.Fatalf("denied phase ran as uid %s, want the unprivileged nobody uid 65534", current.Uid)
		}
		if runtime.GOOS == "linux" && nativeRawGrantPresent() {
			t.Fatalf("denied phase has an effective, permitted, or ambient CAP_NET_RAW grant: %s", privilegeFacts)
		}
		if runtime.GOOS == "windows" && strings.Contains(privilegeFacts, "admin_group_enabled=true") {
			t.Fatalf("denied phase still has an enabled Administrators group: %s", privilegeFacts)
		}
	}
	if phase == "granted" && !nativeCanRaw() {
		t.Fatalf("granted phase lacks the platform's raw-ICMP grant: %s", privilegeFacts)
	}

	for _, fixture := range nativeAddresses(t) {
		t.Run(fixture.name, func(t *testing.T) {
			target, router := fixture.target, fixture.router
			if phase == "denied" {
				callbacks, err := runNativePinger(t, target, router, phase)
				if err == nil {
					if runtime.GOOS == "linux" {
						t.Fatal("Linux raw ICMP unexpectedly succeeded in the denied-capability phase")
					}
					if len(callbacks.sent) != 2 || len(callbacks.received) != 2 {
						t.Fatalf("ordinary-user raw ICMP succeeded but obtained sends=%d replies=%d, want two each", len(callbacks.sent), len(callbacks.received))
					}
					t.Logf("CNMON_NATIVE_PINGER phase=%s family=%d outcome=success", phase, targetFamily(target))
				} else if !nativePermissionError(err) {
					t.Fatalf("ordinary-user raw ICMP failed for a non-permission reason: %T %v", err, err)
				} else {
					t.Logf("CNMON_NATIVE_PINGER phase=%s family=%d outcome=permission-denied error=%T %v", phase, targetFamily(target), err, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				result, methodErr := probeICMP(ctx, target)
				cancel()
				if methodErr != nil {
					t.Fatalf("production probeICMP method returned %v, want per-host error in result.Err", methodErr)
				}
				t.Logf("CNMON_NATIVE_ADAPTER phase=%s target=%s state=%s sent=%d received=%d err=%T %v", phase, target, result.State, result.Sent, result.Received, result.Err, result.Err)
				if result.State == "OK" {
					if runtime.GOOS == "linux" {
						t.Fatal("Linux production adapter unexpectedly succeeded in the denied-capability phase")
					}
					if result.Sent != 2 || result.Received != 2 || result.Err != nil {
						t.Fatalf("ordinary-user production adapter success=%+v, want two replies and no execution error", result)
					}
					t.Logf("CNMON_NATIVE_ORDINARY phase=%s family=%d outcome=adapter-success", phase, targetFamily(target))
					return
				}
				if result.State != "ERROR" || result.Err == nil || !nativePermissionError(result.Err) {
					t.Fatalf("ordinary-user production adapter result=%+v, want ERROR with a platform permission code", result)
				}
				t.Logf("CNMON_NATIVE_ORDINARY phase=%s family=%d outcome=adapter-permission-denied error=%T %v", phase, targetFamily(target), result.Err, result.Err)
				return
			}

			callbacks, runErr := runNativePinger(t, target, nativeErrorSenderForPhase(phase, target, router), phase)
			if runErr != nil {
				t.Fatalf("production pro-bing pinger failed in phase %s: %v", phase, runErr)
			}

			if phase == "target-error" || phase == "intermediate-error" {
				if len(callbacks.errors) == 0 {
					t.Fatal("required native ICMP error did not reach Pinger.OnICMPError")
				}
				wantSender := target
				if phase == "intermediate-error" {
					wantSender = router
				}
				seen := make(map[nativeRequest]bool)
				for _, event := range callbacks.errors {
					if event.Family != targetFamily(target) {
						t.Errorf("callback family=%d, want %d", event.Family, targetFamily(target))
					}
					if event.ReceivedAt.IsZero() {
						t.Errorf("callback has zero receive timestamp")
					}
					if event.Sender == nil {
						t.Errorf("callback has no sender")
					} else if got, ok := netip.AddrFromSlice(event.Sender.IP); !ok || got.Unmap() != wantSender.Unmap() {
						t.Errorf("callback sender=%v, want %s", event.Sender, wantSender)
					}
					if event.Destination == nil {
						t.Errorf("callback has no quoted destination")
					} else if got, ok := netip.AddrFromSlice(event.Destination.IP); !ok || got.Unmap() != target.Unmap() {
						t.Errorf("callback quoted destination=%v, want %s", event.Destination, target)
					}
					key := nativeRequest{ID: event.ID, Seq: event.Seq}
					if !callbacks.sent[key] {
						t.Errorf("callback identity id=%d seq=%d was not observed by OnSend", event.ID, event.Seq)
					}
					if seen[key] {
						t.Errorf("duplicate error callback for request id=%d seq=%d", event.ID, event.Seq)
					}
					wantType, wantCode := 3, 13
					if target.Is6() {
						wantType, wantCode = 1, 1
					}
					if event.Type != wantType || event.Code != wantCode {
						t.Errorf("callback type/code=%d/%d, want %d/%d", event.Type, event.Code, wantType, wantCode)
					}
					if runtime.GOOS == "windows" {
						firstReply, secondReply := 0, 0
						for _, order := range callbacks.receiveOrder {
							if firstReply == 0 || order < firstReply {
								secondReply = firstReply
								firstReply = order
							} else if order > secondReply {
								secondReply = order
							}
						}
						if callbacks.errorOrder[key] <= firstReply || callbacks.errorOrder[key] >= secondReply {
							t.Errorf("injected error callback order=%d must fall after first reply=%d and before second reply=%d", callbacks.errorOrder[key], firstReply, secondReply)
						}
					}
					seen[key] = true
					t.Logf("CNMON_NATIVE_ERROR phase=%s family=%d type=%d code=%d sender=%s destination=%s id=%d seq=%d", phase, event.Family, event.Type, event.Code, event.Sender, event.Destination, event.ID, event.Seq)
				}
			}

			if phase == "echo" || phase == "granted" || runtime.GOOS == "windows" {
				if len(callbacks.received) != 2 {
					t.Fatalf("native loopback echo received %d distinct replies, want 2", len(callbacks.received))
				}
				if len(callbacks.sent) != 2 {
					t.Fatalf("native loopback echo sent %d distinct requests, want 2", len(callbacks.sent))
				}
			}

			if phase == "echo" || phase == "granted" || phase == "target-error" || phase == "intermediate-error" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := probeICMP(ctx, target)
				if err != nil {
					t.Fatalf("production probeICMP failed in phase %s: %v", phase, err)
				}
				if result.Err != nil {
					t.Fatalf("production probeICMP recorded an execution error in phase %s: %v", phase, result.Err)
				}
				t.Logf("CNMON_NATIVE_ADAPTER phase=%s target=%s state=%s sent=%d received=%d err=%v", phase, target, result.State, result.Sent, result.Received, result.Err)
				switch phase {
				case "target-error":
					if runtime.GOOS == "linux" && result.State != "REJECTED" {
						t.Fatalf("target-sourced native rejection state=%q, want REJECTED", result.State)
					}
				case "intermediate-error":
					if runtime.GOOS == "linux" && result.State != "UNREACHABLE" {
						t.Fatalf("intermediate-sourced native error state=%q, want UNREACHABLE", result.State)
					}
				case "echo", "granted":
					if result.State != "OK" || result.Sent != 2 || result.Received != 2 {
						t.Fatalf("production native echo result=%+v, want two of two replies", result)
					}
				}
			}
		})
	}
}

func TestNativeErrorSourceForPhase(t *testing.T) {
	target := netip.MustParseAddr("192.0.2.11")
	router := netip.MustParseAddr("192.0.2.1")
	if got := nativeErrorSenderForPhase("target-error", target, router); got != target {
		t.Fatalf("target-error emitter source=%s, want target %s", got, target)
	}
	if got := nativeErrorSenderForPhase("intermediate-error", target, router); got != router {
		t.Fatalf("intermediate-error emitter source=%s, want router %s", got, router)
	}
}

// Keep error-source choice in a small pure helper so the native sender cannot
// accidentally make both acceptance phases test the same attribution.
func nativeErrorSenderForPhase(phase string, target, router netip.Addr) netip.Addr {
	if phase == "target-error" {
		return target
	}
	return router
}

type nativeCapture struct {
	sent         map[nativeRequest]bool
	received     map[int]bool
	receiveOrder map[int]int
	errorOrder   map[nativeRequest]int
	errors       []*probing.ICMPError
	order        int
	mu           sync.Mutex
}

func runNativePinger(t *testing.T, target, errorSender netip.Addr, phase string) (*nativeCapture, error) {
	t.Helper()
	capture := &nativeCapture{sent: make(map[nativeRequest]bool), received: make(map[int]bool), receiveOrder: make(map[int]int), errorOrder: make(map[nativeRequest]int)}
	pinger := probing.New(target.String())
	pinger.SetIPAddr(&net.IPAddr{IP: net.IP(target.AsSlice()), Zone: target.Zone()})
	pinger.SetPrivileged(true)
	pinger.SetID(0x43a1)
	pinger.SetLogger(probing.NoopLogger{})
	pinger.Count = 2
	pinger.Interval = time.Second
	pinger.Timeout = 3 * time.Second
	pinger.RecordRtts = false
	pinger.RecordTTLs = false
	pinger.OnSend = func(packet *probing.Packet) {
		capture.mu.Lock()
		capture.order++
		capture.sent[nativeRequest{ID: packet.ID, Seq: packet.Seq}] = true
		capture.mu.Unlock()
		t.Logf("CNMON_NATIVE_SEND phase=%s destination=%s id=%d seq=%d", phase, target, packet.ID, packet.Seq)
	}
	pinger.OnRecv = func(packet *probing.Packet) {
		capture.mu.Lock()
		first := len(capture.received) == 0
		capture.order++
		capture.received[packet.Seq] = true
		capture.receiveOrder[packet.Seq] = capture.order
		capture.mu.Unlock()
		t.Logf("CNMON_NATIVE_REPLY phase=%s sender=%s id=%d seq=%d", phase, packet.IPAddr, packet.ID, packet.Seq)
		if runtime.GOOS == "windows" && first && (phase == "target-error" || phase == "intermediate-error") {
			req := nativeRequest{ID: packet.ID, Seq: packet.Seq}
			if err := emitNativeICMPError(target, errorSender, req); err != nil {
				t.Errorf("send native raw ICMP error from %s: %v", errorSender, err)
			}
		}
	}
	pinger.OnICMPError = func(event *probing.ICMPError) {
		capture.mu.Lock()
		copyEvent := *event
		if event.Sender != nil {
			copyEvent.Sender = &net.IPAddr{IP: append(net.IP(nil), event.Sender.IP...), Zone: event.Sender.Zone}
		}
		if event.Destination != nil {
			copyEvent.Destination = &net.IPAddr{IP: append(net.IP(nil), event.Destination.IP...), Zone: event.Destination.Zone}
		}
		capture.errors = append(capture.errors, &copyEvent)
		capture.order++
		capture.errorOrder[nativeRequest{ID: event.ID, Seq: event.Seq}] = capture.order
		capture.mu.Unlock()
		t.Logf("CNMON_NATIVE_CALLBACK phase=%s family=%d type=%d code=%d sender=%s destination=%s id=%d seq=%d", phase, event.Family, event.Type, event.Code, event.Sender, event.Destination, event.ID, event.Seq)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := pinger.RunWithContext(ctx)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture, err
}

type nativeAddressFixture struct {
	name   string
	target netip.Addr
	router netip.Addr
}

func nativeAddresses(t *testing.T) []nativeAddressFixture {
	t.Helper()
	parse := func(key string) netip.Addr {
		t.Helper()
		raw := os.Getenv(key)
		if raw == "" {
			t.Fatalf("required native address %s is missing", key)
		}
		address, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("parse %s=%q: %v", key, raw, err)
		}
		return address
	}
	return []nativeAddressFixture{
		{name: "ipv4", target: parse("CNMON_NATIVE_IPV4_TARGET"), router: parse("CNMON_NATIVE_IPV4_ROUTER")},
		{name: "ipv6", target: parse("CNMON_NATIVE_IPV6_TARGET"), router: parse("CNMON_NATIVE_IPV6_ROUTER")},
	}
}

func targetFamily(address netip.Addr) int {
	if address.Is4() {
		return 4
	}
	return 6
}
