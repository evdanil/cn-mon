// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestWriteRunStructuredFormatsMatchSourceRendererGoldens(t *testing.T) {
	request, outcome := formatFixture()
	for _, format := range []string{"csv", "md"} {
		t.Run(format, func(t *testing.T) {
			var got bytes.Buffer
			if err := WriteRun(&got, request, outcome, nil, ReportOptions{Format: format, Theme: "dark", Terminal: true}); err != nil {
				t.Fatalf("WriteRun: %v", err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "run-human-source-v1."+format))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("%s output mismatch\n got: %q\nwant: %q", format, got.Bytes(), want)
			}
		})
	}
}

func TestWriteRunStructuredOutputIgnoresThemeAndTerminalSanitization(t *testing.T) {
	request, outcome := formatFixture()
	var plain, decorated bytes.Buffer
	if err := WriteRun(&plain, request, outcome, nil, ReportOptions{Format: "csv"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteRun(&decorated, request, outcome, nil, ReportOptions{Format: "csv", Theme: "pastel", Terminal: true, Width: 1}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain.Bytes(), decorated.Bytes()) {
		t.Fatal("CSV changed with terminal/theme options")
	}
	if strings.Contains(plain.String(), "\x1b[") || !strings.Contains(plain.String(), "\x01") || !strings.Contains(plain.String(), "\x1braw") {
		t.Fatal("structured report lost source control bytes")
	}
}

func TestWriteRunHumanEmptySectionsAndWriterError(t *testing.T) {
	for _, format := range []string{"csv", "md", "table"} {
		var got bytes.Buffer
		if err := WriteRun(&got, target.Request{}, monitor.RunOutcome{}, nil, ReportOptions{Format: format}); err != nil {
			t.Fatalf("%s empty report: %v", format, err)
		}
		if got.Len() != 0 {
			t.Errorf("%s empty report = %q", format, got.String())
		}
	}
	wantErr := errors.New("writer failed")
	request := target.Request{Hosts: []string{"host"}}
	outcome := monitor.RunOutcome{Latest: []monitor.LatestRow{{Host: "host", Row: monitor.Row{Result: "OK"}}}}
	if err := WriteRun(reportFailWriter{wantErr: wantErr}, request, outcome, nil, ReportOptions{Format: "table"}); !errors.Is(err, wantErr) {
		t.Fatalf("writer failure = %v, want %v", err, wantErr)
	}
}

func TestWriteHistoryUsesThreeSectionsAndPreservesOpenPeriod(t *testing.T) {
	request, outcome := formatFixture()
	outcome.Seen[0].Ongoing = true
	read := monitor.ReadOutcome{Request: request, Run: outcome, Found: true, Running: true}
	var got bytes.Buffer
	if err := WriteHistory(&got, read, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	text := got.String()
	if strings.Contains(text, "not_found") || !strings.Contains(text, `"online"`) || !strings.Contains(text, `"to": null`) {
		t.Fatalf("historical JSON sections/open period incorrect: %s", text)
	}
}

func TestWriteRequestsUsesOneRequestsSectionInStructuredFormats(t *testing.T) {
	last := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rows := []monitor.RequestRow{{ID: "abcd1234", Targets: "a,b", TCP: "80,443", Runs: 2, Rounds: 5, LastRound: &last, State: "running", Detail: "round 2"}}
	var got bytes.Buffer
	if err := WriteRequests(&got, rows, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(got.String(), `"requests"`) != 1 || strings.Contains(got.String(), "seen") || !strings.Contains(got.String(), `"rounds": 5`) {
		t.Fatalf("request JSON = %s", got.String())
	}
	if !strings.Contains(got.String(), `"last_round": "2026-03-04T05:06:07+00:00"`) {
		t.Fatalf("request time is not canonical UTC seconds: %s", got.String())
	}
}

func TestWriteTableKeepsAllRowsAndSanitizesOnlyHumanTerminalText(t *testing.T) {
	hosts := []string{"first\x1b[31m", "雪二", "third", "fourth"}
	request := target.Request{Hosts: hosts}
	out := monitor.RunOutcome{}
	for _, host := range hosts {
		out.Latest = append(out.Latest, monitor.LatestRow{Host: host, Row: monitor.Row{Result: "NO RESPONSE"}})
	}
	var redirected, terminal bytes.Buffer
	if err := WriteRun(&redirected, request, out, nil, ReportOptions{Format: "table", Width: 24, Terminal: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redirected.String(), "\x1b") {
		t.Fatalf("redirected table contains terminal control: %q", redirected.String())
	}
	for _, host := range []string{"first�", "雪二", "third", "fourth"} {
		if !strings.Contains(redirected.String(), host) {
			t.Errorf("row %q missing from narrow table: %q", host, redirected.String())
		}
	}
	if err := WriteRun(&terminal, request, out, nil, ReportOptions{Format: "table", Terminal: true, NoColor: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.String(), "\x1b[1m") || strings.Contains(terminal.String(), "38;2") {
		t.Fatalf("terminal no-color styling = %q", terminal.String())
	}
}

func TestWriteTableUsesSameDefaultWidthForTitlesAndRows(t *testing.T) {
	request := target.Request{Hosts: []string{"host.example"}}
	outcome := monitor.RunOutcome{Latest: []monitor.LatestRow{{Host: "host.example", Row: monitor.Row{Result: "OK"}}}}
	var got bytes.Buffer
	if err := WriteRun(&got, request, outcome, nil, ReportOptions{Format: "table"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "LATEST\n") || strings.Contains(got.String(), "L\nA\nT\nE\nS\nT") {
		t.Fatalf("zero-width report did not use a consistent default width: %q", got.String())
	}
}

func TestWideTableSharesUnusedColumnsWithLongTimestamps(t *testing.T) {
	first := time.Date(2026, 10, 11, 4, 51, 11, 0, time.UTC)
	last := first.Add(time.Second)
	request := target.Request{Hosts: []string{"alpha.example"}}
	outcome := monitor.RunOutcome{
		Stats: monitor.Stats{Rounds: 1, Seen: 1, FirstRound: first, LastRound: last},
		Seen: []monitor.HostSummary{{
			Host: "alpha.example", Address: "192.0.2.1", Latest: "OK",
			FirstSeen: first, LastSeen: last, SeenRounds: 1, Rounds: 1,
			Periods: []monitor.Period{{From: first, To: last}},
		}},
	}
	for name, write := range map[string]func(*bytes.Buffer) error{
		"run": func(output *bytes.Buffer) error {
			return WriteRun(output, request, outcome, nil, ReportOptions{Format: "table", Width: 180})
		},
		"history": func(output *bytes.Buffer) error {
			return WriteHistory(output, monitor.ReadOutcome{Request: request, Run: outcome, Found: true}, ReportOptions{Format: "table", Width: 180})
		},
	} {
		t.Run(name, func(t *testing.T) {
			var got bytes.Buffer
			if err := write(&got); err != nil {
				t.Fatal(err)
			}
			firstText, lastText := formatTimestamp(first), formatTimestamp(last)
			foundRow := false
			for _, line := range strings.Split(got.String(), "\n") {
				if displayWidth(line) > 180 {
					t.Errorf("table line width %d exceeds 180: %q", displayWidth(line), line)
				}
				if strings.Contains(line, "alpha.example") && strings.Contains(line, firstText) && strings.Contains(line, lastText) {
					foundRow = true
				}
			}
			if !foundRow {
				t.Fatalf("first/last timestamps wrapped away from their host row: %q", got.String())
			}
		})
	}
}

func TestTinyTableWrapsUnicodeWithinWidth(t *testing.T) {
	request := target.Request{Hosts: []string{"雪雪"}}
	first := time.Date(2026, 10, 11, 4, 51, 11, 0, time.UTC)
	outcome := monitor.RunOutcome{Seen: []monitor.HostSummary{{
		Host: request.Hosts[0], Address: "2001:db8::1", Latest: "NO RESPONSE",
		FirstSeen: first, LastSeen: first, SeenRounds: 1, Rounds: 1,
	}}}
	var got bytes.Buffer
	if err := WriteRun(&got, request, outcome, nil, ReportOptions{Format: "table", Width: 12}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(got.String(), "\n") {
		if displayWidth(line) > 12 {
			t.Errorf("tiny table line width %d exceeds 12: %q", displayWidth(line), line)
		}
	}
	if !strings.Contains(got.String(), "雪雪") {
		t.Fatalf("tiny table lost Unicode host: %q", got.String())
	}
}

func TestTableWaterFillBoundsUnicodeAndDistributesRemainderStably(t *testing.T) {
	got := layoutTable(
		[]string{"A", "B", "C"},
		[][]string{{"雪雪雪雪", "abcdef", "xy"}},
		17,
	)
	if len(got) < 3 {
		t.Fatalf("table has %d lines, want header and wrapped Unicode row: %#v", len(got), got)
	}
	firstDataLine := strings.Builder{}
	for _, part := range got[1] {
		firstDataLine.WriteString(part.text)
	}
	if want := "雪雪雪  abcde  xy"; firstDataLine.String() != want {
		t.Fatalf("first data line = %q, want stable capped allocation %q", firstDataLine.String(), want)
	}
	for i, line := range got {
		text := strings.Builder{}
		for _, part := range line {
			text.WriteString(part.text)
		}
		if width := displayWidth(text.String()); width > 17 {
			t.Errorf("line %d width %d exceeds 17: %q", i, width, text.String())
		}
	}
}

func TestTableOnlineColumnLimitsOnlyItsDisplayedPeriods(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	periods := make([]monitor.Period, 5)
	for i := range periods {
		periods[i] = monitor.Period{From: base.Add(time.Duration(i*2) * time.Hour), To: base.Add(time.Duration(i*2+1) * time.Hour)}
	}
	if got := formatPeriods(periods, false, false, true); !strings.Contains(got, "(+2 earlier)") || strings.Contains(got, "00:00–01:00") {
		t.Fatalf("table online periods = %q", got)
	}
	if got := formatPeriods(periods, false, false, false); strings.Contains(got, "earlier") || strings.Count(got, "–") != 5 {
		t.Fatalf("structured online periods = %q", got)
	}
}

func formatFixture() (target.Request, monitor.RunOutcome) {
	first := time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC)
	last := first.Add(2 * time.Minute)
	interval := time.Minute
	request := target.Request{Key: "12345678" + strings.Repeat("0", 56), Hosts: []string{"alpha,\"\n\r\x01\x1braw雪"}, Ports: []int{443, 80}}
	outcome := monitor.RunOutcome{
		Stats:         monitor.Stats{Rounds: 2, Seen: 1, Answering: 1, FirstRound: first, LastRound: last},
		Seen:          []monitor.HostSummary{{Host: request.Hosts[0], Address: "2001:db8::1", Latest: "OK\npartial", TCP: map[int]string{80: "open", 443: "closed"}, FirstSeen: first, LastSeen: last, SeenRounds: 2, Rounds: 2, Periods: []monitor.Period{{From: first, To: last}}}},
		Latest:        []monitor.LatestRow{{Host: request.Hosts[0], Row: monitor.Row{Address: "2001:db8::1", Result: "OK\npartial", TCP: map[int]string{80: "open", 443: "closed"}}}},
		CurrentRounds: 2, Interval: &interval,
	}
	return request, outcome
}

type reportFailWriter struct{ wantErr error }

func (w reportFailWriter) Write([]byte) (int, error) { return 0, w.wantErr }
