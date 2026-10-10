package probe

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
)

func TestIdentifierPoolKeepsConcurrentLeasesDistinct(t *testing.T) {
	pool := new(identifierPool)
	const count = 256
	ids := make([]uint16, count)
	var workers sync.WaitGroup
	for i := range ids {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			id, err := pool.acquire()
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			ids[i] = id
		}(i)
	}
	workers.Wait()
	seen := make(map[uint16]bool, count)
	for _, id := range ids {
		if id == 0 || seen[id] {
			t.Fatalf("lease %d is zero or duplicated", id)
		}
		seen[id] = true
	}
	for _, id := range ids {
		pool.release(id)
	}
	for range count {
		id, err := pool.acquire()
		if err != nil {
			t.Fatalf("released lease was not reusable: %v", err)
		}
		pool.release(id)
	}
}

func TestIPAddrMatchesPreservesScope(t *testing.T) {
	target := netip.MustParseAddr("fe80::1%eth0")
	if !ipAddrMatches(&net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0"}, target) {
		t.Fatal("matching scoped sender was not attributed to target")
	}
	if ipAddrMatches(&net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth1"}, target) {
		t.Fatal("sender from another scope was attributed to target")
	}
}

func TestLocalNoRouteUsesFamilySpecificErrors(t *testing.T) {
	err := testNetworkUnreachableError()
	if !localNoRoute(err, netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("IPv4 network-unreachable was not classified as local unreachable")
	}
	if !localNoRoute(err, netip.MustParseAddr("2001:db8::1")) {
		t.Fatal("IPv6 network-unreachable was not classified as no route")
	}
	v4 := finalizeICMPResult(netip.MustParseAddr("192.0.2.1"), 1, 0, false, false, err)
	if v4.State != "UNREACHABLE" || v4.Err != nil {
		t.Fatalf("IPv4 local route outcome = %+v, want UNREACHABLE without execution error", v4)
	}
	v6 := finalizeICMPResult(netip.MustParseAddr("2001:db8::1"), 1, 0, false, false, err)
	if v6.State != "NO ROUTE" || v6.Err != nil {
		t.Fatalf("IPv6 local route outcome = %+v, want NO ROUTE without execution error", v6)
	}
	partial := finalizeICMPResult(netip.MustParseAddr("192.0.2.1"), 2, 1, false, false, err)
	if partial.State != "OK (1/2 replies)" || partial.Err != nil {
		t.Fatalf("partial reply plus local route result = %+v, want partial OK without execution error", partial)
	}
	rejected := finalizeICMPResult(netip.MustParseAddr("192.0.2.1"), 2, 0, true, false, err)
	if rejected.State != "REJECTED" || rejected.Err != nil {
		t.Fatalf("target error plus local route result = %+v, want REJECTED without execution error", rejected)
	}
}

func TestICMPResultPrecedenceKeepsIndependentExecutionError(t *testing.T) {
	resourceErr := errors.New("resource failure")
	result := finalizeICMPResult(netip.MustParseAddr("192.0.2.1"), 2, 1, true, true, resourceErr)
	if result.State != "OK (1/2 replies)" || !errors.Is(result.Err, resourceErr) {
		t.Fatalf("reply precedence lost state or independent error: %+v", result)
	}
	result = finalizeICMPResult(netip.MustParseAddr("192.0.2.1"), 2, 0, true, true, nil)
	if result.State != "REJECTED" {
		t.Fatalf("target error did not beat intermediate error: %+v", result)
	}
}

func TestMixedLocalRouteAndExecutionErrorsRemainFailures(t *testing.T) {
	routeErr := testNetworkUnreachableError()
	resourceErr := errors.New("socket resource failure")
	mixed := errors.Join(routeErr, resourceErr)
	if localNoRoute(mixed, netip.MustParseAddr("192.0.2.8")) {
		t.Fatal("joined resource failure was classified as a route-only outcome")
	}
	result := finalizeICMPResult(netip.MustParseAddr("192.0.2.8"), 2, 0, false, false, mixed)
	if result.State != "ERROR" || !errors.Is(result.Err, resourceErr) {
		t.Fatalf("mixed route/resource result = %+v, want ERROR retaining resource failure", result)
	}
	result = finalizeICMPResult(netip.MustParseAddr("192.0.2.8"), 2, 1, false, false, mixed)
	if result.State != "OK (1/2 replies)" || !errors.Is(result.Err, resourceErr) {
		t.Fatalf("partial reply plus mixed failure = %+v, want partial OK retaining resource failure", result)
	}
}
