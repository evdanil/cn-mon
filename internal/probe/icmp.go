package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

type identifierPool struct {
	mu     sync.Mutex
	active [1 << 16]bool
	next   uint16
}

var icmpIdentifiers identifierPool

func (p *identifierPool) acquire() (uint16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i < len(p.active)-1; i++ {
		p.next++
		if p.next == 0 {
			p.next++
		}
		if !p.active[p.next] {
			p.active[p.next] = true
			return p.next, nil
		}
	}
	return 0, errors.New("all ICMP identifiers are in use")
}

func (p *identifierPool) release(id uint16) {
	p.mu.Lock()
	p.active[id] = false
	p.mu.Unlock()
}

func probeICMP(ctx context.Context, address netip.Addr) (ICMPResult, error) {
	if err := ctx.Err(); err != nil {
		return ICMPResult{}, err
	}
	if !address.IsValid() {
		return ICMPResult{}, errors.New("invalid ICMP probe address")
	}
	id, err := icmpIdentifiers.acquire()
	if err != nil {
		return ICMPResult{State: "ERROR", Err: err}, nil
	}
	defer icmpIdentifiers.release(id)

	ip := address.AsSlice()
	zone := address.Zone()
	pinger := probing.New(address.String())
	pinger.SetIPAddr(&net.IPAddr{IP: net.IP(append([]byte(nil), ip...)), Zone: zone})
	pinger.SetPrivileged(true)
	pinger.SetID(int(id))
	pinger.Count = 2
	pinger.Interval = time.Second
	pinger.Timeout = 3 * time.Second
	pinger.RecordRtts = false
	pinger.RecordTTLs = false
	pinger.SetLogger(probing.NoopLogger{})

	var result ICMPResult
	var targetError, intermediateError bool
	var mu sync.Mutex
	pinger.OnSend = func(*probing.Packet) {
		mu.Lock()
		result.Sent++
		mu.Unlock()
	}
	pinger.OnRecv = func(*probing.Packet) {
		mu.Lock()
		result.Received++
		mu.Unlock()
	}
	pinger.OnICMPError = func(event *probing.ICMPError) {
		if event == nil || event.Sender == nil {
			return
		}
		isTarget := ipAddrMatches(event.Sender, address)
		mu.Lock()
		if isTarget {
			targetError = true
		} else {
			intermediateError = true
		}
		mu.Unlock()
	}
	pinger.OnSendError = func(_ *probing.Packet, sendErr error) {
		mu.Lock()
		if result.Err == nil {
			result.Err = sendErr
		}
		mu.Unlock()
	}

	runErr := pinger.RunWithContext(ctx)
	if err := ctx.Err(); err != nil {
		return ICMPResult{}, err
	}
	mu.Lock()
	sent, received := pinger.Statistics().PacketsSent, pinger.Statistics().PacketsRecv
	if runErr != nil {
		result.Err = runErr
	}
	result = finalizeICMPResult(address, sent, received, targetError, intermediateError, result.Err)
	mu.Unlock()
	return result, nil
}

func ipAddrMatches(sender *net.IPAddr, target netip.Addr) bool {
	if sender == nil {
		return false
	}
	got, ok := netip.AddrFromSlice(sender.IP)
	if !ok {
		return false
	}
	got = got.Unmap()
	if target.Is4In6() {
		target = target.Unmap()
	}
	if got != target.WithZone("") {
		return false
	}
	if target.Zone() == "" {
		return true
	}
	return sameZone(sender.Zone, target.Zone())
}

func localNoRoute(err error, address netip.Addr) bool {
	return err != nil && onlyLocalNoRouteErrors(err, address.Is6())
}

func onlyLocalNoRouteErrors(err error, ipv6 bool) bool {
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		causes := many.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyLocalNoRouteErrors(cause, ipv6) {
				return false
			}
		}
		return true
	}
	if one, ok := err.(interface{ Unwrap() error }); ok {
		return onlyLocalNoRouteErrors(one.Unwrap(), ipv6)
	}
	return isLocalNoRouteError(err, ipv6)
}

func finalizeICMPResult(address netip.Addr, sent, received int, targetError, intermediateError bool, execErr error) ICMPResult {
	noRoute := localNoRoute(execErr, address)
	if noRoute {
		execErr = nil
	}
	result := ICMPResult{Sent: sent, Received: received, Err: execErr}
	switch {
	case received >= 2:
		result.State = "OK"
	case received > 0:
		result.State = fmt.Sprintf("OK (%d/2 replies)", received)
	case targetError:
		result.State = "REJECTED"
	case intermediateError:
		result.State = "UNREACHABLE"
	case noRoute:
		if address.Is6() {
			result.State = "NO ROUTE"
		} else {
			result.State = "UNREACHABLE"
		}
	case execErr != nil:
		result.State = "ERROR"
	default:
		result.State = "NO RESPONSE"
	}
	return result
}

var zoneIndexes sync.Map

func sameZone(a, b string) bool {
	if a == b {
		return a != ""
	}
	if a == "" || b == "" {
		return false
	}
	return zoneIndex(a) > 0 && zoneIndex(a) == zoneIndex(b)
}

func zoneIndex(zone string) int {
	if index, err := strconv.Atoi(zone); err == nil && index > 0 {
		return index
	}
	if value, ok := zoneIndexes.Load(zone); ok {
		return value.(int)
	}
	iface, err := net.InterfaceByName(zone)
	index := 0
	if err == nil {
		index = iface.Index
	}
	zoneIndexes.Store(zone, index)
	return index
}
