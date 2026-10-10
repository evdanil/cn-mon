package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
)

type observationCase struct {
	Name             string            `json:"name"`
	Result           string            `json:"result"`
	TCP              map[string]string `json:"tcp"`
	Want             string            `json:"want"`
	ExecutionFailure bool              `json:"execution_failure"`
	Partial          bool              `json:"partial"`
}

func TestObservationCasesFromPinnedSource(t *testing.T) {
	data, err := os.ReadFile("testdata/provenance/observation-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []observationCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	ports := []int{22, 443}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			tcp := make(map[int]string, len(tc.TCP))
			for port, state := range tc.TCP {
				var number int
				if _, err := fmt.Sscan(port, &number); err != nil {
					t.Fatal(err)
				}
				tcp[number] = state
			}
			row := Row{Result: tc.Result, TCP: tcp}
			if got := Observation(row, ports); got != tc.Want[0] {
				t.Errorf("Observation() = %q, want %q", got, tc.Want[0])
			}
			if got := ExecutionFailure(row, ports); got != tc.ExecutionFailure {
				t.Errorf("ExecutionFailure() = %t, want %t", got, tc.ExecutionFailure)
			}
			if got := PartialReply(row); got != tc.Partial {
				t.Errorf("PartialReply() = %t, want %t", got, tc.Partial)
			}
		})
	}
}

func TestTrackerStickyStateAndLatestOfflineResult(t *testing.T) {
	tracker := NewTracker([]string{"seen", "error", "unreachable", "unresolved"}, []int{443}, 0)
	start := at(100)
	first := Round{Started: start, Observations: "!.U?", Rows: map[string]Row{
		"seen":        {Address: "192.0.2.10", Result: "OK", TCP: map[int]string{443: "open"}},
		"unreachable": {Result: "UNREACHABLE"},
		"unresolved":  {Result: "no such host"},
	}}
	changes, err := tracker.Add(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("first round changes = %v, want none", changes)
	}
	if got, want := string(tracker.Cells(nil)), "!.U?"; got != want {
		t.Fatalf("first cells = %q, want %q", got, want)
	}
	if stats := tracker.Stats(); stats.Seen != 1 || stats.Answering != 1 || stats.Rounds != 1 {
		t.Fatalf("first-round stats = %#v", stats)
	}

	second := Round{Started: start.Add(time.Second), Observations: ".E.U"}
	if _, err := tracker.Add(second); err != nil {
		t.Fatal(err)
	}
	if got, want := string(tracker.Cells(nil)), "xEUU"; got != want {
		t.Fatalf("second cells = %q, want %q", got, want)
	}
	if !tracker.Stats().HadError {
		t.Fatal("HadError = false, want true for an E observation")
	}
	rows := tracker.Summaries(false)
	if len(rows) != 1 {
		t.Fatalf("summaries = %d rows, want seen host only", len(rows))
	}
	got := rows[0]
	if got.Latest != "NO RESPONSE" || len(got.TCP) != 0 {
		t.Fatalf("latest offline summary = (%q, %v), want current result and empty TCP", got.Latest, got.TCP)
	}
	if got.Address != "192.0.2.10" {
		t.Fatalf("last responding address = %q, want 192.0.2.10", got.Address)
	}
	if got.SeenRounds != 1 || got.Rounds != 2 || got.Ongoing || len(got.Periods) != 1 || !got.Periods[0].To.Equal(start) {
		t.Fatalf("offline aggregate = %#v", got)
	}
}

func TestTrackerSampledPeriodGapThreshold(t *testing.T) {
	tracker := NewTracker([]string{"host"}, nil, 0)
	for _, second := range []int{0, 25, 50, 76} {
		if _, err := tracker.Add(Round{Started: at(int64(second)), Interval: 10 * time.Second, Observations: "!"}); err != nil {
			t.Fatal(err)
		}
	}
	got := tracker.Summaries(false)[0].Periods
	if len(got) != 2 || !got[0].From.Equal(at(0)) || !got[0].To.Equal(at(50)) || !got[1].From.Equal(at(76)) || !got[1].To.Equal(at(76)) {
		t.Fatalf("periods = %#v, want [0,50] and [76,76]; equality at 25 seconds must stay joined", got)
	}
}

func TestTrackerWithoutExpectedSpacingSeparatesSamples(t *testing.T) {
	tracker := NewTracker([]string{"host"}, nil, 0)
	for _, second := range []int64{0, 10} {
		if _, err := tracker.Add(Round{Started: at(second), Observations: "!"}); err != nil {
			t.Fatal(err)
		}
	}
	got := tracker.Summaries(false)[0].Periods
	if len(got) != 2 {
		t.Fatalf("periods = %#v, want one period per sample without expected spacing", got)
	}
}

func TestExpectedSpacingControlsSingleRoundPeriods(t *testing.T) {
	starts := []time.Time{at(0), at(10), at(30), at(81)}
	spacing := ExpectedSpacing(starts)
	if spacing != 20*time.Second {
		t.Fatalf("inferred spacing = %v, want lower median 20s", spacing)
	}
	tracker := NewTracker([]string{"host"}, nil, spacing)
	for _, started := range starts {
		if _, err := tracker.Add(Round{Started: started, Observations: "!"}); err != nil {
			t.Fatal(err)
		}
	}
	periods := tracker.Summaries(false)[0].Periods
	if len(periods) != 2 || !periods[0].To.Equal(at(30)) || !periods[1].From.Equal(at(81)) {
		t.Fatalf("single-round sampled periods = %#v, want 0..30 and 81..81 at inferred 20s spacing", periods)
	}
}

func TestStickyUnreachableClearsWhenHostIsSeen(t *testing.T) {
	tracker := NewTracker([]string{"host"}, nil, 10*time.Second)
	for _, observation := range []string{"U", "E", "!"} {
		if _, err := tracker.Add(Round{Started: at(int64(tracker.Stats().Rounds)), Interval: 10 * time.Second, Observations: observation}); err != nil {
			t.Fatal(err)
		}
		want := byte('U')
		if observation == "!" {
			want = '!'
		}
		if got := tracker.Cells(nil)[0]; got != want {
			t.Fatalf("cell after %s = %q, want %q", observation, got, want)
		}
	}
}

func TestTrackerPartialHighlightAndSeenTransition(t *testing.T) {
	tracker := NewTracker([]string{"host"}, nil, 0)
	if _, err := tracker.Add(Round{Started: at(0), Observations: "."}); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Add(Round{Started: at(1), Observations: "!", Rows: map[string]Row{
		"host": {Result: "OK (1/2 replies)"},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Changed(nil); len(got) != 1 || got[0] != 0 {
		t.Fatalf("changed hosts = %v, want [0]", got)
	}
	if got := tracker.Partial(nil, nil); len(got) != 1 || got[0] != 0 {
		t.Fatalf("partial hosts = %v, want [0]", got)
	}
	if _, err := tracker.Add(Round{Started: at(2), Observations: "."}); err != nil {
		t.Fatal(err)
	}
	if got := string(tracker.Cells(nil)); got != "x" {
		t.Fatalf("cell after seen host goes silent = %q, want x", got)
	}
	if got := tracker.Changed(nil); len(got) != 1 || got[0] != 0 {
		t.Fatalf("changed hosts after silence = %v, want [0]", got)
	}
}

func TestExpectedSpacingUsesLowerMedianAfterThreeSamples(t *testing.T) {
	if got := ExpectedSpacing([]time.Time{at(0), at(10)}); got != 0 {
		t.Fatalf("two starts imply spacing %v, want unavailable", got)
	}
	if got, want := ExpectedSpacing([]time.Time{at(0), at(10), at(30), at(40)}), 10*time.Second; got != want {
		t.Fatalf("lower median spacing = %v, want %v", got, want)
	}
}

func TestTrackerConcurrentSampleTimesAreClamped(t *testing.T) {
	tracker := NewTracker([]string{"host"}, nil, 10*time.Second)
	for _, round := range []Round{
		{Started: at(0), Interval: 10 * time.Second, Observations: "!", Times: map[string]time.Time{"host": at(20)}},
		{Started: at(10), Interval: 10 * time.Second, Observations: "!", Times: map[string]time.Time{"host": at(15)}},
	} {
		if _, err := tracker.Add(round); err != nil {
			t.Fatal(err)
		}
	}
	summary := tracker.Summaries(false)[0]
	if !summary.FirstSeen.Equal(at(20)) || !summary.LastSeen.Equal(at(20)) || !summary.Periods[0].To.Equal(at(20)) {
		t.Fatalf("clamped summary = %#v", summary)
	}
}

func TestTrackerOpenPeriodRequiresLatestRoundOwnerLive(t *testing.T) {
	tracker := NewTracker([]string{"host"}, nil, 0)
	if _, err := tracker.Add(Round{Started: at(0), Observations: "!"}); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Summaries(false)[0].Ongoing; got {
		t.Fatal("a completed latest round must have a closed period")
	}
	if got := tracker.Summaries(true)[0].Ongoing; !got {
		t.Fatal("a live owner of the latest round must leave the period open")
	}
}

func TestPendingOverlayDoesNotCommitOrChangeAggregates(t *testing.T) {
	tracker := NewTracker([]string{"seen", "unreachable", "quiet"}, nil, 10*time.Second)
	if _, err := tracker.Add(Round{Started: at(0), Interval: 10 * time.Second, Observations: "!U.", Rows: map[string]Row{
		"seen": {Address: "192.0.2.1", Result: "OK (1/2 replies)"},
	}}); err != nil {
		t.Fatal(err)
	}
	beforeStats := tracker.Stats()
	beforeCells := append([]byte(nil), tracker.Cells(nil)...)
	beforeSummaries := tracker.Summaries(false)

	pending := map[int]byte{0: NoResponse, 1: TCPSeen, 2: NoName}
	if got, want := string(tracker.Cells(pending)), "xT?"; got != want {
		t.Fatalf("pending cells = %q, want %q", got, want)
	}
	if got := tracker.Changed(pending); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("pending changed hosts = %v, want [0 1]", got)
	}
	if got := tracker.Partial(pending, map[int]bool{2: true}); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("pending partial hosts = %v, want [2]", got)
	}
	if got := tracker.Counts(pending); got[Gone] != 1 || got[TCPSeen] != 1 || got[NoName] != 1 {
		t.Fatalf("pending cell counts = %v", got)
	}
	if got := tracker.Stats(); got != beforeStats {
		t.Fatalf("pending overlay changed stats: before %#v, after %#v", beforeStats, got)
	}
	if got := tracker.Summaries(false); !reflect.DeepEqual(got, beforeSummaries) {
		t.Fatalf("pending overlay changed summaries: before %#v, after %#v", beforeSummaries, got)
	}
	if got := tracker.Cells(nil); !reflect.DeepEqual(got, beforeCells) {
		t.Fatalf("discarding pending overlay changed committed cells: before %q, after %q", beforeCells, got)
	}
	if got := tracker.Summaries(false)[0].Periods; !reflect.DeepEqual(got, beforeSummaries[0].Periods) {
		t.Fatalf("discarding pending overlay changed periods: before %#v, after %#v", beforeSummaries[0].Periods, got)
	}

	if _, err := tracker.Add(Round{Started: at(1), Interval: 10 * time.Second, Observations: ".T."}); err != nil {
		t.Fatal(err)
	}
	if got := string(tracker.Cells(nil)); got != "xT." {
		t.Fatalf("completed round cells = %q, want committed observations to replace overlay", got)
	}
}

func TestInvalidRoundIsAtomic(t *testing.T) {
	tracker := NewTracker([]string{"a", "b"}, nil, 0)
	if _, err := tracker.Add(Round{Started: at(1), Observations: "!U", Rows: map[string]Row{
		"a": {Address: "192.0.2.1", Result: "OK"},
	}}); err != nil {
		t.Fatal(err)
	}
	beforeStats := tracker.Stats()
	beforeCells := append([]byte(nil), tracker.Cells(nil)...)
	beforeSummaries := tracker.Summaries(false)
	for _, invalid := range []Round{
		{Started: at(2), Observations: "!"},
		{Started: at(2), Observations: "!x"},
	} {
		if _, err := tracker.Add(invalid); err == nil {
			t.Fatalf("Add(%q) succeeded, want validation error", invalid.Observations)
		}
		if got := tracker.Stats(); got != beforeStats {
			t.Fatalf("invalid round %q changed stats: before %#v, after %#v", invalid.Observations, beforeStats, got)
		}
		if got := tracker.Cells(nil); !reflect.DeepEqual(got, beforeCells) {
			t.Fatalf("invalid round %q changed cells: before %q, after %q", invalid.Observations, beforeCells, got)
		}
		if got := tracker.Summaries(false); !reflect.DeepEqual(got, beforeSummaries) {
			t.Fatalf("invalid round %q changed summaries", invalid.Observations)
		}
	}
}

func TestTrackerCopiesInputRowsAndReturnedSummaries(t *testing.T) {
	inputRow := Row{Address: "192.0.2.1", Result: "OK", TCP: map[int]string{443: "open"}}
	tracker := NewTracker([]string{"host"}, []int{443}, 0)
	if _, err := tracker.Add(Round{Started: at(1), Observations: "!", Rows: map[string]Row{"host": inputRow}}); err != nil {
		t.Fatal(err)
	}
	inputRow.Address = "203.0.113.99"
	inputRow.Result = "mutated"
	inputRow.TCP[443] = "closed"
	first := tracker.Summaries(false)[0]
	if first.Address != "192.0.2.1" || first.Latest != "OK" || first.TCP[443] != "open" {
		t.Fatalf("input mutation changed tracker row: %#v", first)
	}
	first.TCP[443] = "error"
	first.Periods[0].From = at(100)
	first.Periods[0].To = at(101)
	second := tracker.Summaries(false)[0]
	if second.TCP[443] != "open" || !second.Periods[0].From.Equal(at(1)) || !second.Periods[0].To.Equal(at(1)) {
		t.Fatalf("returned summary mutation changed tracker state: %#v", second)
	}
}

func TestTrackerManyRoundsKeepAggregatesAndOnePeriod(t *testing.T) {
	const rounds = 100_000
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	oneRound := NewTracker([]string{"host"}, nil, time.Second)
	if _, err := oneRound.Add(Round{Started: at(0), Interval: time.Second, Observations: "!"}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var afterOne runtime.MemStats
	runtime.ReadMemStats(&afterOne)
	runtime.KeepAlive(oneRound)

	tracker := NewTracker([]string{"host"}, nil, time.Second)
	for i := 0; i < rounds; i++ {
		if _, err := tracker.Add(Round{Started: at(int64(i)), Interval: time.Second, Observations: "!"}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var afterMany runtime.MemStats
	runtime.ReadMemStats(&afterMany)
	runtime.KeepAlive(oneRound)
	runtime.KeepAlive(tracker)

	stats := tracker.Stats()
	if stats.Rounds != rounds || stats.Seen != 1 || stats.Answering != 1 {
		t.Fatalf("many-round stats = %#v", stats)
	}
	summary := tracker.Summaries(true)[0]
	if summary.Rounds != rounds || summary.SeenRounds != rounds || len(summary.Periods) != 1 ||
		!summary.Periods[0].From.Equal(at(0)) || !summary.Periods[0].To.Equal(at(rounds-1)) {
		t.Fatalf("many-round summary = %#v", summary)
	}
	t.Logf("retained heap from baseline to one-round tracker: %d bytes; additional heap for %d-round tracker: %d bytes",
		int64(afterOne.HeapAlloc)-int64(before.HeapAlloc), rounds,
		int64(afterMany.HeapAlloc)-int64(afterOne.HeapAlloc))
}

func at(seconds int64) time.Time { return time.Unix(seconds, 0).UTC() }
