// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/target"
)

// The unseen section adapts unseen_rows of cn_tool/utils/ping_coverage.py from
// cn-tool merge a739948c69ada9bab27d25058c1d695ca06d5aba (PR #178). The
// fixture is its 2,880-round test_unseen_rows_formats data (host 2 never
// answers in 2,880 rounds, host 3 is silent in half of them and fails in the
// rest) plus a silent-subnet host and a Not tested host from
// test_unseen_rows_hold_unseen_and_not_tested_in_host_order_with_the_silent_note.
// The testdata/unseen-* goldens are hand-derived from that fixture and the
// section 8 field list: they carry the cn-tool strings ("0 of 2,880 rounds",
// "no IP in this subnet ever replied") and the existing section/CSV/Markdown
// writer rules.

var unseenT0 = time.Unix(1_791_500_000, 0).UTC() // 2026-10-08T22:53:20+00:00

func unseenCoverage(t *testing.T) monitor.Coverage {
	t.Helper()
	hosts := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.1.0.1", "10.2.0.1"}
	subnets := []monitor.Subnet{{Token: "10.0.0.0/30", Members: []int{0, 1, 2}}, {Token: "10.1.0.0/30", Members: []int{3}}}
	fold := monitor.NewCoverageFold(hosts, subnets)
	for step := 0; step < 2880; step++ {
		third := "."
		if step%2 == 1 {
			third = "E"
		}
		if err := fold.Add(unseenT0.Add(time.Duration(step)*time.Minute), "!."+third+".E"); err != nil {
			t.Fatal(err)
		}
	}
	return fold.Coverage()
}

func unseenGolden(t *testing.T, name string) []byte {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return want
}

func TestWriteRunWithCoverageMatchesUnseenGoldens(t *testing.T) {
	coverage := unseenCoverage(t)
	for _, tc := range []struct{ format, golden string }{
		{"json", "unseen-run.json"}, {"csv", "unseen-run.csv"}, {"md", "unseen-run.md"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			var got bytes.Buffer
			if err := WriteRun(&got, target.Request{}, monitor.RunOutcome{Coverage: &coverage}, nil, ReportOptions{Format: tc.format}); err != nil {
				t.Fatal(err)
			}
			if want := unseenGolden(t, tc.golden); !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("%s mismatch\n got: %q\nwant: %q", tc.format, got.Bytes(), want)
			}
		})
	}
}

func TestWriteHistoryWithCoverageMatchesUnseenGoldens(t *testing.T) {
	coverage := unseenCoverage(t)
	read := monitor.ReadOutcome{Found: true, Run: monitor.RunOutcome{Coverage: &coverage}}
	var got bytes.Buffer
	if err := WriteHistory(&got, read, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if want := unseenGolden(t, "unseen-show.json"); !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("show JSON mismatch\n got: %q\nwant: %q", got.Bytes(), want)
	}
	for _, tc := range []struct{ format, golden string }{{"csv", "unseen-run.csv"}, {"md", "unseen-run.md"}} {
		got.Reset()
		if err := WriteHistory(&got, read, ReportOptions{Format: tc.format}); err != nil {
			t.Fatal(err)
		}
		if want := unseenGolden(t, tc.golden); !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("show %s mismatch\n got: %q\nwant: %q", tc.format, got.Bytes(), want)
		}
	}
}

func TestWriteTableEndsWithTheUnseenSectionAndKeepsEveryRow(t *testing.T) {
	coverage := unseenCoverage(t)
	request, outcome := formatFixture()
	outcome.Coverage = &coverage
	var got bytes.Buffer
	if err := WriteRun(&got, request, outcome, nil, ReportOptions{Format: "table", Width: 200}); err != nil {
		t.Fatal(err)
	}
	text := got.String()
	title := strings.LastIndex(text, "UNSEEN\n")
	if title < 0 || strings.Contains(text[title:], "SUMMARY") || strings.Contains(text[:title], "UNSEEN") {
		t.Fatalf("UNSEEN is not the single last section: %q", text)
	}
	section := text[title:]
	for _, want := range []string{
		"Host", "Status", "Seen", "Failed rounds", "Since", "Note",
		"10.0.0.2", "0 of 2,880 rounds", "10.0.0.3", "0 of 1,440 rounds", "1440",
		"10.1.0.1", "no IP in this subnet ever replied", "10.2.0.1", "Not tested", "0 of 0 rounds", "2026-10-08T22:53:20+00:00",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("UNSEEN section lacks %q: %q", want, section)
		}
	}
	if strings.Contains(section, "10.0.0.1") {
		t.Errorf("a Seen host is listed in UNSEEN: %q", section)
	}
}

func TestCoverageSectionIsLastAndCSVHeaderIsTheFirstSeenUnion(t *testing.T) {
	coverage := unseenCoverage(t)
	request, outcome := formatFixture()
	outcome.Coverage = &coverage

	var csv bytes.Buffer
	if err := WriteRun(&csv, request, outcome, nil, ReportOptions{Format: "csv"}); err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(csv.String(), "\n")
	const want = "section,Host,Address,Latest,TCP 80,TCP 443,First seen,Last seen,Seen,Online,Result,ID,Started,Ended,Rounds,Earlier rounds,Interval,Hosts,Answering,Status,Failed rounds,Since,Note"
	if header != want {
		t.Fatalf("CSV header = %q, want %q", header, want)
	}
	lines := strings.Split(strings.TrimSuffix(csv.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "unseen,10.2.0.1,") {
		t.Fatalf("last CSV record = %q, want the last unseen row", last)
	}

	var md bytes.Buffer
	if err := WriteRun(&md, request, outcome, nil, ReportOptions{Format: "md"}); err != nil {
		t.Fatal(err)
	}
	if sections := strings.Count(md.String(), "\n## "); !strings.HasPrefix(md.String(), "## SEEN") || sections != 3 || !strings.Contains(md.String(), "\n## UNSEEN\n\n| Host | Status |") {
		t.Fatalf("Markdown sections = %q", md.String())
	}

	var js bytes.Buffer
	if err := WriteRun(&js, request, outcome, nil, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	keys := []string{`"seen":`, `"latest":`, `"summary":`, `"not_found":`, `"unseen":`}
	position := -1
	for _, key := range keys {
		next := strings.Index(js.String(), "\n  "+key)
		if next <= position {
			t.Fatalf("JSON key %s out of order in %s", key, js.String())
		}
		position = next
	}
}

func TestEmptyCoverageIsAnEmptyJSONArrayAndNothingElse(t *testing.T) {
	empty := &monitor.Coverage{}
	var js bytes.Buffer
	if err := WriteRun(&js, target.Request{}, monitor.RunOutcome{Coverage: empty}, nil, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"seen\": [],\n  \"latest\": [],\n  \"summary\": [],\n  \"not_found\": [],\n  \"unseen\": []\n}\n"
	if js.String() != want {
		t.Fatalf("run JSON = %q, want %q", js.String(), want)
	}
	js.Reset()
	if err := WriteHistory(&js, monitor.ReadOutcome{Found: true, Run: monitor.RunOutcome{Coverage: empty}}, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"seen\": [],\n  \"latest\": [],\n  \"summary\": [],\n  \"unseen\": []\n}\n"; js.String() != want {
		t.Fatalf("show JSON = %q, want %q", js.String(), want)
	}
	for _, format := range []string{"table", "md", "csv"} {
		var got bytes.Buffer
		if err := WriteRun(&got, target.Request{}, monitor.RunOutcome{Coverage: empty}, nil, ReportOptions{Format: format}); err != nil {
			t.Fatal(err)
		}
		if got.Len() != 0 {
			t.Errorf("%s with an empty section = %q, want nothing", format, got.String())
		}
	}
}

func TestEverythingRepliedLeavesTheSectionEmpty(t *testing.T) {
	fold := monitor.NewCoverageFold([]string{"10.0.0.1"}, nil)
	if err := fold.Add(unseenT0, "!"); err != nil {
		t.Fatal(err)
	}
	coverage := fold.Coverage()
	var js, md bytes.Buffer
	if err := WriteRun(&js, target.Request{}, monitor.RunOutcome{Coverage: &coverage}, nil, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), "\"unseen\": []") {
		t.Fatalf("JSON = %q, want an empty unseen array", js.String())
	}
	if err := WriteRun(&md, target.Request{}, monitor.RunOutcome{Coverage: &coverage}, nil, ReportOptions{Format: "md"}); err != nil {
		t.Fatal(err)
	}
	if md.Len() != 0 {
		t.Fatalf("Markdown = %q, want nothing", md.String())
	}
}

func TestRoundlessCoverageHasAnEmptySinceAndNotTestedRows(t *testing.T) {
	coverage := monitor.NewCoverageFold([]string{"10.0.0.1", "10.0.0.2"}, nil).Coverage()
	var csv, js bytes.Buffer
	if err := WriteRun(&csv, target.Request{}, monitor.RunOutcome{Coverage: &coverage}, nil, ReportOptions{Format: "csv"}); err != nil {
		t.Fatal(err)
	}
	want := "section,Host,Status,Seen,Failed rounds,Since,Note\n" +
		"unseen,10.0.0.1,Not tested,0 of 0 rounds,0,,\n" +
		"unseen,10.0.0.2,Not tested,0 of 0 rounds,0,,\n"
	if csv.String() != want {
		t.Fatalf("CSV = %q, want %q", csv.String(), want)
	}
	if err := WriteRun(&js, target.Request{}, monitor.RunOutcome{Coverage: &coverage}, nil, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), "\"since\": \"\"") || strings.Contains(js.String(), "null") {
		t.Fatalf("JSON = %q, want empty since strings and no nulls", js.String())
	}
}

func TestThousandsSeparatorsApplyToTheUnseenSeenCellOnly(t *testing.T) {
	request, outcome := formatFixture()
	outcome.Stats.Rounds = 1234
	outcome.Seen[0].SeenRounds, outcome.Seen[0].Rounds = 1234, 2345
	var got bytes.Buffer
	if err := WriteRun(&got, request, outcome, nil, ReportOptions{Format: "csv"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "1234 of 2345 rounds") || strings.Contains(got.String(), "1,234") {
		t.Fatalf("the seen section lost its unseparated form: %q", got.String())
	}
	for count, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 2880: "2,880", 65536: "65,536", 1234567: "1,234,567"} {
		if got := groupThousands(count); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", count, got, want)
		}
	}
}

func TestWithoutCoverageNoFormatMentionsTheSection(t *testing.T) {
	request, outcome := formatFixture()
	for _, format := range []string{"json", "csv", "md", "table"} {
		var run, show bytes.Buffer
		if err := WriteRun(&run, request, outcome, nil, ReportOptions{Format: format}); err != nil {
			t.Fatal(err)
		}
		if err := WriteHistory(&show, monitor.ReadOutcome{Request: request, Run: outcome, Found: true}, ReportOptions{Format: format}); err != nil {
			t.Fatal(err)
		}
		for name, text := range map[string]string{"run": run.String(), "show": show.String()} {
			if strings.Contains(strings.ToLower(text), "unseen") {
				t.Errorf("%s %s output mentions the unseen section without --available: %q", name, format, text)
			}
		}
	}
}

func TestWritersDoNotMutateCoverage(t *testing.T) {
	coverage := unseenCoverage(t)
	before := unseenCoverage(t)
	for _, format := range []string{"json", "csv", "md", "table"} {
		var sink bytes.Buffer
		if err := WriteRun(&sink, target.Request{}, monitor.RunOutcome{Coverage: &coverage}, nil, ReportOptions{Format: format}); err != nil {
			t.Fatal(err)
		}
		if err := WriteHistory(&sink, monitor.ReadOutcome{Found: true, Run: monitor.RunOutcome{Coverage: &coverage}}, ReportOptions{Format: format}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(coverage, before) {
		t.Fatalf("a writer changed the Coverage it was given")
	}
	if len(coverage.Hosts) != 5 || len(coverage.Unseen()) != 4 {
		t.Fatalf("coverage hosts = %d, unseen = %d", len(coverage.Hosts), len(coverage.Unseen()))
	}
}
