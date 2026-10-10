package probe

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveSelectsFirstIPv4AndPreservesInputOrder(t *testing.T) {
	var calls atomic.Int32
	p := &Prober{lookup: func(_ context.Context, network, host string) ([]netip.Addr, error) {
		calls.Add(1)
		if network == "ip4" && host == "dual.example." {
			return []netip.Addr{netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("192.0.2.4")}, nil
		}
		if network == "ip4" && host == "v6.example" {
			return nil, nil
		}
		if network == "ip6" && host == "v6.example" {
			return []netip.Addr{netip.MustParseAddr("2001:db8::9"), netip.MustParseAddr("2001:db8::4")}, nil
		}
		return nil, nil
	}}
	got, err := p.Resolve(context.Background(), []string{"dual.example.", "v6.example", "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.9", "2001:db8::9", "192.0.2.1"}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Index != i || got[i].Address.String() != want[i] {
			t.Errorf("result[%d] = index %d address %v, want index %d address %s", i, got[i].Index, got[i].Address, i, want[i])
		}
	}
	if calls.Load() != 3 { // dual, v6 twice, and the literal makes no resolver call
		t.Fatalf("lookup calls = %d, want 3", calls.Load())
	}
}

func TestResolveUnmapsMappedAnswerAndRecordsLookupFailurePerHost(t *testing.T) {
	p := &Prober{lookup: func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network == "ip4" && host == "mapped.example" {
			return []netip.Addr{netip.MustParseAddr("::ffff:192.0.2.7")}, nil
		}
		if network == "ip4" {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}}
	got, err := p.Resolve(context.Background(), []string{"mapped.example", "missing.example"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Address.String() != "192.0.2.7" {
		t.Fatalf("mapped answer = %v, want unmapped IPv4", got[0].Address)
	}
	if got[1].Address.IsValid() || got[1].LookupErr == nil {
		t.Fatalf("unresolved result = %+v, want per-host lookup diagnostic", got[1])
	}
}

func TestResolveEmptySuccessfulAnswersHaveUsefulDiagnostic(t *testing.T) {
	p := &Prober{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, nil
	}}
	got, err := p.Resolve(context.Background(), []string{"no-records.example"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Address.IsValid() || got[0].LookupErr == nil {
		t.Fatalf("empty successful DNS answer = %+v, want unresolved diagnostic", got[0])
	}
}

func TestResolveRetainsScopedLiteralWithoutLookup(t *testing.T) {
	var calls atomic.Int32
	p := &Prober{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
		calls.Add(1)
		return nil, nil
	}}
	got, err := p.Resolve(context.Background(), []string{"fe80::1%eth0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Address.String() != "fe80::1%eth0" || calls.Load() != 0 {
		t.Fatalf("scoped literal result = %+v, resolver calls = %d", got, calls.Load())
	}
}

func TestResolveOutOfOrderLookupsKeepInputOrder(t *testing.T) {
	release := map[string]chan struct{}{
		"first.example":  make(chan struct{}),
		"second.example": make(chan struct{}),
	}
	started := make(chan string, 2)
	completed := make(chan string, 2)
	p := &Prober{lookup: func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip4" {
			return nil, nil
		}
		started <- host
		<-release[host]
		completed <- host
		if host == "first.example" {
			return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.2")}, nil
	}}
	result := make(chan []Resolved, 1)
	go func() {
		got, _ := p.Resolve(context.Background(), []string{"first.example", "second.example"})
		result <- got
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("both lookups did not start")
		}
	}
	close(release["second.example"])
	if got := <-completed; got != "second.example" {
		t.Fatalf("first completed lookup = %s, want second.example", got)
	}
	close(release["first.example"])
	got := <-result
	if len(got) != 2 || got[0].Host != "first.example" || got[0].Address.String() != "192.0.2.1" || got[0].Index != 0 || got[1].Host != "second.example" || got[1].Address.String() != "192.0.2.2" || got[1].Index != 1 {
		t.Fatalf("out-of-order lookup results = %+v", got)
	}
}

func TestResolveFreshLookupAndCancellationJoinsWorkers(t *testing.T) {
	var calls atomic.Int32
	p := &Prober{lookup: func(ctx context.Context, network, _ string) ([]netip.Addr, error) {
		if network == "ip4" {
			return []netip.Addr{netip.AddrFrom4([4]byte{192, 0, 2, byte(calls.Add(1))})}, nil
		}
		return nil, nil
	}}
	first, err := p.Resolve(context.Background(), []string{"fresh.example"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Resolve(context.Background(), []string{"fresh.example"})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Address == second[0].Address {
		t.Fatalf("separate Resolve calls reused %s", first[0].Address)
	}

	started := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	p.lookup = func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		started <- struct{}{}
		<-ctx.Done()
		finished <- struct{}{}
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := p.Resolve(ctx, []string{"blocked.example"}); done <- e }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Resolve error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Resolve did not return promptly after cancellation")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Resolve returned before its application lookup worker exited")
	}
}

func TestResolveBoundsConcurrentLookupsToOneHundred(t *testing.T) {
	const count = 125
	release := make(chan struct{})
	started := make(chan struct{}, count)
	var active atomic.Int32
	var peak atomic.Int32
	p := &Prober{lookup: func(ctx context.Context, network, _ string) ([]netip.Addr, error) {
		if network != "ip4" {
			return nil, nil
		}
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			active.Add(-1)
			return nil, ctx.Err()
		}
		active.Add(-1)
		return []netip.Addr{netip.MustParseAddr("192.0.2.8")}, nil
	}}
	finished := make(chan []Resolved, 1)
	go func() {
		got, _ := p.Resolve(context.Background(), make([]string, count))
		finished <- got
	}()
	for i := 0; i < maxConcurrent; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("fewer than 100 lookup workers started")
		}
	}
	if peak.Load() != maxConcurrent {
		close(release)
		t.Fatalf("peak lookup concurrency = %d, want 100", peak.Load())
	}
	close(release)
	select {
	case got := <-finished:
		if len(got) != count {
			t.Fatalf("Resolve returned %d hosts, want %d", len(got), count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve did not finish after releasing lookups")
	}
	if peak.Load() > maxConcurrent {
		t.Fatalf("peak lookup concurrency = %d, want at most 100", peak.Load())
	}
}

func TestLookupSystemUsesConfiguredLocalResolver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resolved, err := (&Prober{}).Resolve(ctx, []string{"localhost"})
	if err != nil {
		t.Fatalf("system resolver lookup of localhost: %v", err)
	}
	if len(resolved) != 1 || !resolved[0].Address.Is4() || !resolved[0].Address.IsLoopback() {
		t.Fatalf("system resolver returned no IPv4 loopback for localhost: %+v", resolved)
	}
}
