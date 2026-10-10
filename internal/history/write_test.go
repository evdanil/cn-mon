package history

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBeginRunReturnsDatabaseIDAndPreservesExistingRequest(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	request := writeTestRequest(strings.Repeat("a", 64))
	request.ID = 987654
	started := writeTestTime(1)
	firstID, err := store.BeginRun(ctx, request, writeTestRun("write-first", started))
	if err != nil {
		t.Fatalf("BeginRun first: %v", err)
	}
	if firstID <= 0 || firstID == request.ID {
		t.Fatalf("BeginRun returned request ID %d; expected assigned positive database ID distinct from input ID %d", firstID, request.ID)
	}

	second := request
	second.ID = firstID + 1000
	secondID, err := store.BeginRun(ctx, second, writeTestRun("write-second", started.Add(time.Minute)))
	if err != nil {
		t.Fatalf("BeginRun matching existing request: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("matching request IDs = %d and %d", firstID, secondID)
	}

	got := writeTestReadRequest(t, store, request.Key)
	if got == nil || got.ID != firstID || !reflect.DeepEqual(got.Targets, request.Targets) || !reflect.DeepEqual(got.TCPPorts, request.TCPPorts) || !reflect.DeepEqual(got.Hosts, request.Hosts) {
		t.Fatalf("stored request was not returned unchanged: %+v", got)
	}
}

func TestBeginRunRejectsConflictingExistingMetadataWithoutAddingRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Request)
	}{
		{"targets", func(r *Request) { r.Targets = []string{"other.example"} }},
		{"ports", func(r *Request) { r.TCPPorts = []int{443} }},
		{"hosts", func(r *Request) { r.Hosts = []string{"other.example"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeTestOpen(t)
			ctx := context.Background()
			request := writeTestRequest(strings.Repeat("b", 64))
			id, err := store.BeginRun(ctx, request, writeTestRun("write-original", writeTestTime(2)))
			if err != nil {
				t.Fatalf("BeginRun original: %v", err)
			}
			conflict := request
			conflict.Targets = append([]string(nil), request.Targets...)
			conflict.TCPPorts = append([]int(nil), request.TCPPorts...)
			conflict.Hosts = append([]string(nil), request.Hosts...)
			tc.mutate(&conflict)
			if _, err := store.BeginRun(ctx, conflict, writeTestRun("write-conflict", writeTestTime(3))); err == nil {
				t.Fatal("BeginRun accepted conflicting request metadata")
			}
			got := writeTestRequestAndRuns(t, store, request.Key)
			if got.request == nil || got.request.ID != id || len(got.runs) != 1 || got.runs[0].ID != "write-original" {
				t.Fatalf("conflicting BeginRun changed request or runs: %+v", got)
			}
		})
	}
}

func TestBeginRunRollsBackNewRequestWhenRunInsertFails(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	if _, err := store.BeginRun(ctx, writeTestRequest(strings.Repeat("c", 64)), writeTestRun("duplicate-run", writeTestTime(4))); err != nil {
		t.Fatalf("BeginRun seed: %v", err)
	}
	newRequest := writeTestRequest(strings.Repeat("d", 64))
	if _, err := store.BeginRun(ctx, newRequest, writeTestRun("duplicate-run", writeTestTime(5))); err == nil {
		t.Fatal("BeginRun with duplicate run ID unexpectedly succeeded")
	}
	if got := writeTestReadRequest(t, store, newRequest.Key); got != nil {
		t.Fatalf("request insert survived failed run insert: %+v", got)
	}
}

func TestRecordRoundCommitAndRollbackAreAtomic(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	request := writeTestRequest(strings.Repeat("e", 64))
	requestID, err := store.BeginRun(ctx, request, writeTestRun("write-rounds", writeTestTime(6)))
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	started := writeTestTime(7)
	rows := []ResultRow{
		{Host: "host-b", Address: "192.0.2.2", Result: "custom response", Time: started, TCP: map[int]string{}},
		{Host: "host-a", Address: "192.0.2.1", Result: "!", Time: started, TCP: nil},
	}
	roundID, err := store.RecordRound(ctx, "write-rounds", RoundInput{Started: started, Observations: "!!", Rows: rows})
	if err != nil {
		t.Fatalf("RecordRound: %v", err)
	}
	stored := writeTestReadRounds(t, store, requestID)
	if roundID <= 0 || len(stored) != 1 || stored[0].ID != roundID || stored[0].Answered != 2 || len(stored[0].Rows) != 2 {
		t.Fatalf("committed round = %+v", stored)
	}
	if stored[0].Rows[0].Host != "host-a" || stored[0].Rows[1].Host != "host-b" || stored[0].Rows[0].TCP != nil || stored[0].Rows[1].TCP == nil || len(stored[0].Rows[1].TCP) != 0 {
		t.Fatalf("stored row ordering or nil/empty TCP changed: %+v", stored[0].Rows)
	}

	// This trigger is installed only after schema validation and fails the
	// second detail insert, proving the round header and first detail roll back.
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER write_test_reject_second BEFORE INSERT ON result WHEN NEW.host = 'host-b' BEGIN SELECT RAISE(ABORT, 'injected row constraint'); END`); err != nil {
		t.Fatalf("install test-only constraint trigger: %v", err)
	}
	failed := RoundInput{
		Started:      started.Add(time.Minute),
		Observations: "!!",
		Rows: []ResultRow{
			{Host: "host-a", Address: "192.0.2.1", Result: "!", Time: started, TCP: nil},
			{Host: "host-b", Address: "192.0.2.2", Result: "!", Time: started, TCP: nil},
		},
	}
	if _, err := store.RecordRound(ctx, "write-rounds", failed); err == nil {
		t.Fatal("RecordRound with real SQLite mid-detail constraint failure succeeded")
	}
	stored = writeTestReadRounds(t, store, requestID)
	if len(stored) != 1 || stored[0].Answered != 2 {
		t.Fatalf("failed round or partial detail rows became visible: %+v", stored)
	}
}

func TestRecordRoundRejectsMissingRunAndInvalidObservationShape(t *testing.T) {
	store := writeTestOpen(t)
	started := writeTestTime(8)
	if _, err := store.RecordRound(context.Background(), "missing-run", RoundInput{Started: started, Observations: "?", Rows: nil}); err == nil {
		t.Fatal("RecordRound accepted missing run")
	}
	request := writeTestRequest(strings.Repeat("f", 64))
	id, err := store.BeginRun(context.Background(), request, writeTestRun("write-invalid-round", started))
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	_, err = store.RecordRound(context.Background(), "write-invalid-round", RoundInput{Started: started, Observations: "!", Rows: nil})
	if err == nil {
		t.Fatal("RecordRound accepted seen observation without exactly one detail row")
	}
	if got := writeTestReadRounds(t, store, id); len(got) != 0 {
		t.Fatalf("invalid round persisted: %+v", got)
	}
}

func TestWriteMethodsPreserveContextCancellationAndLeaveNoPartialData(t *testing.T) {
	store := writeTestOpen(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request := writeTestRequest(strings.Repeat("0", 64))
	if _, err := store.BeginRun(canceled, request, writeTestRun("write-canceled-begin", writeTestTime(8))); !errors.Is(err, context.Canceled) {
		t.Fatalf("BeginRun error=%v, want errors.Is(context.Canceled)", err)
	}
	if got := writeTestReadRequest(t, store, request.Key); got != nil {
		t.Fatalf("canceled BeginRun left a request behind: %+v", got)
	}

	request = writeTestRequest(strings.Repeat("9", 64))
	requestID, err := store.BeginRun(context.Background(), request, writeTestRun("write-canceled-round", writeTestTime(9)))
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	started := writeTestTime(10)
	round := RoundInput{
		Started:      started,
		Observations: "!!",
		Rows: []ResultRow{
			{Host: "host-a", Address: "192.0.2.1", Result: "!", Time: started},
			{Host: "host-b", Address: "192.0.2.2", Result: "!", Time: started},
		},
	}
	if _, err := store.RecordRound(ctx, "write-canceled-round", round); !errors.Is(err, context.Canceled) {
		t.Fatalf("RecordRound error=%v, want errors.Is(context.Canceled)", err)
	}
	if got := writeTestReadRounds(t, store, requestID); len(got) != 0 {
		t.Fatalf("canceled RecordRound left history behind: %+v", got)
	}
}

func TestHeartbeatAndEndRunRequireExistingRunAndRetainNilDuration(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	missingErr := store.Heartbeat(ctx, "absent", writeTestTime(10), nil)
	if missingErr == nil {
		t.Fatal("Heartbeat silently accepted missing run")
	}
	if err := store.EndRun(ctx, "absent", writeTestTime(10), "finished"); err == nil {
		t.Fatal("EndRun silently accepted missing run")
	}
	request := writeTestRequest(strings.Repeat("1", 64))
	requestID, err := store.BeginRun(ctx, request, writeTestRun("write-lifecycle", writeTestTime(9)))
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	elapsed := 17 * time.Second
	if err := store.Heartbeat(ctx, "write-lifecycle", writeTestTime(10), &elapsed); err != nil {
		t.Fatalf("Heartbeat duration: %v", err)
	}
	if err := store.Heartbeat(ctx, "write-lifecycle", writeTestTime(11), nil); err != nil {
		t.Fatalf("Heartbeat preserve duration: %v", err)
	}
	runs := writeTestReadRuns(t, store, requestID)
	if len(runs) != 1 || runs[0].Heartbeat != writeTestTime(11) || runs[0].RoundSeconds == nil || *runs[0].RoundSeconds != elapsed {
		t.Fatalf("heartbeat did not retain duration: %+v", runs)
	}
	zero := time.Duration(0)
	if err := store.Heartbeat(ctx, "write-lifecycle", writeTestTime(12), &zero); err != nil {
		t.Fatalf("Heartbeat zero duration: %v", err)
	}
	runs = writeTestReadRuns(t, store, requestID)
	if runs[0].RoundSeconds == nil || *runs[0].RoundSeconds != 0 {
		t.Fatalf("explicit zero duration was stored as NULL or nonzero: %+v", runs[0].RoundSeconds)
	}
	if err := store.EndRun(ctx, "write-lifecycle", writeTestTime(13), "failed"); err != nil {
		t.Fatalf("EndRun failed: %v", err)
	}
	runs = writeTestReadRuns(t, store, requestID)
	if runs[0].Status != "failed" || runs[0].Ended == nil || !runs[0].Ended.Equal(writeTestTime(13)) {
		t.Fatalf("EndRun fields = %+v", runs[0])
	}
	if err := store.EndRun(ctx, "write-lifecycle", writeTestTime(14), "running"); err == nil {
		t.Fatal("EndRun accepted non-final status")
	}
	if err := store.EndRun(ctx, "write-lifecycle", writeTestTime(14), "stopped"); err != nil {
		t.Fatalf("EndRun should preserve source's lack of lifecycle state machine: %v", err)
	}
}

func writeTestOpen(t *testing.T) *Store {
	t.Helper()
	store, err := OpenWriter(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func writeTestRequest(key string) Request {
	return Request{
		RequestMeta: RequestMeta{Key: key, Targets: []string{"host-a.example"}, TCPPorts: []int{80}},
		Hosts:       []string{"host-a", "host-b"},
	}
}

func writeTestRun(id string, started time.Time) RunStart {
	return RunStart{ID: id, Started: started, Host: "writer-test-host", PID: 1, Heartbeat: started}
}

func writeTestTime(seconds int) time.Time {
	return time.Unix(1_800_000_000+int64(seconds), 0).UTC()
}

func writeTestReadRequest(t *testing.T, store *Store, key string) *RequestInfo {
	t.Helper()
	var id int64
	var storedKey, targets, ports, hosts string
	err := store.db.QueryRowContext(context.Background(), "SELECT id, key, targets, tcp_ports, hosts FROM request WHERE key = ?", key).Scan(&id, &storedKey, &targets, &ports, &hosts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("read request row: %v", err)
	}
	request, err := decodeRequest(id, storedKey, targets, ports, hosts)
	if err != nil {
		t.Fatalf("decode request row: %v", err)
	}
	return &RequestInfo{RequestSummary: RequestSummary{RequestMeta: request.RequestMeta}, Hosts: request.Hosts}
}

type writeTestState struct {
	request *RequestInfo
	runs    []RunInfo
}

func writeTestRequestAndRuns(t *testing.T, store *Store, key string) writeTestState {
	t.Helper()
	result := writeTestState{request: writeTestReadRequest(t, store, key)}
	if result.request != nil {
		result.runs = writeTestReadRuns(t, store, result.request.ID)
	}
	return result
}

func writeTestReadRuns(t *testing.T, store *Store, requestID int64) []RunInfo {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT id, request_id, source, started, interval, until, host, pid, heartbeat, round_seconds, ended, status FROM run WHERE request_id = ? ORDER BY started DESC, rowid DESC`, requestID)
	if err != nil {
		t.Fatalf("query runs: %v", err)
	}
	defer rows.Close()
	var runs []RunInfo
	for rows.Next() {
		var run RunInfo
		var source sql.NullString
		var started, heartbeat float64
		var interval, until, roundSeconds, ended sql.NullFloat64
		if err := rows.Scan(&run.ID, &run.RequestID, &source, &started, &interval, &until, &run.Host, &run.PID, &heartbeat, &roundSeconds, &ended, &run.Status); err != nil {
			t.Fatalf("scan run: %v", err)
		}
		var err error
		if run.Started, err = timeFromEpoch(started); err != nil {
			t.Fatalf("decode run start: %v", err)
		}
		if run.Heartbeat, err = timeFromEpoch(heartbeat); err != nil {
			t.Fatalf("decode run heartbeat: %v", err)
		}
		if source.Valid {
			run.Source = &source.String
		}
		if interval.Valid {
			d, err := durationFromSeconds(interval.Float64)
			if err != nil {
				t.Fatalf("decode run interval: %v", err)
			}
			run.Interval = &d
		}
		if until.Valid {
			tm, err := timeFromEpoch(until.Float64)
			if err != nil {
				t.Fatalf("decode run until: %v", err)
			}
			run.Until = &tm
		}
		if roundSeconds.Valid {
			d, err := durationFromSeconds(roundSeconds.Float64)
			if err != nil {
				t.Fatalf("decode round seconds: %v", err)
			}
			run.RoundSeconds = &d
		}
		if ended.Valid {
			tm, err := timeFromEpoch(ended.Float64)
			if err != nil {
				t.Fatalf("decode run end: %v", err)
			}
			run.Ended = &tm
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read runs: %v", err)
	}
	return runs
}

func writeTestReadRounds(t *testing.T, store *Store, requestID int64) []StoredRound {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT round.id, round.run_id, round.started, round.observations, round.answered, run.interval FROM round JOIN run ON run.id = round.run_id WHERE run.request_id = ? ORDER BY round.started, round.id`, requestID)
	if err != nil {
		t.Fatalf("query rounds: %v", err)
	}
	type header struct {
		id       int64
		runID    string
		started  float64
		observed string
		answered int64
		interval sql.NullFloat64
	}
	var headers []header
	for rows.Next() {
		var item header
		if err := rows.Scan(&item.id, &item.runID, &item.started, &item.observed, &item.answered, &item.interval); err != nil {
			rows.Close()
			t.Fatalf("scan round: %v", err)
		}
		headers = append(headers, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("read rounds: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close round rows: %v", err)
	}
	var rounds []StoredRound
	for _, item := range headers {
		started, err := timeFromEpoch(item.started)
		if err != nil {
			t.Fatalf("decode round start: %v", err)
		}
		round := StoredRound{RoundInput: RoundInput{Started: started, Observations: item.observed}, ID: item.id, RunID: item.runID, Answered: item.answered}
		if item.interval.Valid {
			d, err := durationFromSeconds(item.interval.Float64)
			if err != nil {
				t.Fatalf("decode round interval: %v", err)
			}
			round.Interval = &d
		}
		rows, err := store.db.QueryContext(context.Background(), "SELECT host, address, t, result, tcp FROM result WHERE round_id = ? ORDER BY host", item.id)
		if err != nil {
			t.Fatalf("query result rows: %v", err)
		}
		for rows.Next() {
			var row ResultRow
			var at float64
			var tcp sql.NullString
			if err := rows.Scan(&row.Host, &row.Address, &at, &row.Result, &tcp); err != nil {
				rows.Close()
				t.Fatalf("scan result row: %v", err)
			}
			if row.Time, err = timeFromEpoch(at); err != nil {
				rows.Close()
				t.Fatalf("decode result time: %v", err)
			}
			if row.TCP, err = decodeTCP(tcp); err != nil {
				rows.Close()
				t.Fatalf("decode result TCP: %v", err)
			}
			round.Rows = append(round.Rows, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("read result rows: %v", err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close result rows: %v", err)
		}
		rounds = append(rounds, round)
	}
	return rounds
}
