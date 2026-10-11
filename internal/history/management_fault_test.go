package history

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeleteRunRetainsCommittedCountsAfterRealConnectionCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-failure.db")
	seed, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	if _, err := seed.BeginRun(context.Background(), writeTestRequest("abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"), writeTestRun("close-after-commit", writeTestTime(80))); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenExistingWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	closeErr := errors.New("injected connection cleanup failure")
	store.closeConnection = func(conn *sql.Conn) error { _ = conn.Close(); return closeErr }
	result, err := store.DeleteRun(context.Background(), "close-after-commit", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if !errors.Is(err, closeErr) || !result.Committed || result.Runs != 1 || result.Requests != 1 {
		t.Fatalf("committed deletion result=%+v err=%v", result, err)
	}
	reader, err := OpenReader(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err := reader.db.QueryRow(`SELECT COUNT(*) FROM run WHERE id='close-after-commit'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("committed row count=%d err=%v", count, err)
	}
}

func TestDeleteRunCancellationWaitingForWriterLockLeavesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	seed, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	if _, err := seed.BeginRun(context.Background(), writeTestRequest("bcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcde"), writeTestRun("locked-run", writeTestTime(90))); err != nil {
		t.Fatal(err)
	}
	lockConn, err := seed.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanupLockConn := registerLockedConnectionCleanup(t, lockConn)
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	other, err := OpenExistingWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	busyConn, err := other.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanupBusyConn := registerConnectionCleanup(t, busyConn)
	if _, err := busyConn.ExecContext(context.Background(), `PRAGMA busy_timeout=200`); err != nil {
		t.Fatal(err)
	}
	if err := busyConn.Close(); err != nil {
		t.Fatal(err)
	}
	cleanupBusyConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type deleteOutcome struct {
		result DeleteResult
		err    error
	}
	done := make(chan deleteOutcome, 1)
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Errorf("locked delete worker did not stop during cleanup")
			}
		}
	})
	go func() {
		result, err := other.DeleteRun(ctx, "locked-run", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
		done <- deleteOutcome{result, err}
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	var outcome deleteOutcome
	select {
	case outcome = <-done:
		joined = true
	case <-time.After(2 * time.Second):
		t.Fatal("delete remained blocked after context cancellation")
	}
	if !errors.Is(outcome.err, context.Canceled) || outcome.result.Committed {
		t.Fatalf("locked delete result=%+v err=%v", outcome.result, outcome.err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := lockConn.Close(); err != nil {
		t.Fatal(err)
	}
	cleanupLockConn()
	var count int
	if err := seed.db.QueryRow(`SELECT COUNT(*) FROM run WHERE id='locked-run'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("locked row count=%d err=%v", count, err)
	}
	result, err := other.DeleteRun(context.Background(), "locked-run", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
	if err != nil || !result.Committed {
		t.Fatalf("retry after unlock result=%+v err=%v", result, err)
	}
}

func TestDeleteRequestResolvesPrefixAfterWaitingForImmediateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefix-order.db")
	seed, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	if _, err := seed.BeginRun(context.Background(), writeTestRequest(strings.Repeat("a", 64)), writeTestRun("prefix-original", writeTestTime(91))); err != nil {
		t.Fatal(err)
	}
	lockConn, err := seed.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanupLockConn := registerLockedConnectionCleanup(t, lockConn)
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	deleter, err := OpenExistingWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deleter.Close() })
	busyConn, err := deleter.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanupBusyConn := registerConnectionCleanup(t, busyConn)
	if _, err := busyConn.ExecContext(context.Background(), `PRAGMA busy_timeout=1000`); err != nil {
		t.Fatal(err)
	}
	if err := busyConn.Close(); err != nil {
		t.Fatal(err)
	}
	cleanupBusyConn()
	type outcome struct {
		result DeleteResult
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Errorf("prefix delete worker did not stop during cleanup")
			}
		}
	})
	go func() {
		r, e := deleter.DeleteRequest(ctx, "aaaa", DeleteGuard{Hostname: "writer-test-host", PIDState: func(int64) PIDState { return PIDDead }})
		done <- outcome{r, e}
	}()
	time.Sleep(40 * time.Millisecond)
	if _, err := lockConn.ExecContext(context.Background(), `INSERT INTO request(key,targets,tcp_ports,hosts) VALUES(?,?,?,?)`, strings.Repeat("a", 4)+strings.Repeat("b", 60), "[]", "[]", "[]"); err != nil {
		t.Fatal(err)
	}
	if _, err := lockConn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := lockConn.Close(); err != nil {
		t.Fatal(err)
	}
	cleanupLockConn()
	select {
	case got := <-done:
		joined = true
		if !errors.Is(got.err, ErrAmbiguousRequest) || got.result.Committed {
			t.Fatalf("prefix resolution result=%+v err=%v", got.result, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request deletion remained blocked after lock commit")
	}
	var requests int
	if err := seed.db.QueryRow(`SELECT COUNT(*) FROM request`).Scan(&requests); err != nil || requests != 2 {
		t.Fatalf("requests after ambiguous resolution=%d err=%v", requests, err)
	}
}

func registerConnectionCleanup(t *testing.T, conn *sql.Conn) func() {
	t.Helper()
	open := true
	t.Cleanup(func() {
		if open {
			_ = conn.Close()
		}
	})
	return func() { open = false }
}

func registerLockedConnectionCleanup(t *testing.T, conn *sql.Conn) func() {
	t.Helper()
	open := true
	t.Cleanup(func() {
		if open {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			cancel()
			_ = conn.Close()
		}
	})
	return func() { open = false }
}
