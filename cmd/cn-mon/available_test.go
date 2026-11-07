// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/output"
	"github.com/evdanil/cn-mon/internal/probe"
	"github.com/evdanil/cn-mon/internal/target"
)

// These command tests map the --available cases of tests/test_modules_ping_monitor.py
// in cn-tool merge a739948c69ada9bab27d25058c1d695ca06d5aba (PR #178) to cn-mon:
// test_available_run_counts_this_run_and_earlier_rounds,
// test_available_run_counts_a_round_of_its_own_alone,
// test_available_says_when_nothing_is_never_seen,
// test_available_for_a_request_the_history_does_not_hold_says_nothing,
// test_available_run_history_that_cannot_be_opened_after_the_run_exits_3_with_the_run,
// test_available_run_whose_read_fails_after_the_run_exits_3,
// test_available_run_whose_history_never_opened_says_it_once,
// test_available_silent_subnet_warns_on_stderr,
// test_available_names_each_silent_subnet_of_a_run,
// test_available_refusals_before_a_run_carry_an_empty_unseen_section,
// test_available_show_lists_never_seen_hosts,
// test_available_show_refusals_carry_an_empty_unseen_section,
// test_available_history_failure_exits_3,
// test_available_show_without_a_history_is_the_refusal_it_was,
// test_available_does_not_change_the_exit_status and
// test_without_available_refusals_have_no_unseen_key. cn-mon says "cn-mon: "
// where cn-tool says "cn: monitor: ".

const (
	availableNothingNeverSeen = "no never-seen IPs: every host of this target line replied at least once"
	availableHistoryFailure   = "history operation failed"
)

// availableProber answers the hosts in reply and leaves every other host silent.
type availableProber struct{ reply map[string]bool }

func (p availableProber) Resolve(_ context.Context, hosts []string) ([]probe.Resolved, error) {
	resolved := make([]probe.Resolved, 0, len(hosts))
	for index, host := range hosts {
		address, err := netip.ParseAddr(host)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, probe.Resolved{Index: index, Host: host, Address: address})
	}
	return resolved, nil
}

func (p availableProber) ProbeBatch(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
	results := make([]probe.Result, 0, len(targets))
	for _, destination := range targets {
		icmp := probe.ICMPResult{State: "NO RESPONSE", Sent: 1}
		if p.reply[destination.Host] {
			icmp = probe.ICMPResult{State: "OK", Sent: 1, Received: 1}
		}
		results = append(results, probe.Result{Target: destination, ICMP: icmp, TCP: map[int]probe.TCPResult{}})
	}
	return results, nil
}

type availableOutcome struct {
	code           int
	stdout, stderr string
	database       string
	logPath        string
}

// availableCLI runs the command against a fresh history file under a temporary
// home. configure adjusts the injected runtime (probes, writer, read-only opener).
func availableCLI(t *testing.T, args []string, prober availableProber, configure func(root string, runtime *commandRuntime)) availableOutcome {
	t.Helper()
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	return availableCLIAt(t, database, logPath, args, prober, configure)
}

func availableCLIAt(t *testing.T, database, logPath string, args []string, prober availableProber, configure func(root string, runtime *commandRuntime)) availableOutcome {
	t.Helper()
	root := filepath.Dir(database)
	runtime := commandTestRuntime(root, logPath, monitor.RunDeps{
		Prober: prober,
		OpenHistory: func(ctx context.Context, path string) (monitor.History, error) {
			return history.OpenWriter(ctx, path)
		},
	})
	if configure != nil {
		configure(root, &runtime)
	}
	var stdout, stderr bytes.Buffer
	full := append(append([]string(nil), args...), "--history-file", database, "--log-file", logPath)
	code := runCLIWithRuntime(full, nil, &stdout, &stderr, runtime)
	return availableOutcome{code: code, stdout: stdout.String(), stderr: stderr.String(), database: database, logPath: logPath}
}

type availableRow struct {
	Host         string `json:"host"`
	Status       string `json:"status"`
	SeenRounds   int    `json:"seen_rounds"`
	Rounds       int    `json:"rounds"`
	FailedRounds int    `json:"failed_rounds"`
	Since        string `json:"since"`
	Note         string `json:"note"`
}

// availableUnseen decodes the unseen section of a JSON report; present is false
// when the report has no such key.
func availableUnseen(t *testing.T, stdout string) (rows []availableRow, present bool) {
	t.Helper()
	var report struct {
		Unseen *[]availableRow `json:"unseen"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not a JSON report: %v\n%s", err, stdout)
	}
	if report.Unseen == nil {
		return nil, false
	}
	return *report.Unseen, true
}

// availableCSVSection returns the rows of one report section keyed by CSV header.
func availableCSVSection(t *testing.T, text, section string) []map[string]string {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil || len(records) == 0 {
		t.Fatalf("stdout is not CSV: %v\n%s", err, text)
	}
	var rows []map[string]string
	for _, record := range records[1:] {
		if record[0] != section {
			continue
		}
		row := map[string]string{}
		for i, column := range records[0][1:] {
			row[column] = record[i+1]
		}
		rows = append(rows, row)
	}
	return rows
}

func availableMustBeEmptySection(t *testing.T, stdout string) {
	t.Helper()
	rows, present := availableUnseen(t, stdout)
	if !present || len(rows) != 0 {
		t.Fatalf("unseen = %+v (present %t), want the key present and empty:\n%s", rows, present, stdout)
	}
}

func availableRequest(t *testing.T, tokens ...string) target.Request {
	t.Helper()
	request, rejected, err := target.Build(tokens, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("target.Build(%q): %+v, %v, %v", tokens, request, rejected, err)
	}
	return request
}

// availableSeed records rounds of one earlier run of request, one hour apart
// ending an hour ago. Each observation string must fit request.Hosts.
func availableSeed(t *testing.T, database string, request target.Request, runID string, observations ...string) []time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := history.OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open fixture history: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close fixture history: %v", err)
		}
	}()
	first := time.Now().UTC().Truncate(time.Second).Add(-time.Duration(len(observations)+1) * time.Hour)
	if _, err := store.BeginRun(ctx, history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports}, Hosts: request.Hosts,
	}, history.RunStart{ID: runID, Started: first, Host: "available-test", PID: 1, Heartbeat: first}); err != nil {
		t.Fatalf("begin fixture run: %v", err)
	}
	starts := make([]time.Time, 0, len(observations))
	for step, round := range observations {
		started := first.Add(time.Duration(step) * time.Hour)
		input := history.RoundInput{Started: started, Observations: round}
		for i, state := range []byte(round) {
			if state == '!' || state == 'R' || state == 'T' {
				input.Rows = append(input.Rows, history.ResultRow{Host: request.Hosts[i], Address: request.Hosts[i], Result: "OK", Time: started, TCP: map[int]string{}})
			}
		}
		if _, err := store.RecordRound(ctx, runID, input); err != nil {
			t.Fatalf("record fixture round %q: %v", round, err)
		}
		starts = append(starts, started)
	}
	if err := store.EndRun(ctx, runID, first.Add(time.Duration(len(observations))*time.Hour), "finished"); err != nil {
		t.Fatalf("end fixture run: %v", err)
	}
	return starts
}

func TestAvailableRunCountsEarlierAndOwnRounds(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	request := availableRequest(t, "127.0.0.1")
	starts := availableSeed(t, database, request, "earlier", ".", ".")

	got := availableCLIAt(t, database, logPath, []string{"127.0.0.1", "--available", "--format", "json"}, availableProber{}, nil)
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1 (nothing seen, exit unchanged by --available); stderr=%q", got.code, got.stderr)
	}
	rows, present := availableUnseen(t, got.stdout)
	want := availableRow{Host: "127.0.0.1", Status: "Unseen", Rounds: 3, Since: starts[0].Format("2006-01-02T15:04:05") + "+00:00"}
	if !present || len(rows) != 1 || rows[0] != want {
		t.Fatalf("unseen = %+v (present %t), want [%+v]: two earlier rounds and this run's own", rows, present, want)
	}
	if strings.Contains(got.stderr, availableNothingNeverSeen) || strings.Contains(got.stderr, "ever replied") {
		t.Fatalf("stderr says something about a host that is listed: %q", got.stderr)
	}
}

func TestAvailableRunCountsARoundOfItsOwnAlone(t *testing.T) {
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, availableProber{}, nil)
	rows, present := availableUnseen(t, got.stdout)
	if got.code != 1 || !present || len(rows) != 1 || rows[0].Rounds != 1 || rows[0].Status != "Unseen" || rows[0].Since == "" {
		t.Fatalf("exit=%d unseen=%+v present=%t, want one Unseen row over this run's single round\nstderr=%q", got.code, rows, present, got.stderr)
	}
}

func TestAvailableRunSaysWhenNothingIsNeverSeenOnceAndOnlyWithTheOption(t *testing.T) {
	prober := availableProber{reply: map[string]bool{"127.0.0.1": true}}
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, prober, nil)
	availableMustBeEmptySection(t, got.stdout)
	if got.code != 0 || strings.Count(got.stderr, availableNothingNeverSeen) != 1 {
		t.Fatalf("exit=%d, want 0 and the no-never-seen line exactly once; stderr=%q", got.code, got.stderr)
	}
	if strings.Contains(got.stdout, availableNothingNeverSeen) {
		t.Fatalf("the note reached stdout: %q", got.stdout)
	}

	for _, format := range []string{"table", "md", "csv"} {
		got = availableCLI(t, []string{"127.0.0.1", "--available", "--format", format}, prober, nil)
		if strings.Contains(strings.ToLower(got.stdout), "unseen") || strings.Count(got.stderr, availableNothingNeverSeen) != 1 {
			t.Fatalf("%s: stdout=%q stderr=%q, want no unseen section and the line once", format, got.stdout, got.stderr)
		}
	}

	got = availableCLI(t, []string{"127.0.0.1", "--format", "json"}, prober, nil)
	if _, present := availableUnseen(t, got.stdout); present || strings.Contains(got.stderr, "never-seen") {
		t.Fatalf("without --available: unseen key present=%t stderr=%q", present, got.stderr)
	}
}

func TestAvailableRunWithNoHistoryAfterTheRunSaysNothingAndExitsAsBefore(t *testing.T) {
	prober := availableProber{reply: map[string]bool{"127.0.0.1": true}}
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, prober, func(root string, runtime *commandRuntime) {
		// The run records elsewhere, so the history file the command reads afterwards is absent.
		other := filepath.Join(root, "recorded-elsewhere.db")
		runtime.runDeps.OpenHistory = func(ctx context.Context, _ string) (monitor.History, error) {
			return history.OpenWriter(ctx, other)
		}
	})
	availableMustBeEmptySection(t, got.stdout)
	if got.code != 0 || strings.Contains(got.stderr, "never-seen") || strings.Contains(got.stderr, availableHistoryFailure) {
		t.Fatalf("exit=%d stderr=%q, want 0 and nothing said", got.code, got.stderr)
	}
	if _, err := os.Stat(got.database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the post-run read created the history file: %v", err)
	}
}

func TestAvailableRunReaderOpenFailureAfterTheRunExitsThreeWithTheReport(t *testing.T) {
	prober := availableProber{reply: map[string]bool{"127.0.0.1": true}}
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, prober, func(root string, runtime *commandRuntime) {
		runtime.readDeps.OpenHistory = func(context.Context, string) (monitor.Reader, error) {
			return nil, errors.New("reader cannot open")
		}
	})
	availableMustBeEmptySection(t, got.stdout)
	var report struct {
		Summary []struct {
			Rounds int `json:"rounds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil || len(report.Summary) != 1 || report.Summary[0].Rounds != 1 {
		t.Fatalf("the report lost the run: %v %+v", err, report)
	}
	if got.code != 3 || strings.Count(got.stderr, availableHistoryFailure) != 1 || !strings.Contains(got.stderr, "history operation failed: reader cannot open") {
		t.Fatalf("exit=%d stderr=%q, want 3 and exactly one history operation failed line", got.code, got.stderr)
	}
	if strings.Contains(got.stderr, "never-seen") {
		t.Fatalf("a failed read said the list was empty: %q", got.stderr)
	}
}

func TestAvailableRunStreamFailureAfterTheRunExitsThreeWithTheReport(t *testing.T) {
	prober := availableProber{reply: map[string]bool{"127.0.0.1": true}}
	request := availableRequest(t, "127.0.0.1")
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, prober, func(root string, runtime *commandRuntime) {
		// A history holding this request and a round that does not fit its host.
		damaged := filepath.Join(root, "damaged.db")
		availableSeed(t, damaged, request, "damaged", ".")
		mutateRerunDB(t, damaged, `UPDATE round SET observations = '..'`)
		runtime.readDeps.OpenHistory = func(ctx context.Context, _ string) (monitor.Reader, error) {
			return openReadOnlyHistory(ctx, damaged)
		}
	})
	availableMustBeEmptySection(t, got.stdout)
	if got.code != 3 || strings.Count(got.stderr, availableHistoryFailure) != 1 || !strings.Contains(got.stderr, "invalid stored round") {
		t.Fatalf("exit=%d stderr=%q, want 3 and one history operation failed line naming the round", got.code, got.stderr)
	}
}

func TestAvailableRunWhoseHistoryNeverOpenedSaysItOnceAndReadsNothing(t *testing.T) {
	prober := availableProber{reply: map[string]bool{"127.0.0.1": true}}
	reads := 0
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, prober, func(root string, runtime *commandRuntime) {
		runtime.runDeps.OpenHistory = func(context.Context, string) (monitor.History, error) {
			return nil, errors.New("history is unavailable")
		}
		runtime.readDeps.OpenHistory = func(context.Context, string) (monitor.Reader, error) {
			reads++
			return nil, errors.New("must not be called")
		}
	})
	availableMustBeEmptySection(t, got.stdout)
	if got.code != 3 || reads != 0 {
		t.Fatalf("exit=%d, reads=%d; want 3 and no read after a history failure the run reported", got.code, reads)
	}
	if strings.Count(got.stderr, "history is unavailable") != 1 || strings.Contains(got.stderr, "history operation failed") || strings.Contains(got.stderr, "never-seen") {
		t.Fatalf("stderr = %q, want the run's own history line once and nothing else", got.stderr)
	}
}

// A run that fails (here, an unexpected resolver error, which makes monitor.Run
// return an error) is not read past: nothing is opened, nothing is said about
// never-seen hosts, and the report carries the empty section with exit 3.
func TestAvailableRunThatReturnsAnErrorReadsNothing(t *testing.T) {
	reads := 0
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, availableProber{}, func(_ string, runtime *commandRuntime) {
		runtime.runDeps.Prober = commandTestProber{resolveErr: errors.New("unexpected resolver failure")}
		runtime.readDeps.OpenHistory = func(context.Context, string) (monitor.Reader, error) {
			reads++
			return nil, errors.New("must not be called")
		}
	})
	availableMustBeEmptySection(t, got.stdout)
	if got.code != 3 || reads != 0 {
		t.Fatalf("exit=%d reads=%d, want 3 and no read after a run error\nstderr=%q", got.code, reads, got.stderr)
	}
	if !strings.Contains(got.stderr, "run failed: ") || strings.Contains(got.stderr, "never-seen") || strings.Contains(got.stderr, availableHistoryFailure) {
		t.Fatalf("stderr = %q, want the run failure and no never-seen or history operation failed line", got.stderr)
	}
}

// The live screen owns stderr while it is open, so the post-run read and its
// notes come after it is closed: the notes then reach stderr itself, not the
// screen.
func TestAvailableRunClosesTheScreenBeforeTheReadAndTheNotes(t *testing.T) {
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()
	state := &commandScreenState{screen: output.OpenScreen(nil, pipeWriter, "default", true, output.KeyHandlers{})}
	screenOpenAtRead := true
	prober := availableProber{reply: map[string]bool{"127.0.0.1": true}}
	got := availableCLI(t, []string{"127.0.0.1", "--available", "--format", "json"}, prober, func(_ string, runtime *commandRuntime) {
		runtime.screenState = state
		runtime.readDeps.OpenHistory = func(ctx context.Context, path string) (monitor.Reader, error) {
			screenOpenAtRead = state.screen != nil
			return openReadOnlyHistory(ctx, path)
		}
	})
	if screenOpenAtRead {
		t.Fatal("the live screen was still open when the never-seen list was read")
	}
	if strings.Count(got.stderr, availableNothingNeverSeen) != 1 {
		t.Fatalf("stderr = %q, want the no-never-seen line itself once", got.stderr)
	}
	_ = pipeWriter.Close()
	onScreen, err := io.ReadAll(pipeReader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onScreen), "never-seen") {
		t.Fatalf("the note went to the screen, not stderr: %q", onScreen)
	}
}

func TestAvailableRunNamesEachSilentSubnetOnceInTokenOrderAndNotesOnlyItsUnseenHosts(t *testing.T) {
	request := availableRequest(t, "10.9.9.0/30", "10.0.0.0/30", "10.5.5.0/30")
	if len(request.Tokens) != 3 {
		t.Fatalf("tokens = %v", request.Tokens)
	}
	prober := availableProber{reply: map[string]bool{"10.5.5.1": true}}
	got := availableCLI(t, []string{"10.9.9.0/30", "10.0.0.0/30", "10.5.5.0/30", "--available", "--format", "json"}, prober, nil)
	rows, present := availableUnseen(t, got.stdout)
	if got.code != 0 || !present || len(rows) != 5 {
		t.Fatalf("exit=%d unseen=%+v present=%t, want 0 and the five silent hosts", got.code, rows, present)
	}
	notes := map[string]string{}
	for _, row := range rows {
		notes[row.Host] = row.Note
	}
	for _, host := range []string{"10.0.0.1", "10.0.0.2", "10.9.9.1", "10.9.9.2"} {
		if notes[host] != monitor.SilentSubnetNote {
			t.Errorf("%s note = %q, want the silent-subnet note", host, notes[host])
		}
	}
	if notes["10.5.5.2"] != "" {
		t.Errorf("10.5.5.2 note = %q: its subnet has a replying host", notes["10.5.5.2"])
	}

	var silent []string
	for _, token := range request.Tokens {
		if token != "10.5.5.0/30" {
			silent = append(silent, "cn-mon: no IP in "+token+" ever replied; this machine may not be able to reach it\n")
		}
	}
	if want := strings.Join(silent, ""); got.stderr != want {
		t.Fatalf("stderr = %q, want each silent subnet once in token order and nothing else: %q", got.stderr, want)
	}
	// The same notes are logged at INFO, so docs/PRIVACY.md says a typed subnet can reach the log.
	log := readCommandLog(t, got.logPath)
	for _, token := range request.Tokens {
		if token != "10.5.5.0/30" && !strings.Contains(log, "INFO - no IP in "+token+" ever replied") {
			t.Errorf("the silent-subnet note for %s is not in the log:\n%s", token, log)
		}
	}
}

func TestAvailableAllRejectedTargetsReportCarriesAnEmptySection(t *testing.T) {
	got := availableCLI(t, []string{"999.1", "--available", "--format", "json"}, availableProber{}, nil)
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", got.code, got.stderr)
	}
	availableMustBeEmptySection(t, got.stdout)
	if _, err := os.Stat(got.database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an all-rejected command opened history: %v", err)
	}
	plain := availableCLI(t, []string{"999.1", "--format", "json"}, availableProber{}, nil)
	if _, present := availableUnseen(t, plain.stdout); present {
		t.Fatalf("without --available the refusal report has an unseen key: %s", plain.stdout)
	}
}

func TestAvailableShowListsNeverSeenHostsAndNotTestedWithTheirEvidence(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	request := availableRequest(t, "10.0.0.0/30", "10.9.9.0/30")
	if got := strings.Join(request.Hosts, ","); got != "10.0.0.1,10.0.0.2,10.9.9.1,10.9.9.2" {
		t.Fatalf("hosts = %s", got)
	}
	// Host 1 answers once; host 2 is silent; the second subnet never answers; host 4 failed every round.
	availableSeed(t, database, request, "show-run", "!...", "..E.", "...E")
	args := []string{"--show", request.Key[:8], "--available"}

	csvRun := availableCLIAt(t, database, logPath, append(args, "--format", "csv"), availableProber{}, nil)
	if csvRun.code != 0 {
		t.Fatalf("exit = %d, want 0: a host answered; stderr=%q", csvRun.code, csvRun.stderr)
	}
	unseen := availableCSVSection(t, csvRun.stdout, "unseen")
	if len(unseen) != 3 {
		t.Fatalf("CSV unseen rows = %v, want the three silent hosts:\n%s", unseen, csvRun.stdout)
	}
	for i, want := range []map[string]string{
		{"Host": "10.0.0.2", "Status": "Unseen", "Seen": "0 of 3 rounds", "Failed rounds": "0", "Note": ""},
		{"Host": "10.9.9.1", "Status": "Unseen", "Seen": "0 of 2 rounds", "Failed rounds": "1", "Note": monitor.SilentSubnetNote},
		{"Host": "10.9.9.2", "Status": "Unseen", "Seen": "0 of 2 rounds", "Failed rounds": "1", "Note": monitor.SilentSubnetNote},
	} {
		for column, value := range want {
			if unseen[i][column] != value {
				t.Errorf("CSV unseen row %d column %q = %q, want %q", i, column, unseen[i][column], value)
			}
		}
	}
	if want := "cn-mon: no IP in 10.9.9.0/30 ever replied; this machine may not be able to reach it\n"; csvRun.stderr != want {
		t.Fatalf("stderr = %q, want %q", csvRun.stderr, want)
	}

	js := availableCLIAt(t, database, logPath, append(args, "--format", "json"), availableProber{}, nil)
	rows, present := availableUnseen(t, js.stdout)
	if !present || len(rows) != 3 || rows[0].Host != "10.0.0.2" || rows[1].Host != "10.9.9.1" || rows[2].Host != "10.9.9.2" {
		t.Fatalf("unseen = %+v, want the three silent hosts in host order", rows)
	}
	if rows[2].Rounds != 2 || rows[2].FailedRounds != 1 || rows[2].Status != "Unseen" || rows[2].Note != monitor.SilentSubnetNote {
		t.Fatalf("10.9.9.2 = %+v, want 2 tested rounds, 1 failed, Unseen with the note", rows[2])
	}
}

// The summary reads the same rounds as the fold, so through the command a
// malformed round fails the summary read first: the report is the empty one
// with exit 3 and one history operation failed line. Only a failure of the
// fold's own I/O reaches the keeps-the-summary branch (finishShowCoverage).
func TestAvailableShowOfAMalformedRoundFailsTheSummaryReadFirst(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	request := availableRequest(t, "192.0.2.1", "192.0.2.2")
	availableSeed(t, database, request, "damaged-show", "!.", "..")
	mutateRerunDB(t, database, `UPDATE round SET observations = '.' WHERE observations = '..'`)

	got := availableCLIAt(t, database, logPath, []string{"--show", request.Key[:8], "--available", "--format", "json"}, availableProber{}, nil)
	availableMustBeEmptySection(t, got.stdout)
	var report struct {
		Seen    []json.RawMessage `json:"seen"`
		Summary []json.RawMessage `json:"summary"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil || len(report.Seen) != 0 || len(report.Summary) != 0 {
		t.Fatalf("the report kept a summary after a malformed round: %v %+v", err, report)
	}
	if got.code != 3 || strings.Count(got.stderr, availableHistoryFailure) != 1 || !strings.Contains(got.stderr, "invalid stored round") {
		t.Fatalf("exit=%d stderr=%q, want 3 and one history operation failed line naming the round", got.code, got.stderr)
	}
}

func TestAvailableShowExitsOneForAnAllSilentRequestAndZeroForASeenOne(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	silent := availableRequest(t, "192.0.2.1")
	seen := availableRequest(t, "192.0.2.2")
	availableSeed(t, database, silent, "silent-run", ".", ".")
	availableSeed(t, database, seen, "seen-run", "!")

	got := availableCLIAt(t, database, logPath, []string{"--show", silent.Key[:8], "--available", "--format", "json"}, availableProber{}, nil)
	rows, _ := availableUnseen(t, got.stdout)
	if got.code != 1 || len(rows) != 1 || rows[0].Status != "Unseen" {
		t.Fatalf("all silent: exit=%d unseen=%+v, want 1 and one Unseen host", got.code, rows)
	}
	got = availableCLIAt(t, database, logPath, []string{"--show", seen.Key[:8], "--available", "--format", "json"}, availableProber{}, nil)
	availableMustBeEmptySection(t, got.stdout)
	if got.code != 0 || strings.Count(got.stderr, availableNothingNeverSeen) != 1 {
		t.Fatalf("all seen: exit=%d stderr=%q, want 0 and the no-never-seen line once", got.code, got.stderr)
	}
}

func TestAvailableShowOfARequestWithoutRoundsListsEveryHostNotTested(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	request := availableRequest(t, "192.0.2.1", "192.0.2.2")
	availableSeed(t, database, request, "roundless")
	got := availableCLIAt(t, database, logPath, []string{"--show", request.Key[:8], "--available", "--format", "json"}, availableProber{}, nil)
	rows, present := availableUnseen(t, got.stdout)
	if got.code != 1 || !present || len(rows) != 2 {
		t.Fatalf("exit=%d unseen=%+v, want 1 and two rows", got.code, rows)
	}
	for _, row := range rows {
		if row.Status != "Not tested" || row.Rounds != 0 || row.Since != "" {
			t.Errorf("row = %+v, want Not tested with no rounds and an empty since", row)
		}
	}
	if strings.Contains(got.stderr, "never-seen") {
		t.Errorf("a list of untested hosts said nothing is never seen: %q", got.stderr)
	}
}

func TestAvailableShowRefusalsAndFailuresCarryAnEmptySectionAndCreateNothing(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")

	missing := availableCLIAt(t, database, logPath, []string{"--show", "abcd1234", "--available", "--format", "json"}, availableProber{}, nil)
	availableMustBeEmptySection(t, missing.stdout)
	if missing.code != 2 || !strings.Contains(missing.stderr, "no history found") {
		t.Fatalf("missing history: exit=%d stderr=%q, want the refusal it was", missing.code, missing.stderr)
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--show --available created the history: %v", err)
	}

	request := availableRequest(t, "192.0.2.1")
	availableSeed(t, database, request, "known", "!")
	other := availableCLIAt(t, database, logPath, []string{"--show", "ffffffff", "--available", "--format", "json"}, availableProber{}, nil)
	availableMustBeEmptySection(t, other.stdout)
	if other.code != 2 || !strings.Contains(other.stderr, "no request matches") || strings.Contains(other.stderr, "never-seen") {
		t.Fatalf("unmatched ID: exit=%d stderr=%q, want 2 and only the refusal", other.code, other.stderr)
	}

	failed := availableCLIAt(t, database, logPath, []string{"--show", request.Key[:8], "--available", "--format", "json"}, availableProber{}, func(root string, runtime *commandRuntime) {
		runtime.readDeps.OpenHistory = func(context.Context, string) (monitor.Reader, error) {
			return nil, errors.New("reader cannot open")
		}
	})
	availableMustBeEmptySection(t, failed.stdout)
	if failed.code != 3 || strings.Count(failed.stderr, availableHistoryFailure) != 1 || strings.Contains(failed.stderr, "never-seen") {
		t.Fatalf("read failure: exit=%d stderr=%q, want 3 and one history operation failed line", failed.code, failed.stderr)
	}

	plain := availableCLIAt(t, database, logPath, []string{"--show", "ffffffff", "--format", "json"}, availableProber{}, nil)
	if _, present := availableUnseen(t, plain.stdout); present {
		t.Fatalf("without --available the show refusal has an unseen key: %s", plain.stdout)
	}
}

func TestAvailableRefusalsPrintNoReportAndTouchNothing(t *testing.T) {
	for _, args := range [][]string{
		{"--list", "--available"},
		{"--follow", "abcd1234", "--available"},
		{"--show", "abcd1234", "--available", "--since", "1h"},
		{"127.0.0.1", "--available", "--since", "0m"},
	} {
		got := availableCLI(t, args, availableProber{}, nil)
		if got.code != 2 || got.stdout != "" || !strings.HasPrefix(got.stderr, "cn-mon: --available ") {
			t.Errorf("%q: exit=%d stdout=%q stderr=%q, want a bare exit 2 refusal", args, got.code, got.stdout, got.stderr)
		}
		for _, path := range []string{got.database, got.logPath} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%q created %s: %v", args, path, err)
			}
		}
	}
}

func TestAvailableShowWithSinceLeavesAnExistingDatabaseUntouched(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	request := availableRequest(t, "192.0.2.1")
	availableSeed(t, database, request, "known", "!")
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	got := availableCLIAt(t, database, logPath, []string{"--show", request.Key[:8], "--available", "--since", "1h"}, availableProber{}, nil)
	after, readErr := os.ReadFile(database)
	if got.code != 2 || got.stdout != "" || readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("exit=%d stdout=%q: the refusal printed a report or changed the database (%v)", got.code, got.stdout, readErr)
	}
}

func TestAvailableHelpIsUnaffectedAndTheUsageNamesTheOptionWithItsRestriction(t *testing.T) {
	got := availableCLI(t, []string{"--available", "--help"}, availableProber{}, nil)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("--available --help: exit=%d stderr=%q", got.code, got.stderr)
	}
	runOptions := strings.Index(got.stdout, "Run options:")
	historyOptions := strings.Index(got.stdout, "History options:")
	if runOptions < 0 || historyOptions < runOptions {
		t.Fatalf("usage lacks the run and history option sections:\n%s", got.stdout)
	}
	for name, section := range map[string]string{
		"run options":     got.stdout[runOptions:historyOptions],
		"history options": got.stdout[historyOptions:],
	} {
		if !strings.Contains(section, "--available") {
			t.Errorf("%s do not name --available:\n%s", name, section)
		}
	}
	for _, want := range []string{"[--since WHEN|--available]", "never with --since"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

// A show whose fold failed after the summary was read keeps its report: the
// failure is said once, nothing is claimed about never-seen hosts, and the
// outcome keeps the empty section and its exit status of 3.
func TestFinishShowCoverageSaysAFoldFailureOnceAndNotesASuccessfulFold(t *testing.T) {
	var notes, failures []string
	say := func(message string) { notes = append(notes, message) }
	failed := func(err error) { failures = append(failures, err.Error()) }

	failure := errors.New("stored round does not fit")
	out := monitor.ReadOutcome{Found: true, Run: monitor.RunOutcome{Coverage: &monitor.Coverage{}, HistoryError: failure}}
	finishShowCoverage(&out, say, failed)
	if len(notes) != 0 || len(failures) != 1 || failures[0] != failure.Error() || out.Run.Coverage == nil || monitor.ReadExitCode(out) != 3 {
		t.Fatalf("failed fold: notes=%q failures=%q coverage=%v exit=%d", notes, failures, out.Run.Coverage, monitor.ReadExitCode(out))
	}

	notes, failures = nil, nil
	out = monitor.ReadOutcome{Problem: "no request matches \"x\""}
	finishShowCoverage(&out, say, failed)
	if len(notes) != 0 || len(failures) != 0 || out.Run.Coverage == nil || len(out.Run.Coverage.Hosts) != 0 {
		t.Fatalf("problem outcome: notes=%q failures=%q coverage=%+v, want the empty section and no note", notes, failures, out.Run.Coverage)
	}

	fold := monitor.NewCoverageFold([]string{"10.0.0.1"}, []monitor.Subnet{{Token: "10.0.0.0/30", Members: []int{0}}})
	if err := fold.Add(time.Now(), "."); err != nil {
		t.Fatal(err)
	}
	coverage := fold.Coverage()
	notes, failures = nil, nil
	out = monitor.ReadOutcome{Found: true, Run: monitor.RunOutcome{Coverage: &coverage}}
	finishShowCoverage(&out, say, failed)
	if want := []string{"no IP in 10.0.0.0/30 ever replied; this machine may not be able to reach it"}; strings.Join(notes, "|") != strings.Join(want, "|") || len(failures) != 0 {
		t.Fatalf("silent subnet: notes=%q failures=%q, want %q", notes, failures, want)
	}
}
