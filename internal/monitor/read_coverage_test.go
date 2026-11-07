// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

// Fixtures are copied from the read_coverage tests of
// tests/test_utils_ping_coverage.py in cn-tool merge
// a739948c69ada9bab27d25058c1d695ca06d5aba (PR #178, Apache-2.0):
// test_read_coverage_folds_every_run_of_the_request,
// test_read_coverage_marks_a_silent_subnet_from_the_request_targets,
// test_read_coverage_of_a_request_without_rounds_is_not_tested,
// test_a_stored_round_that_does_not_fit_is_a_store_error and
// test_read_coverage_reads_one_snapshot; the show-path cases follow
// test_available_show_with_no_rounds_lists_not_tested and
// test_available_history_failure_exits_3 of tests/test_modules_ping_monitor.py.

const coverageKey = "a3f9c2e1" + "00000000000000000000000000000000000000000000000000000000"

func coverageRequest(tokens, hosts []string) target.Request {
	return target.Request{Tokens: tokens, Hosts: hosts, Ports: []int{}, Key: coverageKey}
}

// coverageStored builds a stored round whose seen hosts carry detail rows.
func coverageStored(request target.Request, started time.Time, observations string) history.RoundInput {
	round := history.RoundInput{Started: started, Observations: observations}
	for i, state := range []byte(observations) {
		if isSeen(state) {
			round.Rows = append(round.Rows, history.ResultRow{Host: request.Hosts[i], Address: request.Hosts[i], Result: "OK", Time: started})
		}
	}
	return round
}

func coverageSeed(t *testing.T, path string, request target.Request, runID string, started time.Time, rounds ...coverageRound) {
	t.Helper()
	stored := make([]history.RoundInput, len(rounds))
	for i, round := range rounds {
		stored[i] = coverageStored(request, coverageAt(round.at), round.observations)
	}
	seedReadHistory(t, path, request, runID, started, nil, stored, "finished")
}

// coverageCorrupt appends a stored round that no writer would record, so the
// streaming read, which validates every round of the request, must refuse it.
func coverageCorrupt(t *testing.T, path, runID string, started time.Time, observations string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seconds := float64(started.Unix())
	if _, err := db.Exec(`INSERT INTO round(run_id, started, observations, answered) VALUES(?, ?, ?, 0)`, runID, seconds, observations); err != nil {
		t.Fatalf("insert corrupt round: %v", err)
	}
}

func coverageDeps(reader *readObservedReader) ReadDeps {
	deps := readDeps(coverageT0.Add(24 * time.Hour))
	deps.OpenHistory = func(context.Context, string) (Reader, error) { return reader, nil }
	return deps
}

func coverageOpen(t *testing.T, path string) *readObservedReader {
	t.Helper()
	store, err := history.OpenReader(context.Background(), path)
	if err != nil || store == nil {
		t.Fatalf("OpenReader: %v, %v", store, err)
	}
	return &readObservedReader{Reader: store}
}

func TestReadCoverageFoldsEveryRunOfTheRequestInOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	hosts := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "192.0.2.7"}
	request := coverageRequest([]string{"10.0.0.0/30", "192.0.2.7"}, hosts)
	coverageSeed(t, path, request, "old", coverageT0, coverageRound{0, "!..E"})
	coverageSeed(t, path, request, "new", coverageAt(7200), coverageRound{7200, "..!."})

	reader := coverageOpen(t, path)
	coverage, err := ReadCoverage(context.Background(), path, request.Key, coverageDeps(reader))
	if err != nil || coverage == nil {
		t.Fatalf("ReadCoverage = %+v, %v", coverage, err)
	}
	if reader.views != 1 || reader.closed != 1 {
		t.Fatalf("view/close count = %d/%d, want exactly one snapshot and one close", reader.views, reader.closed)
	}
	if coverage.Rounds != 2 || coverage.Since == nil || !coverage.Since.Equal(coverageT0) || len(coverage.SilentSubnets) != 0 {
		t.Fatalf("coverage = %+v, want 2 rounds since %v and no silent subnet", coverage, coverageT0)
	}
	if got, want := coverageStatuses(*coverage), []string{CoverageSeen, CoverageUnseen, CoverageSeen, CoverageUnseen}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
}

func TestReadCoverageMarksASilentSubnetFromTheStoredTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30", "10.9.9.0/30"}, []string{"10.0.0.1", "10.0.0.2", "10.9.9.1"})
	coverageSeed(t, path, request, "r", coverageT0, coverageRound{0, "!.."})

	coverage, err := ReadCoverage(context.Background(), path, request.Key, readDeps(coverageT0))
	if err != nil || coverage == nil {
		t.Fatalf("ReadCoverage = %+v, %v", coverage, err)
	}
	if want := []string{"10.9.9.0/30"}; !reflect.DeepEqual(coverage.SilentSubnets, want) {
		t.Fatalf("SilentSubnets = %v, want %v", coverage.SilentSubnets, want)
	}
	if got, want := coverageNotes(*coverage), []string{"", "", SilentSubnetNote}; !reflect.DeepEqual(got, want) {
		t.Fatalf("notes = %v, want %v", got, want)
	}
}

func TestReadCoverageOfARequestWithoutRoundsIsNotTested(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1"})
	coverageSeed(t, path, request, "r", coverageT0)

	coverage, err := ReadCoverage(context.Background(), path, request.Key, readDeps(coverageT0))
	if err != nil || coverage == nil {
		t.Fatalf("ReadCoverage = %+v, %v", coverage, err)
	}
	if coverage.Rounds != 0 || coverage.Since != nil || !reflect.DeepEqual(coverageStatuses(*coverage), []string{CoverageNotTested}) {
		t.Fatalf("coverage = %+v, want no rounds, no since and Not tested", coverage)
	}
}

func TestReadCoverageReturnsNothingForAnAbsentFileOrAnUnheldKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "coverage.db")
	coverage, err := ReadCoverage(context.Background(), path, coverageKey, readDeps(coverageT0))
	if coverage != nil || err != nil {
		t.Fatalf("absent file = %+v, %v; want nil, nil", coverage, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadCoverage created the missing history path: stat err=%v", err)
	}

	path = filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1"})
	coverageSeed(t, path, request, "r", coverageT0, coverageRound{0, "!"})
	reader := coverageOpen(t, path)
	coverage, err = ReadCoverage(context.Background(), path, strings.Repeat("b", 64), coverageDeps(reader))
	if coverage != nil || err != nil {
		t.Fatalf("unheld key = %+v, %v; want nil, nil", coverage, err)
	}
	if reader.views != 1 || reader.closed != 1 {
		t.Fatalf("view/close count = %d/%d, want the reader closed once", reader.views, reader.closed)
	}
}

func TestReadCoverageJoinsViewAndCloseErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1"})
	coverageSeed(t, path, request, "r", coverageT0, coverageRound{0, "!"})
	reader := coverageOpen(t, path)
	viewErr, closeErr := errors.New("injected view failure"), errors.New("injected close failure")
	reader.viewErr, reader.closeErr = viewErr, closeErr

	coverage, err := ReadCoverage(context.Background(), path, request.Key, coverageDeps(reader))
	if coverage != nil || !errors.Is(err, viewErr) || !errors.Is(err, closeErr) {
		t.Fatalf("ReadCoverage = %+v, %v; want nil and both errors joined", coverage, err)
	}
	if reader.closed != 1 {
		t.Fatalf("closed %d times, want once", reader.closed)
	}
}

func TestReadCoverageReportsAMisfitRoundAsAHistoryErrorAndClosesTheReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1", "10.0.0.2"})
	coverageSeed(t, path, request, "r", coverageT0, coverageRound{0, "!."})
	coverageCorrupt(t, path, "r", coverageAt(60), "!..")

	reader := coverageOpen(t, path)
	coverage, err := ReadCoverage(context.Background(), path, request.Key, coverageDeps(reader))
	if coverage != nil || err == nil || !strings.Contains(err.Error(), "invalid stored round") {
		t.Fatalf("ReadCoverage = %+v, %v; want nil and an invalid stored round history error", coverage, err)
	}
	if reader.closed != 1 {
		t.Fatalf("closed %d times, want once", reader.closed)
	}
}

func TestReadWithAvailableFoldsInTheShowSnapshotAndKeepsTheSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30", "10.9.9.0/30"}, []string{"10.0.0.1", "10.0.0.2", "10.9.9.1"})
	coverageSeed(t, path, request, "old", coverageT0, coverageRound{0, "!.."})
	coverageSeed(t, path, request, "new", coverageAt(7200), coverageRound{7200, "!.."})
	plan := ReadPlan{HistoryFile: path, Prefix: request.Key[:8]}

	plain, err := Read(context.Background(), plan, coverageDeps(coverageOpen(t, path)))
	if err != nil || plain.Run.Coverage != nil {
		t.Fatalf("Read without Available: coverage=%+v err=%v; want no coverage", plain.Run.Coverage, err)
	}

	plan.Available = true
	reader := coverageOpen(t, path)
	out, err := Read(context.Background(), plan, coverageDeps(reader))
	if err != nil || !out.Found {
		t.Fatalf("Read: %+v, %v", out, err)
	}
	if reader.views != 1 || reader.closed != 1 {
		t.Fatalf("view/close count = %d/%d, want the fold inside the one show snapshot", reader.views, reader.closed)
	}
	coverage := out.Run.Coverage
	if coverage == nil || coverage.Rounds != 2 || out.Run.HistoryError != nil {
		t.Fatalf("coverage = %+v, HistoryError = %v; want 2 rounds folded", coverage, out.Run.HistoryError)
	}
	if want := []string{"10.9.9.0/30"}; !reflect.DeepEqual(coverage.SilentSubnets, want) {
		t.Fatalf("SilentSubnets = %v, want %v", coverage.SilentSubnets, want)
	}
	if out.Run.CurrentRounds != plain.Run.CurrentRounds || len(out.Run.Seen) != len(plain.Run.Seen) || !reflect.DeepEqual(out.Run.Latest, plain.Run.Latest) {
		t.Fatalf("Available changed the summary: %+v vs %+v", out.Run, plain.Run)
	}
	if got := ReadExitCode(out); got != 0 {
		t.Fatalf("ReadExitCode = %d, want 0: a seen host", got)
	}
}

func TestReadWithAvailableListsEveryHostOfARoundlessRequestAsNotTested(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1", "10.0.0.2"})
	coverageSeed(t, path, request, "r", coverageT0)

	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Available: true}, readDeps(coverageT0))
	if err != nil || out.Run.Coverage == nil {
		t.Fatalf("Read: %+v, %v", out, err)
	}
	if got, want := out.Run.Coverage.Unseen(), []HostCoverage{{Host: "10.0.0.1", Status: CoverageNotTested}, {Host: "10.0.0.2", Status: CoverageNotTested}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Unseen() = %+v, want every host Not tested", got)
	}
	if out.Run.Coverage.Since != nil {
		t.Fatalf("Since = %v, want none without rounds", out.Run.Coverage.Since)
	}
	if got := ReadExitCode(out); got != 1 {
		t.Fatalf("ReadExitCode = %d, want 1: nothing seen", got)
	}
}

// The fold reads the whole history while the summary reads the window, so a
// round older than the window that no writer would have recorded fails the
// fold alone. That is the only way to reach the keeps-the-summary branch with a
// malformed round, and it needs Since together with Available, which the
// command's parser refuses. Through the command a malformed round fails the
// summary read first (the report is then empty, exit 3; see
// TestAvailableShowOfAMalformedRoundFailsTheSummaryReadFirst in cmd/cn-mon), so
// in practice only a failure of the fold's own I/O leaves a summary beside a
// failed fold.
func TestReadWithAvailableKeepsTheSummaryAndSetsHistoryErrorWhenTheStreamFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1", "10.0.0.2"})
	coverageSeed(t, path, request, "r", coverageT0, coverageRound{3600, "!."})
	coverageCorrupt(t, path, "r", coverageT0, "!.?.")
	since := coverageAt(1800)

	reader := coverageOpen(t, path)
	out, err := Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: request.Key[:8], Since: &since, Available: true}, coverageDeps(reader))
	if err != nil {
		t.Fatalf("Read: %v; a fold failure must not fail the read", err)
	}
	if !out.Found || out.Run.CurrentRounds != 1 || len(out.Run.Seen) != 1 {
		t.Fatalf("summary was not kept: %+v", out.Run)
	}
	if out.Run.HistoryError == nil || !strings.Contains(out.Run.HistoryError.Error(), "invalid stored round") {
		t.Fatalf("HistoryError = %v, want the stream failure", out.Run.HistoryError)
	}
	if out.Run.Coverage == nil || out.Run.Coverage.Rounds != 0 || len(out.Run.Coverage.Hosts) != 0 {
		t.Fatalf("Coverage = %+v, want an empty non-nil coverage", out.Run.Coverage)
	}
	if got := ReadExitCode(out); got != 3 {
		t.Fatalf("ReadExitCode = %d, want 3", got)
	}
	if reader.views != 1 || reader.closed != 1 {
		t.Fatalf("view/close count = %d/%d", reader.views, reader.closed)
	}
}

func TestReadWithAvailableLeavesCoverageNilOnProblemOutcomes(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	out, err := Read(context.Background(), ReadPlan{HistoryFile: missing, Prefix: "a3f9c2e1", Available: true}, readDeps(coverageT0))
	if err != nil || out.Problem == "" || out.Run.Coverage != nil || ReadExitCode(out) != 2 {
		t.Fatalf("missing history: %+v, %v", out, err)
	}

	path := filepath.Join(t.TempDir(), "coverage.db")
	request := coverageRequest([]string{"10.0.0.0/30"}, []string{"10.0.0.1"})
	coverageSeed(t, path, request, "r", coverageT0, coverageRound{0, "!"})
	out, err = Read(context.Background(), ReadPlan{HistoryFile: path, Prefix: "ffffffff", Available: true}, readDeps(coverageT0))
	if err != nil || out.Problem == "" || out.Run.Coverage != nil || ReadExitCode(out) != 2 {
		t.Fatalf("unmatched prefix: %+v, %v", out, err)
	}
}

func TestReadExitCodeIsThreeForAHistoryError(t *testing.T) {
	failed := ReadOutcome{Found: true, Run: RunOutcome{Stats: Stats{Seen: 1}, HistoryError: errors.New("history operation failed")}}
	if got := ReadExitCode(failed); got != 3 {
		t.Fatalf("seen host with a history error: exit = %d, want 3 before the reachability rules", got)
	}
	failed.Found = false
	if got := ReadExitCode(failed); got != 3 {
		t.Fatalf("history error without a found request: exit = %d, want 3", got)
	}
}
