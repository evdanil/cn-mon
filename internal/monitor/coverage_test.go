// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"fmt"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// Fixtures in this file are copied from tests/test_utils_ping_coverage.py of
// cn-tool merge a739948c69ada9bab27d25058c1d695ca06d5aba (PR #178, Apache-2.0):
// test_status_from_observations, test_failed_rounds_are_not_evidence,
// test_a_name_that_did_not_resolve_is_a_failed_round_too,
// test_each_host_is_read_at_its_own_position, test_since_is_the_oldest_round,
// test_no_rounds_is_not_tested, test_bad_round_raises,
// test_silent_subnet_notes_only_its_unseen_rows,
// test_a_subnet_with_only_untested_hosts_is_not_silent,
// test_silent_subnets_follow_the_order_of_the_subnets_and_a_host_in_two_belongs_to_both,
// test_without_subnets_nothing_is_silent, the six test_subnets_of_* cases and
// the 2,880-round unseen-row fixture of test_unseen_rows_formats.

var coverageT0 = time.Unix(1_791_500_000, 0).UTC()

func coverageAt(seconds int) time.Time { return coverageT0.Add(time.Duration(seconds) * time.Second) }

type coverageRound struct {
	at           int
	observations string
}

func coverageFold(t *testing.T, hosts []string, subnets []Subnet, rounds ...coverageRound) Coverage {
	t.Helper()
	fold := NewCoverageFold(hosts, subnets)
	for _, round := range rounds {
		if err := fold.Add(coverageAt(round.at), round.observations); err != nil {
			t.Fatalf("Add(%d, %q): %v", round.at, round.observations, err)
		}
	}
	return fold.Coverage()
}

func coverageStatuses(coverage Coverage) []string {
	statuses := make([]string, 0, len(coverage.Hosts))
	for _, host := range coverage.Hosts {
		statuses = append(statuses, host.Status)
	}
	return statuses
}

func coverageNotes(coverage Coverage) []string {
	notes := make([]string, 0, len(coverage.Hosts))
	for _, host := range coverage.Hosts {
		notes = append(notes, host.Note)
	}
	return notes
}

func TestCoverageStatusFromObservations(t *testing.T) {
	for _, tc := range []struct {
		observations []string
		want         string
	}{
		{[]string{"!"}, CoverageSeen},
		{[]string{"R"}, CoverageSeen},
		{[]string{"T"}, CoverageSeen},
		{[]string{".", "!", "."}, CoverageSeen},
		{[]string{"E", "U", "?", "R"}, CoverageSeen},
		{[]string{".", "."}, CoverageUnseen},
		{[]string{"U"}, CoverageUnseen},
		{[]string{".", "U", "E", "?"}, CoverageUnseen},
		{[]string{"E"}, CoverageNotTested},
		{[]string{"?"}, CoverageNotTested},
		{[]string{"E", "?", "E"}, CoverageNotTested},
	} {
		t.Run(fmt.Sprint(tc.observations), func(t *testing.T) {
			var rounds []coverageRound
			for step, observation := range tc.observations {
				rounds = append(rounds, coverageRound{60 * step, observation})
			}
			coverage := coverageFold(t, []string{"10.0.0.1"}, nil, rounds...)
			if got := coverage.Hosts[0].Status; got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCoverageFailedRoundsAreNotEvidence(t *testing.T) {
	coverage := coverageFold(t, []string{"10.0.0.1"}, nil, coverageRound{0, "."}, coverageRound{60, "E"}, coverageRound{120, "."})
	host := coverage.Hosts[0]
	if host.Status != CoverageUnseen || host.Answered != 0 || host.Tested != 2 || host.Failed != 1 {
		t.Fatalf("host = %+v, want Unseen with 0 answered, 2 tested, 1 failed", host)
	}
	if coverage.Rounds != 3 {
		t.Fatalf("Rounds = %d, want 3: a failed round still counts as a round read", coverage.Rounds)
	}
}

func TestCoverageUnresolvedNameIsAFailedRoundToo(t *testing.T) {
	host := coverageFold(t, []string{"10.0.0.1"}, nil, coverageRound{0, "?"}, coverageRound{60, "."}, coverageRound{120, "!"}).Hosts[0]
	if host.Answered != 1 || host.Tested != 2 || host.Failed != 1 {
		t.Fatalf("host = %+v, want 1 answered, 2 tested, 1 failed", host)
	}
}

func TestCoverageReadsEachHostAtItsOwnPosition(t *testing.T) {
	hosts := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}
	coverage := coverageFold(t, hosts, nil, coverageRound{0, "!.E."}, coverageRound{60, ".!E."}, coverageRound{120, "..EU"})
	if got, want := coverageStatuses(coverage), []string{CoverageSeen, CoverageSeen, CoverageNotTested, CoverageUnseen}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	type counts struct{ answered, tested, failed int }
	var got []counts
	for i, host := range coverage.Hosts {
		if host.Host != hosts[i] {
			t.Fatalf("host %d = %q, want %q", i, host.Host, hosts[i])
		}
		got = append(got, counts{host.Answered, host.Tested, host.Failed})
	}
	if want := []counts{{1, 3, 0}, {1, 3, 0}, {0, 0, 3}, {0, 3, 0}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
	for _, note := range coverageNotes(coverage) {
		if note != "" {
			t.Fatalf("notes = %v, want none without subnets", coverageNotes(coverage))
		}
	}
}

func TestCoverageSinceIsTheOldestRoundWhateverTheOrder(t *testing.T) {
	coverage := coverageFold(t, []string{"10.0.0.1"}, nil, coverageRound{120, "."}, coverageRound{0, "E"}, coverageRound{60, "!"})
	if coverage.Since == nil || !coverage.Since.Equal(coverageT0) || coverage.Rounds != 3 {
		t.Fatalf("Since = %v, Rounds = %d; want %v and 3", coverage.Since, coverage.Rounds, coverageT0)
	}
}

func TestCoverageWithoutRoundsIsNotTested(t *testing.T) {
	hosts := []string{"10.0.0.1", "10.0.0.2"}
	coverage := coverageFold(t, hosts, []Subnet{{Token: "10.0.0.0/30", Members: []int{0, 1}}})
	if coverage.Rounds != 0 || coverage.Since != nil || len(coverage.SilentSubnets) != 0 {
		t.Fatalf("coverage = %+v, want no rounds, no since and no silent subnet", coverage)
	}
	if got, want := coverageStatuses(coverage), []string{CoverageNotTested, CoverageNotTested}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	for _, host := range coverage.Hosts {
		if host.Answered != 0 || host.Tested != 0 || host.Failed != 0 || host.Note != "" {
			t.Fatalf("host = %+v, want zero counts and no note", host)
		}
	}
}

func TestCoverageAddRejectsRoundsThatDoNotFitAndLeavesTheFoldUnchanged(t *testing.T) {
	for _, observations := range []string{"!", "!!!", "", "!x", "!!#", "! "} {
		t.Run(fmt.Sprintf("%q", observations), func(t *testing.T) {
			fold := NewCoverageFold([]string{"10.0.0.1", "10.0.0.2"}, nil)
			if err := fold.Add(coverageAt(0), "!."); err != nil {
				t.Fatal(err)
			}
			before := fold.Coverage()
			if err := fold.Add(coverageAt(60), observations); err == nil {
				t.Fatalf("Add accepted %q for two hosts", observations)
			}
			if after := fold.Coverage(); !reflect.DeepEqual(after, before) {
				t.Fatalf("a rejected round changed the fold: %+v, was %+v", after, before)
			}
		})
	}
}

func TestCoverageSilentSubnetNotesOnlyItsUnseenRows(t *testing.T) {
	hosts := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.1.0.1", "10.1.0.2", "10.2.0.1"}
	subnets := []Subnet{
		{Token: "10.0.0.0/30", Members: []int{0, 1, 2}},
		{Token: "10.1.0.0/30", Members: []int{3, 4}},
		{Token: "10.2.0.0/30", Members: []int{5}},
	}
	// 10.0.0.0/30 never answered (host 2 could not be tested); 10.1.0.0/30
	// answered once, so its silent host 10.1.0.1 gets no note; 10.2.0.0/30 only failed.
	coverage := coverageFold(t, hosts, subnets, coverageRound{0, "..E.!E"}, coverageRound{60, "..E..E"})
	if got, want := coverageStatuses(coverage), []string{CoverageUnseen, CoverageUnseen, CoverageNotTested, CoverageUnseen, CoverageSeen, CoverageNotTested}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	if got, want := coverageNotes(coverage), []string{SilentSubnetNote, SilentSubnetNote, "", "", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("notes = %v, want %v", got, want)
	}
	if want := []string{"10.0.0.0/30"}; !reflect.DeepEqual(coverage.SilentSubnets, want) {
		t.Fatalf("SilentSubnets = %v, want %v", coverage.SilentSubnets, want)
	}
}

func TestCoverageSubnetWithOnlyUntestedHostsIsNotSilent(t *testing.T) {
	coverage := coverageFold(t, []string{"10.0.0.1", "10.0.0.2"}, []Subnet{{Token: "10.0.0.0/30", Members: []int{0, 1}}}, coverageRound{0, "EE"})
	if got, want := coverageStatuses(coverage), []string{CoverageNotTested, CoverageNotTested}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	if len(coverage.SilentSubnets) != 0 || !reflect.DeepEqual(coverageNotes(coverage), []string{"", ""}) {
		t.Fatalf("coverage = %+v, want no silent subnet and no note", coverage)
	}
}

func TestCoverageSilentSubnetsFollowTokenOrderAndAHostInTwoBelongsToBoth(t *testing.T) {
	hosts := []string{"10.0.0.1", "10.0.0.2", "10.0.0.5", "10.0.0.6"}
	subnets := []Subnet{
		{Token: "10.0.0.4/30", Members: []int{2, 3}},
		{Token: "10.0.0.0/29", Members: []int{0, 1, 2, 3}},
		{Token: "10.0.0.0/30", Members: []int{0, 1}},
	}
	coverage := coverageFold(t, hosts, subnets, coverageRound{0, "..!."})
	// 10.0.0.4/30 holds 10.0.0.5, which answered; /29 holds it too; only /30 is silent.
	if want := []string{"10.0.0.0/30"}; !reflect.DeepEqual(coverage.SilentSubnets, want) {
		t.Fatalf("SilentSubnets = %v, want %v", coverage.SilentSubnets, want)
	}
	if got, want := coverageNotes(coverage), []string{SilentSubnetNote, SilentSubnetNote, "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("notes = %v, want %v", got, want)
	}

	silentBoth := coverageFold(t, hosts, subnets, coverageRound{0, "...."})
	if want := []string{"10.0.0.4/30", "10.0.0.0/29", "10.0.0.0/30"}; !reflect.DeepEqual(silentBoth.SilentSubnets, want) {
		t.Fatalf("SilentSubnets = %v, want %v in token order", silentBoth.SilentSubnets, want)
	}
	for _, note := range coverageNotes(silentBoth) {
		if note != SilentSubnetNote {
			t.Fatalf("notes = %v, want the note on every host, once", coverageNotes(silentBoth))
		}
	}
}

func TestCoverageWithoutSubnetsNothingIsSilent(t *testing.T) {
	coverage := coverageFold(t, []string{"10.0.0.1"}, nil, coverageRound{0, "."})
	if len(coverage.SilentSubnets) != 0 || coverage.Hosts[0].Note != "" {
		t.Fatalf("coverage = %+v, want nothing silent", coverage)
	}
}

func TestCoverageIsNotChangedByLaterAddsToACopy(t *testing.T) {
	fold := NewCoverageFold([]string{"10.0.0.1"}, nil)
	if err := fold.Add(coverageAt(0), "."); err != nil {
		t.Fatal(err)
	}
	first := fold.Coverage()
	if err := fold.Add(coverageAt(60), "!"); err != nil {
		t.Fatal(err)
	}
	if first.Rounds != 1 || first.Hosts[0].Status != CoverageUnseen || first.Since == nil || !first.Since.Equal(coverageT0) {
		t.Fatalf("an earlier Coverage changed after a later Add: %+v", first)
	}
}

// The 2,880-round fixture of test_unseen_rows_formats: host 1 answers, host 2
// never does in 2,880 rounds, host 3 is silent in half of them and fails in the rest.
func TestCoverageUnseenHoldsUnseenAndNotTestedHostsInHostOrder(t *testing.T) {
	hosts := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	fold := NewCoverageFold(hosts, []Subnet{{Token: "10.0.0.0/30", Members: []int{0, 1, 2}}})
	for step := 0; step < 2880; step++ {
		third := "."
		if step%2 == 1 {
			third = "E"
		}
		if err := fold.Add(coverageAt(60*step), "!."+third); err != nil {
			t.Fatal(err)
		}
	}
	coverage := fold.Coverage()
	want := []HostCoverage{
		{Host: "10.0.0.2", Status: CoverageUnseen, Answered: 0, Tested: 2880, Failed: 0},
		{Host: "10.0.0.3", Status: CoverageUnseen, Answered: 0, Tested: 1440, Failed: 1440},
	}
	if got := coverage.Unseen(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unseen() = %+v, want %+v", got, want)
	}

	// test_unseen_rows_hold_unseen_and_not_tested_in_host_order_with_the_silent_note
	hosts = []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.1.0.1"}
	coverage = coverageFold(t, hosts, []Subnet{{Token: "10.0.0.0/30", Members: []int{0, 1, 2}}, {Token: "10.1.0.0/30", Members: []int{3}}},
		coverageRound{0, "!.E."}, coverageRound{60, "!.E."})
	want = []HostCoverage{
		{Host: "10.0.0.2", Status: CoverageUnseen, Answered: 0, Tested: 2, Failed: 0},
		{Host: "10.0.0.3", Status: CoverageNotTested, Answered: 0, Tested: 0, Failed: 2},
		{Host: "10.1.0.1", Status: CoverageUnseen, Answered: 0, Tested: 2, Failed: 0, Note: SilentSubnetNote},
	}
	if got := coverage.Unseen(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unseen() = %+v, want %+v", got, want)
	}

	// test_unseen_rows_of_a_line_where_everything_replied_are_empty
	replied := coverageFold(t, []string{"10.0.0.1"}, nil, coverageRound{0, "!"})
	if got := replied.Unseen(); got == nil || len(got) != 0 {
		t.Fatalf("Unseen() = %#v, want a non-nil empty list", got)
	}
	if got := coverageFold(t, nil, nil).Unseen(); len(got) != 0 {
		t.Fatalf("Unseen() of no hosts = %+v, want empty", got)
	}
}

func subnetStrings(subnets []Subnet) []string {
	out := make([]string, 0, len(subnets))
	for _, subnet := range subnets {
		out = append(out, fmt.Sprintf("%s=%v", subnet.Token, subnet.Members))
	}
	return out
}

func TestSubnetsOf(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tokens, hosts []string
		want          []string
	}{
		{"ignores names and single addresses",
			[]string{"10.0.0.0/30", "host.example.com", "10.1.1.1"},
			[]string{"10.0.0.1", "10.0.0.2", "host.example.com", "10.1.1.1"},
			[]string{"10.0.0.0/30=[0 1]"}},
		{"without a network is empty (names)",
			[]string{"host.example.com", "10.1.1.1"}, []string{"10.1.1.1"}, []string{}},
		{"without a network is empty (no tokens)",
			[]string{}, []string{"10.1.1.1"}, []string{}},
		{"lists indexes in host order and a host in two subnets belongs to both",
			[]string{"10.0.0.4/30", "10.0.0.0/29", "10.9.9.0/24"},
			[]string{"10.0.0.6", "10.0.0.1", "10.0.0.5", "10.0.0.2", "192.0.2.9"},
			[]string{"10.0.0.4/30=[0 2]", "10.0.0.0/29=[0 1 2 3]", "10.9.9.0/24=[]"}},
		{"reads both families and keeps them apart, a zoned host outside",
			[]string{"2001:db8::/126", "10.0.0.0/8"},
			[]string{"10.0.0.1", "2001:db8::1", "2001:db8::2", "2001:db8::5", "fe80::1%eth0"},
			[]string{"2001:db8::/126=[1 2]", "10.0.0.0/8=[0]"}},
		{"reads a zoned host inside a link-local subnet without its zone",
			[]string{"fe80::/64"}, []string{"fe80::1%eth0", "fe80:1::1", "10.0.0.1"},
			[]string{"fe80::/64=[0]"}},
		{"reads a network written with host bits set",
			[]string{"10.0.0.5/30"}, []string{"10.0.0.1", "10.0.0.5", "10.0.0.6"},
			[]string{"10.0.0.5/30=[1 2]"}},
		{"skips a target that is not a network",
			[]string{"site/one", "10.0.0.0/30"}, []string{"10.0.0.1"},
			[]string{"10.0.0.0/30=[0]"}},
		{"skips a host that is not an address",
			[]string{"10.0.0.0/30"}, []string{"not-an-ip", "10.0.0.2"},
			[]string{"10.0.0.0/30=[1]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := subnetStrings(SubnetsOf(tc.tokens, tc.hosts)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SubnetsOf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubnetsOfTokenIsKeptAsWritten(t *testing.T) {
	got := SubnetsOf([]string{"10.0.0.5/30"}, []string{"10.0.0.1"})
	if len(got) != 1 || got[0].Token != "10.0.0.5/30" {
		t.Fatalf("SubnetsOf = %+v, want the token as written even though host bits are masked for membership", got)
	}
}

// TestSubnetsOfDoesNotScaleWithTokensTimesHosts guards the hosts-sorted-once,
// bisect-per-token design of cn-tool's subnets_of. 65,536 /30 tokens over
// 262,144 hosts, in reverse order so the sort has work to do, is about
// 1.7e10 membership tests for a pass over every host per token, which no
// machine finishes within the bound, and well under a second for the sorted
// search. The bound is two orders of magnitude from either side, so the test
// does not depend on machine speed. There is no size limit: a /14 of /30s is
// an input a request can hold.
func TestSubnetsOfDoesNotScaleWithTokensTimesHosts(t *testing.T) {
	const hostCount, tokenCount = 1 << 18, 1 << 16
	base := netip.MustParseAddr("10.0.0.0").As4()
	address := func(n int) netip.Addr {
		return netip.AddrFrom4([4]byte{base[0], byte(n >> 16), byte(n >> 8), byte(n)})
	}
	hosts := make([]string, hostCount)
	for i := range hosts {
		hosts[i] = address(hostCount - 1 - i).String() // reversed: index i holds address N-1-i
	}
	tokens := make([]string, tokenCount)
	for j := range tokens {
		tokens[j] = netip.PrefixFrom(address(4*j), 30).String()
	}

	done := make(chan []Subnet, 1)
	go func() { done <- SubnetsOf(tokens, hosts) }()
	var subnets []Subnet
	select {
	case subnets = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("SubnetsOf did not finish 65,536 tokens over 262,144 hosts within 30s: its cost has returned to tokens x hosts")
	}
	if len(subnets) != tokenCount {
		t.Fatalf("subnets = %d, want %d in token order", len(subnets), tokenCount)
	}
	for _, j := range []int{0, 1, tokenCount / 2, tokenCount - 1} {
		// Addresses 4j..4j+3 sit at indexes N-1-(4j+3)..N-1-4j, ascending.
		first := hostCount - 1 - (4*j + 3)
		want := []int{first, first + 1, first + 2, first + 3}
		if subnets[j].Token != tokens[j] || !reflect.DeepEqual(subnets[j].Members, want) {
			t.Fatalf("subnet %d = %+v, want token %s with members %v", j, subnets[j], tokens[j], want)
		}
	}
}
