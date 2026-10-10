package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestOpenTxCallbackFailureRollsBackAndPreservesCause(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "tx.db"))
	store := &Store{db: db, path: "tx.db", writable: true}
	sentinel := errors.New("callback failed")
	err := store.withTx(context.Background(), true, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), `CREATE TABLE sample(value TEXT)`); err != nil {
			return err
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO sample VALUES('partial')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("withTx lost callback error cause: %v", err)
	}
	var count int
	queryErr := db.QueryRow(`SELECT count(*) FROM sample`).Scan(&count)
	if queryErr == nil || count != 0 {
		t.Fatalf("failed callback became visible: count=%d err=%v", count, queryErr)
	}
}

func TestOpenTxCancellationDuringPoolAcquisitionReturnsPromptly(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "pool.db"))
	store := &Store{db: db, path: "pool.db", writable: true}
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = store.withTx(ctx, false, func(*sql.Conn) error { t.Error("canceled waiter unexpectedly acquired connection"); return nil })
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection acquisition lost context cause: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("connection acquisition cancellation took %s", elapsed)
	}
}

func TestOpenTxBusyCancellationNormalizesCauseAndStoreRemainsUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db := openTestSQLite(t, path)
	store := &Store{db: db, path: path, writable: true}
	lockDB := openTestSQLite(t, path)
	locker, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	started := time.Now()
	err = store.withTx(ctx, true, func(*sql.Conn) error { return nil })
	elapsed := time.Since(started)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("busy cancellation lost context cause: %v", err)
	}
	if !CancellationOnly(err, context.DeadlineExceeded) {
		t.Errorf("busy cancellation retained an unrelated error or lost cancellation provenance: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("busy cancellation exceeded prompt bound: %s", elapsed)
	}
	if _, err := locker.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := locker.Close(); err != nil {
		t.Fatal(err)
	}
	_ = lockDB.Close()
	if err := store.withTx(context.Background(), true, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS reusable(value INTEGER)`)
		return err
	}); err != nil {
		t.Fatalf("store did not recover after canceled lock wait: %v", err)
	}
}

func TestOpenTxCanceledReadInterruptsRowsAndRollsBack(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "read.db"))
	store := &Store{db: db, path: "read.db", writable: false}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := store.withTx(ctx, false, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n`)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		defer rows.Close()
		for rows.Next() {
			var total int64
			if err := rows.Scan(&total); err != nil {
				return normalizeDBError(ctx, err)
			}
		}
		return normalizeDBError(ctx, rows.Err())
	})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("read cancellation took %s", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read interruption lost context cause: %v", err)
	}
	if !CancellationOnly(err, context.DeadlineExceeded) {
		t.Fatalf("read interruption was not classified as cancellation only: %v", err)
	}
	if err := store.withTx(context.Background(), false, func(conn *sql.Conn) error {
		var result int
		return conn.QueryRowContext(context.Background(), `SELECT 1`).Scan(&result)
	}); err != nil {
		t.Fatalf("connection unusable after interrupted read rollback: %v", err)
	}
}

func TestOpenTxCanceledInsertInterruptsAndRollsBack(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "insert.db"))
	if _, err := db.Exec(`CREATE TABLE sample(value INTEGER)`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, path: "insert.db", writable: true}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := store.withTx(ctx, true, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) INSERT INTO sample SELECT x FROM n`)
		return normalizeDBError(ctx, err)
	})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("insert cancellation took %s", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interrupted insert lost context cause: %v", err)
	}
	if !CancellationOnly(err, context.DeadlineExceeded) {
		t.Fatalf("interrupted insert was not classified as cancellation only: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sample`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("interrupted insert leaked partial rows: count=%d err=%v", count, err)
	}
}

func TestOpenTxDoesNotRelabelObservedErrorAfterLateCancellation(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "late-cancel.db"))
	store := &Store{db: db, path: "late-cancel.db", writable: true}
	ctx, cancel := context.WithCancel(context.Background())
	busy := error(sqlite3.BUSY)
	err := store.withTx(ctx, false, func(*sql.Conn) error {
		cancel()
		return busy // Already observed before cancellation; withTx must not normalize late.
	})
	if errors.Is(err, context.Canceled) || !errors.Is(err, busy) {
		t.Fatalf("late context cancellation changed callback cause: %v", err)
	}
	if CancellationOnly(err, context.Canceled) {
		t.Fatalf("unrelated callback error was classified as cancellation only: %v", err)
	}
}

func TestOpenTxPanicRollsBackAndRethrowsOriginalValue(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "panic.db"))
	store := &Store{db: db, path: "panic.db", writable: true}
	if _, err := db.Exec(`CREATE TABLE sample(value INTEGER)`); err != nil {
		t.Fatal(err)
	}
	marker := &struct{ value string }{"original panic"}
	func() {
		defer func() {
			if got := recover(); got != marker {
				t.Fatalf("withTx panic=%#v, want original %#v", got, marker)
			}
		}()
		_ = store.withTx(context.Background(), true, func(conn *sql.Conn) error {
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO sample VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			panic(marker)
		})
	}()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sample`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("panic path did not rollback: count=%d err=%v", count, err)
	}
}

func TestOpenTxFailedCallbackDoesNotLeakConnectionLock(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "cleanup.db"))
	store := &Store{db: db, path: "cleanup.db", writable: true}
	if _, err := db.Exec(`CREATE TABLE sample(value INTEGER)`); err != nil {
		t.Fatal(err)
	}
	want := errors.New("force rollback")
	if err := store.withTx(context.Background(), true, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO sample VALUES(1)`)
		if err != nil {
			return err
		}
		return want
	}); !errors.Is(err, want) {
		t.Fatalf("callback failure mismatch: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sample`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback did not remove data: count=%d err=%v", count, err)
	}
}

func TestOpenReplacementConnectionHasRequiredPragmas(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "replace.db"))
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	first, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("discard original connection cause: %v", err)
	}
	_ = first.Close()
	var busy, foreignKeys int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if busy != 10000 || foreignKeys != 1 {
		t.Fatalf("replacement pragmas: busy_timeout=%d foreign_keys=%d", busy, foreignKeys)
	}
}

func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn, err := sqliteURI(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
