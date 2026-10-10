package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestCancellationOnlyPreservesCausalProvenance(t *testing.T) {
	busy := error(sqlite3.BUSY)
	other := errors.New("independent failure")
	tests := []struct {
		name  string
		err   error
		cause error
		want  bool
	}{
		{"exact canceled", context.Canceled, context.Canceled, true},
		{"wrapped deadline", fmt.Errorf("read: %w", context.DeadlineExceeded), context.DeadlineExceeded, true},
		{"all joined causes", errors.Join(context.Canceled, fmt.Errorf("again: %w", context.Canceled)), context.Canceled, true},
		{"causally tagged busy", &canceledDBError{cause: context.Canceled, operation: busy}, context.Canceled, true},
		{"nil error", nil, context.Canceled, false},
		{"nil cause", context.Canceled, nil, false},
		{"wrong cancellation cause", context.DeadlineExceeded, context.Canceled, false},
		{"independent joined driver error", errors.Join(context.Canceled, busy), context.Canceled, false},
		{"independent joined failure", errors.Join(context.Canceled, other), context.Canceled, false},
		{"cleanup sentinel", markCleanup(context.Canceled), context.Canceled, false},
		{"cleanup in join", errors.Join(context.Canceled, markCleanup(other)), context.Canceled, false},
		{"unrelated error", other, context.Canceled, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CancellationOnly(tc.err, tc.cause); got != tc.want {
				t.Fatalf("CancellationOnly(%v, %v) = %t, want %t", tc.err, tc.cause, got, tc.want)
			}
		})
	}
}

type closeErrorConnector struct{ err error }

func (c closeErrorConnector) Connect(context.Context) (driver.Conn, error) {
	return closeErrorConn{err: c.err}, nil
}
func (c closeErrorConnector) Driver() driver.Driver { return closeErrorDriver{err: c.err} }

type closeErrorDriver struct{ err error }

func (d closeErrorDriver) Open(string) (driver.Conn, error) { return closeErrorConn{err: d.err}, nil }

type closeErrorConn struct{ err error }

func (closeErrorConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c closeErrorConn) Close() error                      { return c.err }
func (closeErrorConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
func (c closeErrorConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (c closeErrorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &closeErrorRows{err: c.err}, nil
}

type closeErrorRows struct {
	err  error
	done bool
}

func (closeErrorRows) Columns() []string { return []string{"value"} }
func (r *closeErrorRows) Close() error   { return r.err }
func (r *closeErrorRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

func cancellationTestRows(t *testing.T, closeErr error) (*sql.DB, *sql.Conn, *sql.Rows) {
	t.Helper()
	db := sql.OpenDB(closeErrorConnector{err: closeErr})
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(context.Background(), "SELECT")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rows.Close()
		_ = conn.Close()
		_ = db.Close()
	})
	return db, conn, rows
}

func TestReaderAndPruneRowsCloseAlwaysRetainCleanupProvenance(t *testing.T) {
	closeErrors := []struct {
		name string
		err  error
	}{
		{"same cancellation", context.Canceled},
		{"driver busy", error(sqlite3.BUSY)},
		{"ordinary I/O", errors.New("close failed")},
	}
	for _, tc := range closeErrors {
		for _, withPrimary := range []bool{false, true} {
			name := tc.name + "/standalone"
			if withPrimary {
				name = tc.name + "/with-primary"
			}
			t.Run("reader/"+name, func(t *testing.T) {
				_, conn, rows := cancellationTestRows(t, tc.err)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				snapshot := &Snapshot{conn: conn, ctx: ctx, active: true}
				var got error
				if withPrimary {
					got = snapshot.failRows(rows, context.Canceled)
				} else {
					got = snapshot.closeRows(rows)
				}
				if got == nil || !errors.Is(got, tc.err) {
					t.Fatalf("reader close error=%v, want retained cause %v", got, tc.err)
				}
				if withPrimary && !errors.Is(got, context.Canceled) {
					t.Fatalf("reader lost primary cancellation: %v", got)
				}
				if CancellationOnly(got, context.Canceled) {
					t.Fatalf("reader cleanup was classified as cancellation only: %v", got)
				}
			})
			t.Run("prune/"+name, func(t *testing.T) {
				_, _, rows := cancellationTestRows(t, tc.err)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				var primary error
				if withPrimary {
					primary = context.Canceled
				}
				got := closePruneRows(ctx, rows, primary)
				if got == nil || !errors.Is(got, tc.err) {
					t.Fatalf("prune close error=%v, want retained cause %v", got, tc.err)
				}
				if withPrimary && !errors.Is(got, context.Canceled) {
					t.Fatalf("prune lost primary cancellation: %v", got)
				}
				if CancellationOnly(got, context.Canceled) {
					t.Fatalf("prune cleanup was classified as cancellation only: %v", got)
				}
			})
		}
	}
}

func TestStoreCloseFailureIsCleanupEvenWhenItIsCancellation(t *testing.T) {
	for _, closeErr := range []error{context.Canceled, error(sqlite3.BUSY), errors.New("close I/O failure")} {
		db := sql.OpenDB(closeErrorConnector{err: closeErr})
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		store := &Store{db: db}
		got := store.Close()
		if got == nil || !errors.Is(got, closeErr) {
			t.Fatalf("Store.Close error=%v, want retained %v", got, closeErr)
		}
		if CancellationOnly(got, context.Canceled) {
			t.Fatalf("Store.Close cleanup %v was classified as cancellation only", got)
		}
	}
}

func TestCanceledTransactionWithRollbackFailureIsNotCancellationOnly(t *testing.T) {
	state := &openingFaultState{blockRoll: true}
	store := newOpeningFaultStore(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	err := store.withTx(ctx, true, func(*sql.Conn) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transaction lost cancellation or rollback cleanup: %v", err)
	}
	if CancellationOnly(err, context.Canceled) {
		t.Fatalf("transaction cleanup was hidden as graceful cancellation: %v", err)
	}
}

func TestNormalizeDBErrorTagsOnlySingleCausalDriverError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	busy := error(sqlite3.BUSY)
	wrapped := fmt.Errorf("driver operation: %w", busy)
	got := normalizeDBError(ctx, wrapped)
	if !CancellationOnly(got, context.Canceled) || !errors.Is(got, busy) || !errors.Is(got, context.Canceled) {
		t.Fatalf("single wrapped BUSY did not retain causal cancellation and driver identity: %v", got)
	}

	independent := errors.New("additional error")
	joined := errors.Join(busy, independent)
	got = normalizeDBError(ctx, joined)
	if CancellationOnly(got, context.Canceled) || !errors.Is(got, busy) || !errors.Is(got, independent) || errors.Is(got, context.Canceled) {
		t.Fatalf("multi-cause driver error was incorrectly tagged: %v", got)
	}
}
