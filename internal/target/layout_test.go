// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package target

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestLayoutInputsPreservesFirstOccurrenceBlocksAndLabels(t *testing.T) {
	inputs := LayoutInputs([]string{
		"10.0.0.0/30", "10.0.0.1", "10.0.0.0/30", "10.0.0.3",
		"192.0.2.5", "192.0.2.5", "192.0.2.6,192.0.2.7",
	})
	want := []LayoutInput{
		{Network: netip.MustParsePrefix("10.0.0.0/30")},
		{Hosts: []string{"10.0.0.3"}},
		{Hosts: []string{"192.0.2.5"}},
		{Hosts: []string{"192.0.2.6", "192.0.2.7"}},
	}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("LayoutInputs = %#v, want %#v", inputs, want)
	}
}

func TestLayoutInputsRetainsPartiallyOverlappingSubnetGrid(t *testing.T) {
	inputs := LayoutInputs([]string{"10.0.0.0", "10.0.0.0/30", "10.0.0.1"})
	want := []LayoutInput{{Hosts: []string{"10.0.0.0"}}, {Network: netip.MustParsePrefix("10.0.0.0/30")}}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("LayoutInputs = %#v, want %#v", inputs, want)
	}
}

func TestLayoutInputsOmitsSubnetWithNoNewUsableHosts(t *testing.T) {
	inputs := LayoutInputs([]string{"10.0.0.1", "10.0.0.2", "10.0.0.0/30"})
	want := []LayoutInput{{Hosts: []string{"10.0.0.1"}}, {Hosts: []string{"10.0.0.2"}}}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("LayoutInputs = %#v, want %#v", inputs, want)
	}
}
