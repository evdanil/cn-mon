package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestOpenExistingWriterMissingDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "history.db")
	store, err := OpenExistingWriter(context.Background(), path)
	if err != nil || store != nil {
		t.Fatalf("OpenExistingWriter = %v, %v", store, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent created: %v", err)
	}
}

func TestDeleteRunExactCascadeAndOnlyOrphanRequest(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	started := writeTestTime(20)
	request1 := writeTestRequest(strings.Repeat("a", 64))
	request2 := writeTestRequest(strings.Repeat("b", 64))
	if _, err := store.BeginRun(ctx, request1, writeTestRun("Case/α", started)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRun(ctx, request1, writeTestRun("keep", started.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRun(ctx, request2, writeTestRun("case/α", started)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"Case/α", "keep", "case/α"} {
		if _, err := store.RecordRound(ctx, id, RoundInput{Started: started, Observations: "??", Rows: nil}); err != nil {
			t.Fatalf("RecordRound %q: %v", id, err)
		}
	}
	result, err := store.DeleteRun(ctx, "Case/α", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Requests != 0 || result.Runs != 1 || result.Rounds != 1 || result.Results != 0 {
		t.Fatalf("delete counts: %+v", result)
	}
	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run WHERE id='Case/α'`).Scan(&got); err != nil || got != 0 {
		t.Fatalf("exact run remains/count err: %d %v", got, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run WHERE id='case/α'`).Scan(&got); err != nil || got != 1 {
		t.Fatalf("case-distinct run changed/count err: %d %v", got, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM request`).Scan(&got); err != nil || got != 2 {
		t.Fatalf("requests=%d err=%v", got, err)
	}
}

func TestDeleteRequestPrefixAndProtectedAllOrNothing(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	started := writeTestTime(30)
	key1, key2 := strings.Repeat("a", 64), strings.Repeat("a", 4)+strings.Repeat("b", 60)
	if _, err := store.BeginRun(ctx, writeTestRequest(key1), writeTestRun("dead", started)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRun(ctx, writeTestRequest(key2), writeTestRun("alive", started)); err != nil {
		t.Fatal(err)
	}
	guard := DeleteGuard{Hostname: "local", PIDState: func(pid int64) PIDState {
		if pid == 999 {
			return PIDDead
		}
		return PIDAlive
	}}
	if _, err := store.DeleteRequest(ctx, "AAAA", guard); !errors.Is(err, ErrAmbiguousRequest) {
		t.Fatalf("ambiguous prefix error=%v", err)
	}
	if _, err := store.DeleteRequest(ctx, key1, guard); !errors.Is(err, ErrProtectedRun) {
		t.Fatalf("alive request error=%v", err)
	}
	var runs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run`).Scan(&runs); err != nil || runs != 2 {
		t.Fatalf("partial deletion: runs=%d err=%v", runs, err)
	}
}

func TestDeleteRequestExactPrefixDeletesWholeGroupAndReportsCascades(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	at := writeTestTime(35)
	key := strings.Repeat("e", 64)
	for _, id := range []string{"group-one", "group-two"} {
		if _, err := store.BeginRun(ctx, writeTestRequest(key), writeTestRun(id, at)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordRound(ctx, id, RoundInput{Started: at, Observations: "??", Rows: nil}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.DeleteRequest(ctx, strings.ToUpper(key), DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Requests != 1 || result.Runs != 2 || result.Rounds != 2 || result.Results != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDeleteRequestGuardChecksEveryRunBeforeMutation(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	at := writeTestTime(36)
	key := strings.Repeat("f", 64)
	for _, id := range []string{"safe-dead", "protected-alive"} {
		if _, err := store.BeginRun(ctx, writeTestRequest(key), writeTestRun(id, at)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`UPDATE run SET pid=CASE id WHEN 'safe-dead' THEN 999 ELSE 1000 END WHERE request_id=(SELECT id FROM request WHERE key=?)`, key); err != nil {
		t.Fatal(err)
	}
	result, err := store.DeleteRequest(ctx, key, DeleteGuard{Hostname: "writer-test-host", PIDState: func(pid int64) PIDState {
		if pid == 999 {
			return PIDDead
		}
		return PIDAlive
	}})
	if !errors.Is(err, ErrProtectedRun) || result.Committed {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("runs=%d err=%v", count, err)
	}
}

func TestDeleteRejectsMalformedRunStateAndFinalizedEndTime(t *testing.T) {
	for _, tc := range []struct{ name, update string }{
		{"unknown status", `UPDATE run SET status='mystery',ended=1 WHERE id='bad'`},
		{"missing finalized end", `UPDATE run SET status='finished',ended=NULL WHERE id='bad'`},
		{"text ended time", `UPDATE run SET status='finished',ended='yesterday' WHERE id='bad'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeTestOpen(t)
			defer store.Close()
			ctx := context.Background()
			at := writeTestTime(37)
			if _, err := store.BeginRun(ctx, writeTestRequest(strings.Repeat("1", 64)), writeTestRun("bad", at)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(tc.update); err != nil {
				t.Fatal(err)
			}
			if result, err := store.DeleteRun(ctx, "bad", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }}); err == nil || result.Committed {
				t.Fatalf("malformed run deleted: %+v %v", result, err)
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM run`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("run count=%d err=%v", count, err)
			}
		})
	}
}

func TestDeleteRejectsRunningRunWithPersistedEndTimeAsMalformed(t *testing.T) {
	store := writeTestOpen(t)
	if _, err := store.BeginRun(context.Background(), writeTestRequest(strings.Repeat("3", 64)), writeTestRun("running-ended", writeTestTime(38))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE run SET ended=1 WHERE id='running-ended'`); err != nil {
		t.Fatal(err)
	}
	result, err := store.DeleteRun(context.Background(), "running-ended", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if err == nil || result.Committed || errors.Is(err, ErrProtectedRun) {
		t.Fatalf("inconsistent running/end state classification: result=%+v err=%v", result, err)
	}
}

func TestClearEmptyHistoryCommitsZeroAndRetainsMeta(t *testing.T) {
	store := writeTestOpen(t)
	result, err := store.ClearHistory(context.Background(), DeleteGuard{})
	if err != nil || !result.Committed || result.Requests+result.Runs+result.Rounds+result.Results != 0 {
		t.Fatalf("clear=%+v err=%v", result, err)
	}
	var version string
	if err := store.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("meta version=%q err=%v", version, err)
	}
}

func TestClearCountsOrphanRoundsAndResults(t *testing.T) {
	store := writeTestOpen(t)
	if _, err := store.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO round(run_id,started,observations,answered) VALUES('absent-run',1,'',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO result(round_id,host,address,t,result) VALUES(999,'h','a',1,'OK')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	result, err := store.ClearHistory(context.Background(), DeleteGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rounds != 1 || result.Results != 1 {
		t.Fatalf("removed orphan round/result but reported %+v", result)
	}
}

func TestDeleteRunRejectsMalformedCanonicalParentKey(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	if _, err := store.BeginRun(ctx, writeTestRequest(strings.Repeat("a", 64)), writeTestRun("parent-key", writeTestTime(60))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE request SET key='bad'`); err != nil {
		t.Fatal(err)
	}
	result, err := store.DeleteRun(ctx, "parent-key", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if err == nil || result.Committed {
		t.Fatalf("deleted run with malformed parent identity: result=%+v err=%v", result, err)
	}
}

func TestDeleteRunPreservesExistingReaderSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot-delete.db")
	writer, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := writer.BeginRun(context.Background(), writeTestRequest(strings.Repeat("2", 64)), writeTestRun("snapshot-run", writeTestTime(61)))
	if err != nil {
		t.Fatal(err)
	}
	deleter, err := OpenExistingWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer deleter.Close()
	reader, err := OpenReader(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	err = reader.View(context.Background(), func(snapshot *Snapshot) error {
		before, err := snapshot.Runs(requestID, false, nil, 10)
		if err != nil {
			return err
		}
		if len(before.Items) != 1 {
			return errors.New("run missing before deletion")
		}
		result, err := deleter.DeleteRun(context.Background(), "snapshot-run", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
		if err != nil {
			return err
		}
		if !result.Committed {
			return errors.New("delete did not commit")
		}
		during, err := snapshot.Runs(requestID, false, nil, 10)
		if err != nil {
			return err
		}
		if len(during.Items) != 1 {
			return errors.New("active reader snapshot changed across deletion")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.View(context.Background(), func(snapshot *Snapshot) error {
		page, err := snapshot.Runs(requestID, false, nil, 10)
		if err == nil && len(page.Items) != 0 {
			return errors.New("new reader view retained deleted run")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRunningRowsRequireLocalConfirmedDeadValidPID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		host      any
		pid       any
		state     PIDState
		wantErr   bool
		protected bool
	}{
		{"confirmed dead", "local", int64(2147483647), PIDDead, false, false},
		{"alive despite stale heartbeat", "local", int64(100), PIDAlive, true, true},
		{"unknown", "local", int64(100), PIDUnknown, true, true},
		{"foreign", "other", int64(100), PIDDead, true, true},
		{"nonpositive pid", "local", int64(0), PIDDead, true, true},
		{"text pid", "local", "not-a-number", PIDDead, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeTestOpen(t)
			defer store.Close()
			ctx := context.Background()
			at := writeTestTime(40)
			if _, err := store.BeginRun(ctx, writeTestRequest(strings.Repeat("c", 64)), writeTestRun("guarded", at)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE run SET host=?, pid=?, heartbeat=? WHERE id='guarded'`, tc.host, tc.pid, at.Add(-24*time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			result, err := store.DeleteRun(ctx, "guarded", DeleteGuard{Hostname: "local", PIDState: func(int64) PIDState { return tc.state }})
			if tc.wantErr {
				if tc.protected && !errors.Is(err, ErrProtectedRun) {
					t.Fatalf("error=%v", err)
				}
				if err == nil {
					t.Fatal("malformed or protected run was deleted")
				}
				if result.Committed {
					t.Fatalf("protected delete committed: %+v", result)
				}
			} else if err != nil || !result.Committed {
				t.Fatalf("dead local deletion: %+v %v", result, err)
			}
		})
	}
}

func TestClearHistoryPreservesSchemaAndSequences(t *testing.T) {
	store := writeTestOpen(t)
	ctx := context.Background()
	at := writeTestTime(50)
	if _, err := store.BeginRun(ctx, writeTestRequest(strings.Repeat("d", 64)), writeTestRun("clear", at)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordRound(ctx, "clear", RoundInput{Started: at, Observations: "??", Rows: nil}); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := store.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='round'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	result, err := store.ClearHistory(ctx, DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if err != nil || !result.Committed || result.Requests != 1 || result.Runs != 1 || result.Rounds != 1 {
		t.Fatalf("clear=%+v err=%v", result, err)
	}
	var tables int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM meta)+(SELECT COUNT(*) FROM request)+(SELECT COUNT(*) FROM run)+(SELECT COUNT(*) FROM round)+(SELECT COUNT(*) FROM result)`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("remaining rows=%d err=%v", tables, err)
	}
	var after int64
	if err := store.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='round'`).Scan(&after); err != nil || after != seq {
		t.Fatalf("round sequence before=%d after=%d err=%v", seq, after, err)
	}
	var schema int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('meta','request','run','round','result')`).Scan(&schema); err != nil || schema != 5 {
		t.Fatalf("schema objects=%d err=%v", schema, err)
	}
	if _, err := store.BeginRun(ctx, writeTestRequest(strings.Repeat("d", 64)), writeTestRun("after-clear", at.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	newRound, err := store.RecordRound(ctx, "after-clear", RoundInput{Started: at.Add(time.Minute), Observations: "??", Rows: nil})
	if err != nil || newRound <= seq {
		t.Fatalf("round sequence was recreated: old=%d new=%d err=%v", seq, newRound, err)
	}
}

func TestDeleteMissingAndUnsupportedExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenExistingWriter(context.Background(), path); err == nil {
		if store != nil {
			store.Close()
		}
		t.Fatal("accepted empty unsupported database")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database changed/disappeared: %v", err)
	}

	store := writeTestOpen(t)
	defer store.Close()
	_, err := store.DeleteRun(context.Background(), "missing", DeleteGuard{Hostname: "local", PIDState: func(int64) PIDState { return PIDDead }})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run error=%v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM request`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("missing delete side effect count=%d err=%v", count, err)
	}
}

func TestOpenExistingWriterDoesNotChangeJournalMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	seed, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenExistingWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("OpenExistingWriter returned nil for existing database")
	}
	defer store.Close()
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("journal mode=%q err=%v", mode, err)
	}
}
