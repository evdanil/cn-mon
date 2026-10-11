// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"reflect"
	"strings"
	"testing"

	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

func TestNewLiveLayoutAddsSavedHostsMissingFromTargetBlocks(t *testing.T) {
	request := target.Request{
		Tokens: []string{"192.0.2.0/30"},
		Hosts:  []string{"192.0.2.1", "192.0.2.2", "old.example", "other.example"},
	}
	layout := newLiveLayout(request)
	var hosts []string
	for _, block := range layout.blocks {
		if block.title == "Hosts" {
			for _, name := range block.names {
				hosts = append(hosts, name.text)
			}
		}
	}
	if want := []string{"old.example", "other.example"}; !reflect.DeepEqual(hosts, want) {
		t.Fatalf("fallback Hosts block = %v, want saved-order extras %v", hosts, want)
	}
}

func TestLiveGridShowsObservationForNoncanonicalSavedIPInSubnet(t *testing.T) {
	request := target.Request{
		Tokens: []string{"2001:db8::/126"},
		Hosts:  []string{"2001:0DB8:0:0:0:0:0:1"},
	}
	update := monitor.LiveUpdate{
		Request: request,
		Data: monitor.LiveData{
			Cells: []byte{monitor.EchoReply},
			Seen:  1,
		},
	}
	grid := joinLines(newLiveLayout(request).grid(update, 100, 20))
	if !strings.Contains(grid, "2001:0DB8:0:0:0:0:0:1 !") {
		t.Fatalf("live grid omits saved host observation: %q", grid)
	}
}
