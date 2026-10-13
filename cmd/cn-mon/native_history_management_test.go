// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build native

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestNativeCommandHistoryManagement(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "management history.db")
	logPath := filepath.Join(directory, "management.log")
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	requestA, runID := seedNativeManagementRun(t, database, "192.0.2.77", "Case/pipe|Δ", hostname, int64(os.Getpid()), "finished")
	requestB, requestRunID := seedNativeManagementRun(t, database, "192.0.2.78", "request/β", hostname, int64(os.Getpid()), "finished")

	for _, format := range []string{"json", "table", "csv", "md"} {
		t.Run("list-runs/"+format, func(t *testing.T) {
			code, stdout, stderr := runNativeHistoryManagementCommand(t, binary, directory, database, logPath,
				"--list-runs", requestA.Key[:8], "--format", format)
			if code != 0 {
				t.Fatalf("list-runs exit=%d stderr=%q", code, stderr)
			}
			switch format {
			case "json":
				var report struct {
					Runs []struct {
						ID        string  `json:"id"`
						RequestID string  `json:"request_id"`
						Status    string  `json:"status"`
						State     string  `json:"state"`
						Started   string  `json:"started"`
						Ended     *string `json:"ended"`
						Host      string  `json:"host"`
						PID       int64   `json:"pid"`
						Rounds    int64   `json:"rounds"`
					} `json:"runs"`
				}
				if err := json.Unmarshal(stdout, &report); err != nil || len(report.Runs) != 1 {
					t.Fatalf("decode JSON run list rows=%d err=%v stdout=%q", len(report.Runs), err, stdout)
				}
				row := report.Runs[0]
				if row.ID != runID || row.RequestID != requestA.Key || row.Status != "finished" || row.State != "finished" || row.Started == "" || row.Ended == nil || row.Host != hostname || row.PID != int64(os.Getpid()) || row.Rounds != 1 {
					t.Fatalf("JSON run row=%+v", row)
				}
			case "table":
				assertNativeManagementTableRun(t, stdout, runID, requestA.Key, "finished")
			case "md":
				if !bytes.Contains(stdout, []byte(`"Case/pipe\|Δ"`)) || !bytes.Contains(stdout, []byte(requestA.Key)) {
					t.Fatalf("Markdown run listing omitted escaped full ID/key: %q", stdout)
				}
			case "csv":
				records, err := csv.NewReader(bytes.NewReader(stdout)).ReadAll()
				if err != nil || len(records) != 2 || records[1][0] != "runs" || records[1][1] != runID || records[1][2] != requestA.Key || records[1][3] != "finished" {
					t.Fatalf("CSV run listing records=%v err=%v", records, err)
				}
			}
		})
	}

	code, stdout, stderr := runNativeHistoryManagementCommand(t, binary, directory, database, logPath, "--delete-run=case/pipe|Δ", "--format", "json")
	if code != 1 || len(stdout) != 0 || len(stderr) == 0 {
		t.Fatalf("case-changed run ID code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, database, logPath, "--delete-run="+runID, "--format", "json")
	if code != 0 {
		t.Fatalf("exact UTF-8 run deletion code=%d stderr=%q", code, stderr)
	}
	assertNativeManagementDeleteReport(t, stdout, history.DeleteResult{Requests: 1, Runs: 1, Rounds: 1, Results: 1, Committed: true})

	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, database, logPath, "--delete-request", strings.ToUpper(requestB.Key[:8]), "--format", "json")
	if code != 0 {
		t.Fatalf("unique case-insensitive request prefix deletion code=%d stderr=%q", code, stderr)
	}
	assertNativeManagementDeleteReport(t, stdout, history.DeleteResult{Requests: 1, Runs: 1, Rounds: 1, Results: 1, Committed: true})
	if requestRunID == "" {
		t.Fatal("seeded request run ID unexpectedly empty")
	}

	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, database, logPath, "--clear-history", "--format", "json")
	if code != 0 {
		t.Fatalf("clear existing empty history code=%d stderr=%q", code, stderr)
	}
	assertNativeManagementDeleteReport(t, stdout, history.DeleteResult{Committed: true})

	clearDatabase := filepath.Join(directory, "clear populated.db")
	seedNativeManagementRun(t, clearDatabase, "192.0.2.79", "clear-one", hostname, int64(os.Getpid()), "finished")
	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, clearDatabase, logPath, "--clear-history", "--format", "json")
	if code != 0 {
		t.Fatalf("clear populated history code=%d stderr=%q", code, stderr)
	}
	assertNativeManagementDeleteReport(t, stdout, history.DeleteResult{Requests: 1, Runs: 1, Rounds: 1, Results: 1, Committed: true})
	assertNativeManagementHistoryEmpty(t, clearDatabase)

	protectedDatabase := filepath.Join(directory, "protected.db")
	protectedRequest, protectedRun := seedNativeManagementRun(t, protectedDatabase, "192.0.2.80", "live-owned", hostname, int64(os.Getpid()), "running")
	for _, args := range [][]string{
		{"--delete-run=" + protectedRun},
		{"--delete-request", protectedRequest.Key[:8]},
		{"--clear-history"},
	} {
		args = append(args, "--history-file", protectedDatabase, "--log-file", logPath, "--format", "json")
		code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, protectedDatabase, logPath, args...)
		if code != 3 || len(stdout) != 0 || !bytes.Contains(stderr, []byte("active or cannot be safely verified")) {
			t.Fatalf("active local owner operation %v code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	assertNativeManagementHistoryHasRuns(t, protectedDatabase, protectedRequest.Key, 1)

	deadPID := startNativeManagementExitedChild(t)
	deadDatabase := filepath.Join(directory, "dead-local-owner.db")
	deadRequest, deadRun := seedNativeManagementRun(t, deadDatabase, "192.0.2.81", "dead-local-owner", hostname, int64(deadPID), "running")
	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, deadDatabase, logPath, "--delete-run="+deadRun, "--history-file", deadDatabase, "--log-file", logPath, "--format", "json")
	if code != 0 {
		t.Fatalf("dead local owner run deletion code=%d stderr=%q", code, stderr)
	}
	assertNativeManagementDeleteReport(t, stdout, history.DeleteResult{Requests: 1, Runs: 1, Rounds: 1, Results: 1, Committed: true})
	if deadRequest.Key == "" {
		t.Fatal("dead-owner fixture request key unexpectedly empty")
	}
	t.Log("CNMON_NATIVE_COMMAND_HISTORY_MANAGEMENT formats=json,table,csv,md exact-utf8-id=pass request-prefix=pass delete-cascade=pass clear=pass live-owner=protected dead-local-owner=deletable")
}

func assertNativeManagementTableRun(t *testing.T, output []byte, runID, requestKey, status string) {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	section := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "RUNS" {
			section = i
			break
		}
	}
	if section < 0 || section+1 >= len(lines) {
		t.Fatalf("table is missing RUNS section/header: %q", output)
	}
	requestAt := strings.Index(lines[section+1], "Request")
	statusAt := strings.Index(lines[section+1], "Status")
	stateAt := strings.Index(lines[section+1], "State")
	if requestAt < 2 || statusAt <= requestAt+2 || stateAt <= statusAt+2 {
		t.Fatalf("cannot identify table column boundaries from header %q", lines[section+1])
	}
	requestStart, statusStart, stateStart := len([]rune(lines[section+1][:requestAt])), len([]rune(lines[section+1][:statusAt])), len([]rune(lines[section+1][:stateAt]))
	widths := [3]int{requestStart - 2, statusStart - requestStart - 2, stateStart - statusStart - 2}
	starts := [3]int{0, requestStart, statusStart}
	cell := func(line string, column int) string {
		runes := []rune(line)
		start, end := starts[column], starts[column]+widths[column]
		if start >= len(runes) {
			return ""
		}
		if end > len(runes) {
			end = len(runes)
		}
		return strings.TrimSpace(string(runes[start:end]))
	}
	var headers [3]strings.Builder
	headerEnd := -1
	for i := section + 1; i < len(lines); i++ {
		for column := range headers {
			headers[column].WriteString(cell(lines[i], column))
		}
		if strings.ReplaceAll(headers[0].String(), " ", "") == "ID" && strings.ReplaceAll(headers[1].String(), " ", "") == "RequestID" && strings.ReplaceAll(headers[2].String(), " ", "") == "Status" {
			headerEnd = i + 1
			break
		}
	}
	if headerEnd < 0 {
		t.Fatalf("cannot reconstruct wrapped table headers from %q", output)
	}
	wantID, err := json.Marshal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var values [3]strings.Builder
	for _, line := range lines[headerEnd:] {
		for column := range values {
			values[column].WriteString(cell(line, column))
		}
	}
	if values[0].String() != string(wantID) || values[1].String() != requestKey || values[2].String() != status {
		t.Fatalf("table did not reconstruct exact first cells (ID=%q key=%q status=%q) from wrapped output: %q", wantID, requestKey, status, output)
	}
}

func TestNativeHistoryManagementPIDHelper(t *testing.T) {
	if os.Getenv("CNMON_NATIVE_HISTORY_PID_HELPER") == "1" {
		return
	}
}

func TestNativeCommandHistoryManagementReportsCommittedOutputFailure(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "report failure.db")
	logPath := filepath.Join(directory, "report failure.log")
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	request, runID := seedNativeManagementRun(t, database, "192.0.2.90", "output-failure-run", hostname, int64(os.Getpid()), "finished")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--delete-run="+runID, "--format", "json", "--history-file", database, "--log-file", logPath)
	command.Dir = directory
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err = command.Run()
	_ = writer.Close()
	if ctx.Err() != nil {
		t.Fatalf("closed-output deletion timed out: %v", ctx.Err())
	}
	if code := childExitCode(err); code != 0 || stderr.Len() != 0 {
		t.Fatalf("closed-output deletion exit=%d err=%v stderr=%q, want ordinary quiet pipe handling", code, err, stderr.String())
	}
	store, err := history.OpenReader(context.Background(), database)
	if err != nil || store == nil {
		t.Fatalf("open history after committed report failure: store=%v err=%v", store, err)
	}
	defer store.Close()
	err = store.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info != nil {
			return errors.New("run deletion was rolled back after output failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify committed delete after output error: %v", err)
	}

	missingDatabase := filepath.Join(directory, "missing-clear.db")
	missingReader, missingWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := missingReader.Close(); err != nil {
		t.Fatal(err)
	}
	var missingStderr bytes.Buffer
	missingCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	missing := exec.CommandContext(missingCtx, binary, "--clear-history", "--format", "json", "--history-file", missingDatabase, "--log-file", logPath)
	missing.Dir = directory
	missing.Env = nativeCommandEnv(t, directory, missingDatabase, logPath)
	missing.Stdout = missingWriter
	missing.Stderr = &missingStderr
	missingErr := missing.Run()
	_ = missingWriter.Close()
	if missingCtx.Err() != nil || childExitCode(missingErr) != 0 || missingStderr.Len() != 0 {
		t.Fatalf("missing-history clear with closed stdout exit=%d err=%v stderr=%q", childExitCode(missingErr), missingErr, missingStderr.String())
	}
	if _, err := os.Stat(missingDatabase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed-output clear created missing history: %v", err)
	}

	badOutputDatabase := filepath.Join(directory, "bad-output.db")
	badOutputRequest, badOutputRun := seedNativeManagementRun(t, badOutputDatabase, "192.0.2.95", "postcommit-output-failure", "fixture-host", 424242, "finished")
	readOnlyPath := filepath.Join(directory, "read-only-stdout")
	if err := os.WriteFile(readOnlyPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	readOnlyStdout, err := os.Open(readOnlyPath)
	if err != nil {
		t.Fatal(err)
	}
	badOutputCtx, badOutputCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer badOutputCancel()
	badOutput := exec.CommandContext(badOutputCtx, binary, "--delete-run="+badOutputRun, "--format", "json", "--history-file", badOutputDatabase, "--log-file", logPath)
	badOutput.Dir = directory
	badOutput.Env = nativeCommandEnv(t, directory, badOutputDatabase, logPath)
	badOutput.Stdout = readOnlyStdout
	var badOutputStderr bytes.Buffer
	badOutput.Stderr = &badOutputStderr
	badOutputErr := badOutput.Run()
	_ = readOnlyStdout.Close()
	if badOutputCtx.Err() != nil || childExitCode(badOutputErr) != 3 || !strings.Contains(badOutputStderr.String(), "history mutation committed; write deletion report") {
		t.Fatalf("non-pipe postcommit report failure exit=%d err=%v stderr=%q", childExitCode(badOutputErr), badOutputErr, badOutputStderr.String())
	}
	store, err = history.OpenReader(context.Background(), badOutputDatabase)
	if err != nil || store == nil {
		t.Fatalf("open history after non-pipe output error: store=%v err=%v", store, err)
	}
	verifyErr := store.View(context.Background(), func(snapshot *history.Snapshot) error {
		request, err := snapshot.Request(badOutputRequest.Key)
		if err != nil {
			return err
		}
		if request != nil {
			return errors.New("run deletion was rolled back after non-pipe output error")
		}
		return nil
	})
	_ = store.Close()
	if verifyErr != nil {
		t.Fatalf("verify committed deletion after non-pipe output error: %v", verifyErr)
	}
	t.Log("CNMON_NATIVE_COMMAND_HISTORY_MANAGEMENT postcommit-report-error=committed-diagnostic=pass exit=3 closed-pipe=quiet missing-clear=no-create")
}

func TestNativeCommandHistoryManagementSelectionSemantics(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	directory := t.TempDir()
	database := filepath.Join(directory, "selection.db")
	logPath := filepath.Join(directory, "selection.log")
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	request, runID := seedNativeManagementRun(t, database, "192.0.2.91", "zero-round-run", hostname, int64(os.Getpid()), "finished")
	db, err := sql.Open("sqlite3", database)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		conn.Close()
		db.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "DELETE FROM run WHERE id=?", runID); err != nil {
		conn.Close()
		db.Close()
		t.Fatal(err)
	}
	_ = conn.Close()
	_ = db.Close()
	code, stdout, stderr := runNativeHistoryManagementCommand(t, binary, directory, database, logPath, "--list-runs", request.Key[:8], "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("existing request with no runs code=%d stderr=%q", code, stderr)
	}
	var empty struct {
		Runs []json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(stdout, &empty); err != nil || empty.Runs == nil || len(empty.Runs) != 0 {
		t.Fatalf("existing zero-run request output=%q err=%v", stdout, err)
	}
	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, database, logPath, "--delete-request", request.Key[:8], "--format", "json")
	if code != 0 {
		t.Fatalf("delete zero-run request code=%d stderr=%q", code, stderr)
	}
	assertNativeManagementDeleteReport(t, stdout, history.DeleteResult{Requests: 1, Committed: true})

	ambiguousDatabase := filepath.Join(directory, "ambiguous.db")
	requestA, _ := seedNativeManagementRun(t, ambiguousDatabase, "192.0.2.92", "ambiguous-a", hostname, int64(os.Getpid()), "finished")
	requestB, _ := seedNativeManagementRun(t, ambiguousDatabase, "192.0.2.93", "ambiguous-b", hostname, int64(os.Getpid()), "finished")
	prefix := requestA.Key[:4]
	newKey := prefix + strings.Repeat("0", 60)
	if newKey == requestA.Key || newKey == requestB.Key {
		newKey = prefix + strings.Repeat("1", 60)
	}
	ambiguousDB, err := sql.Open("sqlite3", ambiguousDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ambiguousDB.Exec("UPDATE request SET key=? WHERE key=?", newKey, requestB.Key); err != nil {
		_ = ambiguousDB.Close()
		t.Fatalf("create isolated ambiguous-prefix fixture: %v", err)
	}
	if err := ambiguousDB.Close(); err != nil {
		t.Fatalf("close ambiguous-prefix fixture: %v", err)
	}
	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, ambiguousDatabase, logPath, "--delete-request", prefix, "--format", "json")
	if code != 2 || len(stdout) != 0 || !strings.Contains(string(stderr), "ambiguous") {
		t.Fatalf("ambiguous request prefix code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertNativeManagementHistoryHasRuns(t, ambiguousDatabase, requestA.Key, 1)
	assertNativeManagementHistoryHasRuns(t, ambiguousDatabase, newKey, 1)
	missingDatabase := filepath.Join(directory, "missing-list.db")
	code, stdout, stderr = runNativeHistoryManagementCommand(t, binary, directory, missingDatabase, logPath, "--list-runs", strings.Repeat("a", 64), "--format", "json")
	if code != 1 || len(stdout) != 0 || !strings.Contains(string(stderr), "not found") {
		t.Fatalf("missing list-runs code=%d stdout=%q stderr=%q, want diagnostic exit 1", code, stdout, stderr)
	}
	if _, err := os.Stat(missingDatabase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing list-runs created history: %v", err)
	}
	t.Log("CNMON_NATIVE_COMMAND_HISTORY_MANAGEMENT selection=zero-run-success ambiguous-prefix=exit2-no-mutation")
}

func seedNativeManagementSibling(t *testing.T, database string, request target.Request, port int) string {
	t.Helper()
	store, err := history.OpenWriter(context.Background(), database)
	if err != nil {
		t.Fatalf("open history to seed sibling run: %v", err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	hostname, err := os.Hostname()
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	const runID = "ended-sibling"
	_, err = store.BeginRun(context.Background(), history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports}, Hosts: request.Hosts,
	}, history.RunStart{ID: runID, Started: started, Host: hostname, PID: int64(os.Getpid()), Heartbeat: started})
	if err != nil {
		_ = store.Close()
		t.Fatalf("seed sibling request run: %v", err)
	}
	_, err = store.RecordRound(context.Background(), runID, history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: request.Hosts[0], Result: "OK", Time: started, TCP: map[int]string{port: "open"},
	}}})
	if err != nil {
		_ = store.Close()
		t.Fatalf("seed sibling request result: %v", err)
	}
	if err := store.EndRun(context.Background(), runID, started.Add(time.Second), "finished"); err != nil {
		_ = store.Close()
		t.Fatalf("finish sibling request run: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close sibling history: %v", err)
	}
	return runID
}

func waitNativeManagementRun(t *testing.T, database, requestKey string, limit time.Duration, ready func(history.RunInfo) bool) (history.RunInfo, error) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var latest history.RunInfo
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		store, err := history.OpenReader(ctx, database)
		if err == nil && store != nil {
			err = store.View(ctx, func(snapshot *history.Snapshot) error {
				request, err := snapshot.Request(requestKey)
				if err != nil || request == nil {
					return err
				}
				page, err := snapshot.Runs(request.ID, false, nil, 10)
				if err != nil {
					return err
				}
				for _, run := range page.Items {
					latest = run
					if ready(run) {
						latest = run
						return nil
					}
				}
				return nil
			})
			closeErr := store.Close()
			if err == nil {
				err = closeErr
			}
		}
		cancel()
		if err == nil && ready(latest) {
			return latest, nil
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-time.After(time.Until(deadline)):
		}
	}
	return latest, errors.New("native management writer did not reach requested history state")
}

func runNativeHistoryManagementCommand(t *testing.T, binary, directory, database, logPath string, args ...string) (int, []byte, []byte) {
	t.Helper()
	args = append(args, "--history-file", database, "--log-file", logPath)
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = directory
	command.Env = nativeCommandEnv(t, directory, database, logPath)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("native management command timed out: %v", ctx.Err())
	}
	return childExitCode(err), stdout.Bytes(), stderr.Bytes()
}

func seedNativeManagementRun(t *testing.T, database, host, runID, owner string, pid int64, status string) (target.Request, string) {
	t.Helper()
	request, rejected, err := target.Build([]string{host}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build native management request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}
	store, err := history.OpenWriter(context.Background(), database)
	if err != nil {
		t.Fatalf("open native management history: %v", err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	if _, err := store.BeginRun(context.Background(), history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports}, Hosts: request.Hosts,
	}, history.RunStart{ID: runID, Started: started, Host: owner, PID: pid, Heartbeat: started}); err != nil {
		_ = store.Close()
		t.Fatalf("begin native management run: %v", err)
	}
	if _, err := store.RecordRound(context.Background(), runID, history.RoundInput{Started: started, Observations: "!", Rows: []history.ResultRow{{
		Host: request.Hosts[0], Address: host, Result: "OK", Time: started, TCP: map[int]string{},
	}}}); err != nil {
		_ = store.Close()
		t.Fatalf("record native management round: %v", err)
	}
	if status != "running" {
		if err := store.EndRun(context.Background(), runID, started.Add(time.Second), status); err != nil {
			_ = store.Close()
			t.Fatalf("end native management run: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close native management history: %v", err)
	}
	return request, runID
}

func assertNativeManagementDeleteReport(t *testing.T, data []byte, want history.DeleteResult) {
	t.Helper()
	var report struct {
		Committed bool `json:"committed"`
		Deleted   struct {
			Requests int64 `json:"requests"`
			Runs     int64 `json:"runs"`
			Rounds   int64 `json:"rounds"`
			Results  int64 `json:"results"`
		} `json:"deleted"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode native management deletion report: %v data=%q", err, data)
	}
	if report.Committed != want.Committed || report.Deleted.Requests != want.Requests || report.Deleted.Runs != want.Runs || report.Deleted.Rounds != want.Rounds || report.Deleted.Results != want.Results {
		t.Fatalf("native deletion report=%+v want=%+v", report, want)
	}
}

func assertNativeManagementHistoryEmpty(t *testing.T, database string) {
	t.Helper()
	store, err := history.OpenReader(context.Background(), database)
	if err != nil || store == nil {
		t.Fatalf("open cleared history: store=%v err=%v", store, err)
	}
	defer store.Close()
	if err := store.View(context.Background(), func(snapshot *history.Snapshot) error {
		page, err := snapshot.Requests(nil, 10)
		if err != nil {
			return err
		}
		if len(page.Items) != 0 || page.Next != nil {
			return errors.New("clear-history retained request rows")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify cleared history: %v", err)
	}
}

func assertNativeManagementHistoryHasRuns(t *testing.T, database, requestKey string, wantRuns int) {
	t.Helper()
	store, err := history.OpenReader(context.Background(), database)
	if err != nil || store == nil {
		t.Fatalf("open protected history: store=%v err=%v", store, err)
	}
	defer store.Close()
	if err := store.View(context.Background(), func(snapshot *history.Snapshot) error {
		request, err := snapshot.Request(requestKey)
		if err != nil {
			return err
		}
		if request == nil || request.Runs != int64(wantRuns) {
			return errors.New("protected history was changed")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify protected history: %v", err)
	}
}

func startNativeManagementExitedChild(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeHistoryManagementPIDHelper$")
	command.Env = append(envWithout(os.Environ(), "CNMON_NATIVE_HISTORY_PID_HELPER"), "CNMON_NATIVE_HISTORY_PID_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatalf("start private PID-state child: %v", err)
	}
	pid := command.Process.Pid
	if err := command.Wait(); err != nil {
		t.Fatalf("wait for private PID-state child: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("private PID-state child timed out: %v", ctx.Err())
	}
	if state := history.LocalPIDState(int64(pid)); state != history.PIDDead {
		t.Fatalf("exited private child PID %d state=%v, want dead", pid, state)
	}
	return pid
}
