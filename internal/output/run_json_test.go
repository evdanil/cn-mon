// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

func TestWriteRunJSONEmptyReportHasFourArrays(t *testing.T) {
	var got bytes.Buffer
	err := WriteRunJSON(&got, target.Request{}, monitor.RunOutcome{}, nil)
	if err != nil {
		t.Fatalf("WriteRunJSON: %v", err)
	}
	want := "{\n" +
		"  \"seen\": [],\n" +
		"  \"latest\": [],\n" +
		"  \"summary\": [],\n" +
		"  \"not_found\": []\n" +
		"}\n"
	if got.String() != want {
		t.Fatalf("JSON = %q, want %q", got.String(), want)
	}
}

func TestWriteRunJSONSourceGeneratedGolden(t *testing.T) {
	alphaFirst := time.Date(2026, time.January, 1, 4, 5, 6, 900, time.UTC)
	alphaLast := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	zetaFirst := time.Date(2026, time.January, 2, 3, 5, 0, 0, time.UTC)
	zetaLast := time.Date(2026, time.January, 2, 3, 6, 0, 0, time.UTC)
	started := time.Date(2026, time.January, 2, 3, 0, 0, 0, time.UTC)
	ended := time.Date(2026, time.January, 2, 3, 6, 0, 0, time.UTC)
	interval := 1500 * time.Millisecond
	request := target.Request{
		Key:   "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		Hosts: []string{"alpha.example", "zeta.example"},
		Ports: []int{443, 80},
	}
	outcome := monitor.RunOutcome{
		Stats: monitor.Stats{
			Rounds: 3, Seen: 2, Answering: 1,
			FirstRound: started, LastRound: ended,
		},
		Seen: []monitor.HostSummary{
			{ // Deliberately reversed; output follows request host order.
				Host: "zeta.example", Address: "2001:db8::2", Latest: "NO RESPONSE",
				TCP:       map[int]string{443: "error", 80: "closed"},
				FirstSeen: zetaFirst, LastSeen: zetaLast, SeenRounds: 1, Rounds: 3,
				Periods: []monitor.Period{{From: zetaFirst, To: zetaLast}},
			},
			{
				Host: "alpha.example", Address: "A <&> \"雪\"", Latest: "OK (1/2 replies)",
				TCP:       map[int]string{443: "open", 80: ""},
				FirstSeen: alphaFirst, LastSeen: alphaLast, SeenRounds: 2, Rounds: 3, Ongoing: true,
				Periods: []monitor.Period{
					{From: alphaFirst, To: alphaFirst},
					{From: alphaLast, To: alphaLast},
				},
			},
		},
		Latest: []monitor.LatestRow{
			{Host: "zeta.example", Row: monitor.Row{Address: "2001:db8::2", Result: "NO RESPONSE", TCP: map[int]string{443: "error", 80: "closed"}}},
			{Host: "alpha.example", Row: monitor.Row{Address: "A <&> \"雪\"", Result: "OK (1/2 replies)", TCP: map[int]string{443: "open", 80: ""}}},
		},
		CurrentRounds: 1, EarlierRounds: 2, Interval: &interval,
		Unresolved: []string{"zulu.invalid", "alpha.invalid"},
	}
	rejected := []target.Rejection{
		{Object: "late <&> \"x\"", Reason: "invalid <target>"},
		{Object: "first", Reason: "bad input"},
	}
	var got bytes.Buffer
	if err := WriteRunJSON(&got, request, outcome, rejected); err != nil {
		t.Fatalf("WriteRunJSON: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "run-json-source-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("JSON golden mismatch\n got: %s\nwant: %s", got.String(), want)
	}
}

func TestWriteRunJSONEarlierOnlySummaryAndSparseLatest(t *testing.T) {
	first := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	var got bytes.Buffer
	outcome := monitor.RunOutcome{
		Stats:         monitor.Stats{Rounds: 2, Seen: 1, Answering: 0, FirstRound: first, LastRound: first.Add(time.Minute)},
		Latest:        []monitor.LatestRow{{Host: "host.example", Row: monitor.Row{Result: "NO RESPONSE"}}},
		EarlierRounds: 2, CurrentRounds: 0, Interval: nil,
	}
	if err := WriteRunJSON(&got, target.Request{Key: "12345678" + strings.Repeat("0", 56), Hosts: []string{"host.example"}}, outcome, nil); err != nil {
		t.Fatalf("WriteRunJSON: %v", err)
	}
	var decoded struct {
		Latest  []map[string]any `json:"latest"`
		Summary []map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(got.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(decoded.Latest) != 1 || decoded.Latest[0]["address"] != "" || decoded.Latest[0]["result"] != "NO RESPONSE" {
		t.Fatalf("sparse historical latest = %+v", decoded.Latest)
	}
	if len(decoded.Summary) != 1 || decoded.Summary[0]["rounds"] != float64(0) || decoded.Summary[0]["earlier_rounds"] != float64(2) || decoded.Summary[0]["interval"] != nil {
		t.Fatalf("earlier-only summary = %+v", decoded.Summary)
	}
}

type jsonFailWriter struct{ err error }

func (w jsonFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteRunJSONReturnsWriterFailure(t *testing.T) {
	want := errors.New("closed report pipe")
	err := WriteRunJSON(jsonFailWriter{err: want}, target.Request{}, monitor.RunOutcome{}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("WriteRunJSON error = %v, want wrapped %v", err, want)
	}
}

func TestFormatTimestampKeepsUTCSecondPrecisionAndLowYears(t *testing.T) {
	for _, test := range []struct {
		when time.Time
		want string
	}{
		{time.Date(7, time.February, 3, 4, 5, 6, 123, time.FixedZone("east", 90*60)), "0007-02-03T02:35:06+00:00"},
		{time.Date(2026, time.January, 2, 3, 4, 5, 999, time.UTC), "2026-01-02T03:04:05+00:00"},
	} {
		if got := formatTimestamp(test.when); got != test.want {
			t.Errorf("formatTimestamp(%s) = %q, want %q", test.when, got, test.want)
		}
	}
}

func TestWriteRunJSONSortsTCPFieldsNumerically(t *testing.T) {
	var got bytes.Buffer
	request := target.Request{Hosts: []string{"h"}, Ports: []int{1000, 443, 80}}
	outcome := monitor.RunOutcome{Latest: []monitor.LatestRow{{
		Host: "h", Row: monitor.Row{TCP: map[int]string{1000: "high", 80: "low", 443: "middle"}},
	}}}
	if err := WriteRunJSON(&got, request, outcome, nil); err != nil {
		t.Fatalf("WriteRunJSON: %v", err)
	}
	text := got.String()
	positions := []int{strings.Index(text, `"tcp_80"`), strings.Index(text, `"tcp_443"`), strings.Index(text, `"tcp_1000"`)}
	for i, position := range positions {
		if position < 0 {
			t.Fatalf("TCP field %d missing from report: %s", i, text)
		}
	}
	if !(positions[0] < positions[1] && positions[1] < positions[2]) {
		t.Fatalf("TCP keys are not numerically ordered: %s", text)
	}
}
