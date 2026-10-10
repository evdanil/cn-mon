package probe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeBatchPreservesInputAndPortOrderAndContinuesHostFailures(t *testing.T) {
	var mu sync.Mutex
	var starts []string
	p := &Prober{
		icmp: func(_ context.Context, addr netip.Addr) (ICMPResult, error) {
			if addr.String() == "192.0.2.2" {
				return ICMPResult{State: "ERROR", Err: errors.New("synthetic ICMP resource failure")}, nil
			}
			return ICMPResult{State: "NO RESPONSE"}, nil
		},
		dial: func(_ context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			starts = append(starts, network+" "+addr)
			mu.Unlock()
			return nil, errors.New("synthetic refusal")
		},
	}
	targets := []Resolved{
		{Index: 8, Host: "a", Address: netip.MustParseAddr("192.0.2.1")},
		{Index: 3, Host: "b", Address: netip.MustParseAddr("192.0.2.2")},
	}
	got, err := p.ProbeBatch(context.Background(), targets, []int{443, 22, 443})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Target.Index != 8 || got[1].Target.Index != 3 {
		t.Fatalf("batch target order/indexes = %+v", got)
	}
	if got[0].TCP[22].State != "error" || got[0].TCP[443].State != "error" {
		t.Fatalf("first host TCP results = %+v", got[0].TCP)
	}
	if got[0].TCP[22].Err == nil || got[0].TCP[443].Err == nil {
		t.Fatalf("TCP execution errors were not retained: %+v", got[0].TCP)
	}
	if got[1].ICMP.Err == nil {
		t.Fatalf("second host ICMP = %+v, want per-host execution error", got[1].ICMP)
	}
	if len(starts) != 4 {
		t.Fatalf("dial attempts = %d, want 4 for two hosts and two distinct ports", len(starts))
	}
}

func TestProbeBatchRejectsInvalidParametersBeforeAdapters(t *testing.T) {
	calls := 0
	p := &Prober{
		icmp: func(context.Context, netip.Addr) (ICMPResult, error) { calls++; return ICMPResult{}, nil },
		dial: func(context.Context, string, string) (net.Conn, error) { calls++; return nil, nil },
	}
	for _, tc := range []struct {
		name    string
		targets []Resolved
		ports   []int
	}{
		{"invalid address", []Resolved{{Host: "x"}}, nil},
		{"bad port", []Resolved{{Address: netip.MustParseAddr("192.0.2.1")}}, []int{0}},
		{"too many distinct ports", []Resolved{{Address: netip.MustParseAddr("192.0.2.1")}}, []int{1, 2, 3, 4, 5, 6}},
		{"too many hosts", makeTargets(101), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.ProbeBatch(context.Background(), tc.targets, tc.ports); err == nil {
				t.Fatal("ProbeBatch succeeded with invalid parameters")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid requests invoked adapters %d times", calls)
	}
}

func TestProbeBatchCancellationReturnsNoPartialResultsAndJoinsWorkers(t *testing.T) {
	started := make(chan struct{}, 2)
	tcpStarted := make(chan struct{}, 2)
	finished := make(chan struct{}, 4)
	p := &Prober{
		icmp: func(ctx context.Context, _ netip.Addr) (ICMPResult, error) {
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return ICMPResult{}, ctx.Err()
		},
		dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			tcpStarted <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		results []Result
		err     error
	}, 1)
	go func() {
		results, err := p.ProbeBatch(ctx, makeTargets(2), []int{80})
		done <- struct {
			results []Result
			err     error
		}{results, err}
	}()
	<-started
	<-started
	<-tcpStarted
	<-tcpStarted
	cancel()
	select {
	case got := <-done:
		if got.err != context.Canceled || got.results != nil {
			t.Fatalf("cancelled batch returned %d results and error %v", len(got.results), got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("ProbeBatch did not return promptly after cancellation")
	}
	for i := 0; i < 4; i++ {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("ProbeBatch returned before active adapter worker exited")
		}
	}
}

func TestNormalizePortsCopiesDeduplicatesAndSorts(t *testing.T) {
	input := []int{443, 22, 443}
	got, err := normalizePorts(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{22, 443}) {
		t.Fatalf("normalized ports = %v", got)
	}
	input[0] = 1
	if !reflect.DeepEqual(got, []int{22, 443}) {
		t.Fatalf("normalized ports changed with caller slice: %v", got)
	}
}

func TestNewWiresRealAdapters(t *testing.T) {
	p := New()
	if p == nil || p.lookup == nil || p.dial == nil || p.icmp == nil {
		t.Fatalf("New returned incomplete adapters: %+v", p)
	}
}

func TestTCPConnectLimitIsSharedAcrossConcurrentBatches(t *testing.T) {
	const batchSize = 100
	release := make(chan struct{})
	started := make(chan struct{}, 2*batchSize)
	var active atomic.Int32
	var peak atomic.Int32
	p := &Prober{
		icmp: func(context.Context, netip.Addr) (ICMPResult, error) {
			return ICMPResult{State: "NO RESPONSE"}, nil
		},
		dial: func(context.Context, string, string) (net.Conn, error) {
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return nil, errors.New("synthetic connect completion")
		},
	}
	done := make(chan error, 2)
	for run := 0; run < 2; run++ {
		go func() {
			_, err := p.ProbeBatch(context.Background(), makeTargets(batchSize), []int{443})
			done <- err
		}()
	}
	for i := 0; i < batchSize; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("100 TCP attempts did not become active")
		}
	}
	if got := active.Load(); got != batchSize {
		close(release)
		t.Fatalf("active TCP attempts = %d, want 100", got)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent batches did not finish")
		}
	}
	if got := peak.Load(); got != batchSize {
		t.Fatalf("peak concurrent TCP attempts = %d, want exactly 100", got)
	}
}

func TestICMPActiveLimitIsSharedAcrossConcurrentBatches(t *testing.T) {
	const batchSize = 100
	release := make(chan struct{})
	started := make(chan struct{}, 2*batchSize)
	var active atomic.Int32
	var peak atomic.Int32
	p := &Prober{icmp: func(context.Context, netip.Addr) (ICMPResult, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return ICMPResult{State: "NO RESPONSE"}, nil
	}}
	done := make(chan error, 2)
	for run := 0; run < 2; run++ {
		go func() {
			_, err := p.ProbeBatch(context.Background(), makeTargets(batchSize), nil)
			done <- err
		}()
	}
	for i := 0; i < batchSize; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("100 ICMP attempts did not become active")
		}
	}
	if got := active.Load(); got != batchSize {
		close(release)
		t.Fatalf("active ICMP attempts = %d, want 100", got)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent batches did not finish")
		}
	}
	if got := peak.Load(); got != batchSize {
		t.Fatalf("peak concurrent ICMP attempts = %d, want exactly 100", got)
	}
}

func TestProbeBatchRunsTCPWavesAfterICMPFinishesAndJoinsOnCancellation(t *testing.T) {
	const hostCount = 100
	firstWaveRelease := make(chan struct{})
	firstStarted := make(chan struct{}, hostCount)
	secondStarted := make(chan struct{}, hostCount)
	icmpFinished := make(chan struct{})
	var icmpCount atomic.Int32
	var dialAttempts atomic.Int32
	var completedDials atomic.Int32
	p := &Prober{
		icmp: func(context.Context, netip.Addr) (ICMPResult, error) {
			if icmpCount.Add(1) == hostCount {
				close(icmpFinished)
			}
			return ICMPResult{State: "NO RESPONSE"}, nil
		},
		dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			n := dialAttempts.Add(1)
			if n <= hostCount {
				firstStarted <- struct{}{}
				<-firstWaveRelease
				completedDials.Add(1)
				return nil, errors.New("synthetic first-wave result")
			}
			secondStarted <- struct{}{}
			<-ctx.Done()
			completedDials.Add(1)
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct {
		results []Result
		err     error
	}, 1)
	go func() {
		results, err := p.ProbeBatch(ctx, makeTargets(hostCount), []int{80, 443, 8080})
		done <- struct {
			results []Result
			err     error
		}{results, err}
	}()
	for i := 0; i < hostCount; i++ {
		select {
		case <-firstStarted:
		case <-time.After(2 * time.Second):
			close(firstWaveRelease)
			t.Fatal("first TCP wave did not start")
		}
	}
	select {
	case <-icmpFinished:
	case <-time.After(2 * time.Second):
		close(firstWaveRelease)
		t.Fatal("ICMP probes did not complete while TCP wave was active")
	}
	close(firstWaveRelease)
	for i := 0; i < hostCount; i++ {
		select {
		case <-secondStarted:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("queued TCP wave did not start after ICMP completed")
		}
	}
	if started := dialAttempts.Load(); started != 2*hostCount {
		t.Fatalf("TCP dials started before cancellation = %d, want two full waves (%d) with the third queued", started, 2*hostCount)
	}
	cancel()
	select {
	case got := <-done:
		if got.err != context.Canceled || got.results != nil {
			t.Fatalf("cancelled multi-wave batch returned %d results, error %v", len(got.results), got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled multi-wave batch did not join workers promptly")
	}
	if got := completedDials.Load(); got != 2*hostCount {
		t.Fatalf("dial workers completed %d attempts before return, want all %d started attempts joined", got, 2*hostCount)
	}
	if got := dialAttempts.Load(); got != 2*hostCount {
		t.Fatalf("cancelled batch started %d dials, want no queued third wave after cancellation", got)
	}
}

func makeTargets(n int) []Resolved {
	result := make([]Resolved, n)
	for i := range result {
		result[i] = Resolved{Index: i, Host: "host", Address: netip.MustParseAddr("192.0.2.1")}
	}
	return result
}
