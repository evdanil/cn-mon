// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

func TestNewLiveLayoutOwnsRequestAndMatchesAllIdentityFields(t *testing.T) {
	request, _, err := target.Build([]string{"10.0.0.0/30", "host.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	layout := newLiveLayout(request)
	request.Tokens[0] = "mutated"
	request.Hosts[0] = "mutated"
	if layout.matches(request) {
		t.Fatal("layout matched mutated request metadata")
	}
	if len(layout.request.Tokens) == 0 || layout.request.Tokens[0] == "mutated" {
		t.Fatal("layout retained borrowed request token slice")
	}
	if !layout.matches(layout.request) {
		t.Fatal("layout does not match its owned request")
	}
}

func TestNewLiveLayoutCombinesAllLiteralNamesIntoOneHostsBlock(t *testing.T) {
	request, _, err := target.Build([]string{"10.0.0.0/30", "one.example", "two.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	layout := newLiveLayout(request)
	var hostBlocks int
	for _, block := range layout.blocks {
		if block.title == "Hosts" {
			hostBlocks++
			if len(block.names) != 2 {
				t.Fatalf("Hosts block labels = %+v", block.names)
			}
		}
	}
	if hostBlocks != 1 {
		t.Fatalf("got %d Hosts blocks, want one: %+v", hostBlocks, layout.blocks)
	}
}

func TestLiveFrameSubnetGridLegendAndCellStyling(t *testing.T) {
	request, _, err := target.Build([]string{"10.0.0.0/30"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	update := monitor.LiveUpdate{Request: request, Now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), CurrentRounds: 1}
	update.Data.Cells = []byte{monitor.EchoReply, monitor.NoResponse}
	update.Data.Changed = []int{0}
	update.Data.Partial = []int{0}
	update.Data.Seen = 1
	layout := newLiveLayout(request)
	frame := layout.frame(update, 90, 20, false)
	text := joinLines(frame)
	for _, want := range []string{"10.0.0.0/30", ".0", "!. ", "replied", "silent", "1 seen of 2"} {
		if !strings.Contains(text, want) {
			t.Errorf("frame lacks %q: %q", want, text)
		}
	}
	styled := frame[4]
	var hasReverse bool
	for _, segment := range styled {
		if strings.Contains(segment.style, "reverse") {
			hasReverse = true
		}
	}
	if !hasReverse {
		t.Fatal("changed cell lost reverse semantic style")
	}
}

func TestWriteStyledAppliesCompositeCellStylesWithAndWithoutColor(t *testing.T) {
	for _, test := range []struct {
		name  string
		color bool
		want  string
	}{
		{name: "color", color: true, want: "\x1b[38;2;255;165;0m\x1b[7m!\x1b[0m\n"},
		{name: "no color", color: false, want: "\x1b[7m!\x1b[0m\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			if err := writeStyled(&output, []line{{{text: "!", style: "warning reverse"}}}, "default", true, test.color); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("writeStyled() = %q, want %q", got, test.want)
			}
		})
	}

	var output strings.Builder
	if err := writeStyled(&output, []line{{{text: " ", style: "dim reverse"}}}, "default", true, false); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "\x1b[2m\x1b[7m \x1b[0m\n"; got != want {
		t.Fatalf("writeStyled(dim reverse, no color) = %q, want %q", got, want)
	}

	output.Reset()
	if err := writeStyled(&output, []line{{{text: "H", style: "header"}}}, "default", true, true); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "\x1b[38;2;0;206;209m\x1b[1mH\x1b[0m\n"; got != want {
		t.Fatalf("writeStyled(header, color) = %q, want %q", got, want)
	}
}

func TestWriteStyledFrameOmitsOnlyTrailingNewline(t *testing.T) {
	var output strings.Builder
	lines := []line{{{text: "first", style: "text"}}, {{text: "last", style: "header"}}}
	if err := writeStyledFrame(&output, lines, "default", true, false); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "first\n\x1b[1mlast\x1b[0m"; got != want {
		t.Fatalf("frame output = %q, want %q", got, want)
	}
	var report strings.Builder
	if err := writeStyled(&report, lines, "default", true, false); err != nil {
		t.Fatal(err)
	}
	if got, want := report.String(), "first\n\x1b[1mlast\x1b[0m\n"; got != want {
		t.Fatalf("ordinary output = %q, want final newline %q", got, want)
	}
}

func TestLiveHeaderAndCompactCountsMatchSourceSemanticFixture(t *testing.T) {
	var source struct {
		WaitingAfterOne string `json:"waiting_after_one"`
		ResolvingBefore string `json:"resolving_before_first"`
		ProbingAfterOne string `json:"probing_after_one"`
		Compact1024     string `json:"compact_1024"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", "live-header-source-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	request, _, err := target.Build([]string{"host.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waiting := liveHeader(monitor.LiveUpdate{Request: request, Phase: monitor.LiveWaiting, CurrentRounds: 1}, 200)
	if !strings.Contains(waiting, source.WaitingAfterOne) || strings.Contains(waiting, "round 2") {
		t.Errorf("waiting header = %q, want completed-round phrase %q", waiting, source.WaitingAfterOne)
	}
	resolving := liveHeader(monitor.LiveUpdate{Request: request, Phase: monitor.LiveResolving, Total: 1024}, 200)
	if !strings.Contains(resolving, source.ResolvingBefore) || !strings.Contains(resolving, "0 of 1,024 pinged") {
		t.Errorf("resolving header = %q, want source in-flight phrase %q and zero-progress count", resolving, source.ResolvingBefore)
	}
	probing := liveHeader(monitor.LiveUpdate{Request: request, Phase: monitor.LiveProbing, CurrentRounds: 1, Done: 300, Total: 1024}, 200)
	if !strings.Contains(probing, source.ProbingAfterOne) {
		t.Errorf("probing header = %q, want in-flight phrase %q", probing, source.ProbingAfterOne)
	}

	chars := make([]byte, 1024)
	members := make([]int, len(chars))
	for i := range chars {
		chars[i], members[i] = '.', i
	}
	compact := joinLines(compactGrid([]liveBlock{{title: "Hosts", members: members}}, chars, 2, 40))
	if compact != source.Compact1024+"\n" {
		t.Errorf("compact counts = %q, want source fixture %q", compact, source.Compact1024+"\n")
	}

	request.Hosts = make([]string, 1024)
	cells := make([]byte, 1024)
	for i := range cells {
		cells[i] = '!'
	}
	update := monitor.LiveUpdate{Request: request, Data: monitor.LiveData{Cells: cells, Seen: 1024}}
	if got, want := liveCounters(update, 200), "1,024 replying · 1,024 seen of 1,024"; !strings.Contains(got, want) {
		t.Errorf("live counters = %q, want grouped counts containing %q", got, want)
	}
}

func TestLiveFrameCompactFallbackAndSafeTinyDimensions(t *testing.T) {
	request, _, err := target.Build([]string{"10.0.0.0/24"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	update := monitor.LiveUpdate{Request: request, Data: monitor.LiveData{Cells: make([]byte, len(request.Hosts)), Seen: 3}}
	for i := range update.Data.Cells {
		update.Data.Cells[i] = monitor.Unprobed
	}
	update.Data.Cells[0], update.Data.Cells[1], update.Data.Cells[2] = '!', 'R', '.'
	layout := newLiveLayout(request)
	compact := joinLines(layout.frame(update, 25, 12, false))
	if !strings.Contains(compact, "10.0.0.0/24:") || !strings.Contains(compact, "1 !") || !strings.Contains(compact, "1 R") {
		t.Fatalf("compact frame fallback missing counters: %q", compact)
	}
	for _, dimensions := range [][2]int{{1, 1}, {2, 4}, {8, 3}, {120, 4}} {
		frame := layout.frame(update, dimensions[0], dimensions[1], false)
		if len(frame) > dimensions[1] {
			t.Errorf("frame %dx%d has %d lines", dimensions[0], dimensions[1], len(frame))
		}
		for _, line := range frame {
			var text strings.Builder
			for _, part := range line {
				text.WriteString(part.text)
			}
			if displayWidth(text.String()) > dimensions[0] {
				t.Errorf("frame line exceeds %dx%d: %q", dimensions[0], dimensions[1], text.String())
			}
		}
	}
}

func TestLiveHostsGridFallsBackWhenWrappedPairsExceedHeight(t *testing.T) {
	hosts := []string{"host-one.example", "host-two.example", "host-three.example", "host-four.example", "host-five.example", "host-six.example", "host-seven.example", "host-eight.example"}
	request := target.Request{Key: "layout", Tokens: append([]string(nil), hosts...), Hosts: append([]string(nil), hosts...)}
	cells := make([]byte, len(hosts))
	for i := range cells {
		cells[i] = '!'
	}
	got := joinLines(newLiveLayout(request).grid(monitor.LiveUpdate{Request: request, Data: monitor.LiveData{Cells: cells}}, 80, 2))
	if got != "Hosts: 8 !\n" {
		t.Fatalf("too-tall Hosts grid should use source counters; got %q", got)
	}
}

func TestLiveFrameTableRetainsRowsAndUsesFollowingFooter(t *testing.T) {
	request, _, err := target.Build([]string{"alpha.example", "beta.example", "gamma.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	update := monitor.LiveUpdate{Request: request, Phase: monitor.LiveFollowing, Data: monitor.LiveData{Seen: 3}}
	for i, host := range request.Hosts {
		update.Data.Rows = append(update.Data.Rows, monitor.LiveRow{Index: i, Host: host, Latest: "OK", Cell: monitor.EchoReply, SeenRounds: 1, Rounds: 1})
	}
	frame := newLiveLayout(request).frame(update, 80, 12, true)
	text := joinLines(frame)
	for _, want := range []string{"alpha.example", "beta.example", "gamma.example", "q: stop following, not the writer"} {
		if !strings.Contains(text, want) {
			t.Errorf("table frame lacks %q: %q", want, text)
		}
	}
}

func TestAddressAtAdvancesIPv4AndIPv6WithoutSplittingAddresses(t *testing.T) {
	for _, test := range []struct {
		prefix string
		offset uint64
		want   string
	}{{"192.0.2.0/24", 255, "192.0.2.255"}, {"2001:db8::/120", 255, "2001:db8::ff"}} {
		prefix := netip.MustParsePrefix(test.prefix)
		if got := addressAt(prefix, test.offset).String(); got != test.want {
			t.Errorf("addressAt(%s,%d) = %s, want %s", prefix, test.offset, got, test.want)
		}
	}
}
