// Package probe resolves monitor targets and runs bounded native probes.
//
// New utility code in this package is licensed under Apache License 2.0.
package probe

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

type Resolved struct {
	Index     int
	Host      string
	Address   netip.Addr
	LookupErr error
}

type ICMPResult struct {
	State          string
	Sent, Received int
	Err            error
}

type TCPResult struct {
	State string
	Err   error
}

type Result struct {
	Target Resolved
	ICMP   ICMPResult
	TCP    map[int]TCPResult
}

type lookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)
type icmpProbeFunc func(context.Context, netip.Addr) (ICMPResult, error)

// Prober owns the adapters and concurrency limits shared by its calls.
type Prober struct {
	lookup      lookupFunc
	dial        dialFunc
	icmp        icmpProbeFunc
	limitsOnce  sync.Once
	lookupLimit chan struct{}
	icmpLimit   chan struct{}
	tcpLimit    chan struct{}
}

// New creates a Prober backed by the machine resolver, native TCP dialing and ICMP adapter.
func New() *Prober {
	return &Prober{lookup: lookupSystem, dial: (&net.Dialer{}).DialContext, icmp: probeICMP}
}

func (p *Prober) initLimits() {
	p.limitsOnce.Do(func() {
		p.lookupLimit = make(chan struct{}, maxConcurrent)
		p.icmpLimit = make(chan struct{}, maxConcurrent)
		p.tcpLimit = make(chan struct{}, maxConcurrent)
	})
}
