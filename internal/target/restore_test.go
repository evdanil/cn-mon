// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package target

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRestorePreservesPythonHistoryFixtureIdentityAndHosts(t *testing.T) {
	stored := Request{
		Key:    "24156eb156b5bb9d83f75c5e4391db9736dee2ae690bd2c99e8154414d124fff",
		Tokens: []string{"api.example.com", "db.example.com"},
		Hosts:  []string{"api.example.com", "db.example.com", "legacy.example.com", "timeout.example.com", "unreachable.example.com", "unresolved.example.com", "future.example.com"},
	}
	got, err := Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("Restore() = %#v, want exact saved request %#v", got, stored)
	}
}

func TestRestorePreservesCanonicalArraysAndStoredKeySpelling(t *testing.T) {
	stored := requestWithKey([]string{"192.0.2.0/30", "host.example"}, []int{22, 443}, []string{"192.0.2.1", "192.0.2.2", "host.example"})
	stored.Key = strings.ToUpper(stored.Key)
	got, err := Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("Restore() = %#v, want exact arrays and key spelling %#v", got, stored)
	}
}

func TestRestorePreservesCanonicalNetworkTokenWithoutReexpandingHosts(t *testing.T) {
	stored := requestWithKey([]string{"192.0.0.0/16"}, nil, []string{"legacy.example"})
	got, err := Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("Restore() = %#v, want saved request %#v", got, stored)
	}
}

func TestRestorePreservesValidNoncanonicalSavedAddressSpelling(t *testing.T) {
	stored := requestWithKey([]string{"2001:db8::/126"}, nil, []string{"2001:0DB8:0:0:0:0:0:1"})
	got, err := Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("Restore() = %#v, want exact saved request %#v", got, stored)
	}
}

func TestRestoreReturnsDefensiveCopies(t *testing.T) {
	input := requestWithKey([]string{"a.example"}, []int{80}, []string{"a.example"})
	want := requestWithKey([]string{"a.example"}, []int{80}, []string{"a.example"})
	got, err := Restore(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Tokens[0], input.Ports[0], input.Hosts[0] = "changed.example", 81, "changed.example"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating input changed restored result: got %#v, want %#v", got, want)
	}

	input = requestWithKey([]string{"a.example"}, []int{80}, []string{"a.example"})
	wantInput := requestWithKey([]string{"a.example"}, []int{80}, []string{"a.example"})
	got, err = Restore(input)
	if err != nil {
		t.Fatal(err)
	}
	got.Tokens[0], got.Ports[0], got.Hosts[0] = "returned.example", 82, "returned.example"
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("mutating restored result changed input: got %#v, want %#v", input, wantInput)
	}
}

func TestRestoreAllowsSavedHostCountAboveTCPCapWithoutTCP(t *testing.T) {
	hosts := make([]string, 1025)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("host-%d.example", i)
	}
	stored := requestWithKey([]string{"a.example"}, nil, hosts)
	got, err := Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != len(hosts) || got.Hosts[len(hosts)-1] != hosts[len(hosts)-1] {
		t.Fatalf("Restore changed saved host list: got %d hosts, last %q", len(got.Hosts), got.Hosts[len(got.Hosts)-1])
	}
}

func TestRestoreAllowsValidEmptySavedArrays(t *testing.T) {
	stored := requestWithKey(nil, nil, nil)
	got, err := Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, stored) {
		t.Fatalf("Restore() = %#v, want empty saved request %#v", got, stored)
	}
	emptyArrays := requestWithKey([]string{}, []int{}, []string{})
	got, err = Restore(emptyArrays)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, emptyArrays) {
		t.Fatalf("Restore() = %#v, want non-nil empty arrays %#v", got, emptyArrays)
	}
}

func TestRestoreRejectsInvalidRequestDataWithMatchingDigest(t *testing.T) {
	valid := requestWithKey([]string{"a.example"}, []int{80}, []string{"a.example"})
	tooManyPorts := []int{1, 2, 3, 4, 5, 6}
	tooManyHosts := make([]string, 1025)
	for i := range tooManyHosts {
		tooManyHosts[i] = fmt.Sprintf("host-%d.example", i)
	}
	cases := []struct {
		name string
		edit func(*Request)
	}{
		{"unsorted tokens", func(r *Request) {
			r.Tokens = []string{"b.example", "a.example"}
			r.Hosts = []string{"b.example", "a.example"}
		}},
		{"duplicate tokens", func(r *Request) { r.Tokens = []string{"a.example", "a.example"} }},
		{"noncanonical token", func(r *Request) { r.Tokens = []string{"A.example"}; r.Hosts = []string{"A.example"} }},
		{"invalid token", func(r *Request) { r.Tokens = []string{"bad_name"}; r.Hosts = []string{"bad_name"} }},
		{"invalid utf8 token", func(r *Request) { r.Tokens = []string{string([]byte{0xff})} }},
		{"unsorted ports", func(r *Request) { r.Ports = []int{443, 80} }},
		{"duplicate ports", func(r *Request) { r.Ports = []int{80, 80} }},
		{"out of range port", func(r *Request) { r.Ports = []int{0} }},
		{"too many ports", func(r *Request) { r.Ports = tooManyPorts }},
		{"duplicate hosts", func(r *Request) { r.Hosts = []string{"a.example", "a.example"} }},
		{"invalid single host", func(r *Request) { r.Hosts = []string{"192.0.2.0/24"} }},
		{"scoped address with prefix", func(r *Request) { r.Hosts = []string{"fe80::1%eth0/64"} }},
		{"scoped address list", func(r *Request) { r.Hosts = []string{"fe80::1%eth0, fe80::2"} }},
		{"multiple scoped address percents", func(r *Request) { r.Hosts = []string{"fe80::1%eth0%extra"} }},
		{"scoped address whitespace", func(r *Request) { r.Hosts = []string{"fe80::1%eth 0"} }},
		{"malformed numeric address", func(r *Request) { r.Hosts = []string{"999.999.999.999"} }},
		{"mapped IPv4 address", func(r *Request) { r.Hosts = []string{"::ffff:192.0.2.1"} }},
		{"unscoped link-local IPv6", func(r *Request) { r.Hosts = []string{"fe80::1"} }},
		{"invalid utf8 host", func(r *Request) { r.Hosts = []string{string([]byte{0xff})} }},
		{"too many tcp hosts", func(r *Request) { r.Ports = []int{80}; r.Hosts = tooManyHosts }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			r.Tokens = append([]string(nil), valid.Tokens...)
			r.Ports = append([]int(nil), valid.Ports...)
			r.Hosts = append([]string(nil), valid.Hosts...)
			tc.edit(&r)
			r.Key = requestDigest(r)
			if _, err := Restore(r); err == nil {
				t.Fatalf("Restore accepted invalid request: %#v", r)
			}
		})
	}
}

func TestRestoreRejectsMismatchedKey(t *testing.T) {
	stored := requestWithKey([]string{"a.example"}, nil, []string{"a.example"})
	for _, key := range []string{strings.Repeat("0", 64), "", "not-a-digest"} {
		stored.Key = key
		if _, err := Restore(stored); err == nil {
			t.Errorf("Restore accepted invalid key %q", key)
		}
	}
}

func requestWithKey(tokens []string, ports []int, hosts []string) Request {
	r := Request{Tokens: tokens, Ports: ports, Hosts: hosts}
	r.Key = requestDigest(r)
	return r
}

func requestDigest(r Request) string {
	sum := sha256.Sum256([]byte(identityJSON(r.Tokens, r.Ports, r.Hosts)))
	return hex.EncodeToString(sum[:])
}
