// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"
)

// The whole-history fold adapts cn_tool/utils/ping_coverage.py (subnets_of,
// fold_coverage) from cn-tool merge a739948c69ada9bab27d25058c1d695ca06d5aba
// (PR #178, Apache-2.0). It is pure: it imports neither history nor
// database/sql, and its caller supplies the rounds. cn-tool's web paging, its
// per-host first and last seen and its stop callback are not ported.

const (
	CoverageSeen      = "Seen"
	CoverageUnseen    = "Unseen"
	CoverageNotTested = "Not tested"
	SilentSubnetNote  = "no IP in this subnet ever replied"
)

// HostCoverage is one request host's status with the evidence it rests on.
// Tested is the number of rounds less the Failed ones: a failed probe or an
// unresolved name is not evidence of silence.
type HostCoverage struct {
	Host                     string
	Status                   string // one of the three Coverage constants
	Answered, Tested, Failed int
	Note                     string // SilentSubnetNote or ""
}

// Coverage is the fold's result. Since is the start of the oldest round folded.
type Coverage struct {
	Hosts         []HostCoverage // request host order
	Rounds        int            // rounds folded
	Since         *time.Time     // nil without rounds
	SilentSubnets []string       // tokens, in request token order
}

// Subnet is a request token that is a network, with the request hosts inside it.
type Subnet struct {
	Token   string
	Members []int // ascending indexes into the request hosts
}

// SubnetsOf finds the request tokens that are networks and, for each, the
// ascending indexes of the hosts inside it. A token is a network when it holds
// "/" and parses as a prefix, which is masked; names, single addresses
// and text such as "site/one" are skipped, as are hosts that are not
// addresses. A host's zone is ignored and a host inside two networks belongs
// to both. target.LayoutInputs is not used because it omits a later subnet
// whose hosts are already represented, which would hide a silent subnet.
//
// Like cn-tool's subnets_of, it sorts the addresses once and bisects for each
// network, so the cost is the sort plus a search and the members of each
// token, not a pass over every host for every token. The sort puts IPv4 before
// IPv6 and breaks ties by host index, and the members of one network are one
// contiguous run, because a prefix is a contiguous address range.
func SubnetsOf(tokens, hosts []string) []Subnet {
	subnets := make([]Subnet, 0)
	var sorted []hostAddr
	for _, token := range tokens {
		if !strings.Contains(token, "/") {
			continue
		}
		prefix, err := netip.ParsePrefix(token)
		if err != nil {
			continue
		}
		prefix = prefix.Masked()
		if sorted == nil {
			sorted = sortedHostAddrs(hosts)
		}
		network := prefix.Addr()
		first := sort.Search(len(sorted), func(i int) bool { return sorted[i].addr.Compare(network) >= 0 })
		members := make([]int, 0)
		for _, host := range sorted[first:] {
			if !prefix.Contains(host.addr) {
				break
			}
			members = append(members, host.index)
		}
		sort.Ints(members)
		subnets = append(subnets, Subnet{Token: token, Members: members})
	}
	return subnets
}

type hostAddr struct {
	addr  netip.Addr // without its zone
	index int
}

// sortedHostAddrs lists the hosts that are addresses ordered by address, then
// by index. Unparsable hosts are in no network and are left out.
func sortedHostAddrs(hosts []string) []hostAddr {
	sorted := make([]hostAddr, 0, len(hosts))
	for i, host := range hosts {
		if addr, err := netip.ParseAddr(host); err == nil {
			sorted = append(sorted, hostAddr{addr: addr.WithZone(""), index: i})
		}
	}
	slices.SortFunc(sorted, func(a, b hostAddr) int {
		if c := a.addr.Compare(b.addr); c != 0 {
			return c
		}
		return cmp.Compare(a.index, b.index)
	})
	return sorted
}

// CoverageFold accumulates rounds, in any order, into per-host counts sized by
// the host count. The caller drives it with Add and reads Coverage.
type CoverageFold struct {
	hosts            []string
	subnets          []Subnet
	answered, failed []int
	rounds           int
	since            time.Time
}

func NewCoverageFold(hosts []string, subnets []Subnet) *CoverageFold {
	return &CoverageFold{
		hosts: hosts, subnets: subnets,
		answered: make([]int, len(hosts)), failed: make([]int, len(hosts)),
	}
}

// Add folds one round. It rejects, and leaves the fold unchanged for, an
// observation string whose length differs from the host count or that holds a
// byte a stored round never records.
func (f *CoverageFold) Add(started time.Time, observations string) error {
	if len(observations) != len(f.hosts) {
		return fmt.Errorf("a round has %d observations for %d hosts", len(observations), len(f.hosts))
	}
	for i := 0; i < len(observations); i++ {
		if !validObservation(observations[i]) {
			return fmt.Errorf("a round holds %q at position %d, which is not an observation", observations[i], i)
		}
	}
	f.rounds++
	if f.rounds == 1 || started.Before(f.since) {
		f.since = started
	}
	for i := 0; i < len(observations); i++ {
		switch observations[i] {
		case EchoReply, Rejected, TCPSeen:
			f.answered[i]++
		case ExecutionErr, NoName:
			f.failed[i]++
		}
	}
	return nil
}

// Coverage derives the statuses and silent subnets from the rounds folded so
// far. A host is Seen when it answered in some round, else Unseen when some
// round tested it, else Not tested. A subnet is silent when none of its
// members is Seen and at least one is Unseen; each Unseen member of a silent
// subnet carries SilentSubnetNote.
func (f *CoverageFold) Coverage() Coverage {
	hosts := make([]HostCoverage, len(f.hosts))
	for i, host := range f.hosts {
		tested := f.rounds - f.failed[i]
		status := CoverageNotTested
		if f.answered[i] > 0 {
			status = CoverageSeen
		} else if tested > 0 {
			status = CoverageUnseen
		}
		hosts[i] = HostCoverage{Host: host, Status: status, Answered: f.answered[i], Tested: tested, Failed: f.failed[i]}
	}
	silent := make([]string, 0)
	for _, subnet := range f.subnets {
		var anySeen, anyUnseen bool
		for _, i := range subnet.Members {
			anySeen = anySeen || hosts[i].Status == CoverageSeen
			anyUnseen = anyUnseen || hosts[i].Status == CoverageUnseen
		}
		if anySeen || !anyUnseen {
			continue
		}
		silent = append(silent, subnet.Token)
		for _, i := range subnet.Members {
			if hosts[i].Status == CoverageUnseen {
				hosts[i].Note = SilentSubnetNote
			}
		}
	}
	coverage := Coverage{Hosts: hosts, Rounds: f.rounds, SilentSubnets: silent}
	if f.rounds > 0 {
		since := f.since
		coverage.Since = &since
	}
	return coverage
}

// Unseen lists the Unseen and Not tested hosts in host order.
func (c Coverage) Unseen() []HostCoverage {
	unseen := make([]HostCoverage, 0)
	for _, host := range c.Hosts {
		if host.Status != CoverageSeen {
			unseen = append(unseen, host)
		}
	}
	return unseen
}
