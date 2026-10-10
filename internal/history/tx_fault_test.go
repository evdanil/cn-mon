package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type openingFaultState struct {
	mu         sync.Mutex
	events     []string
	commitErr  error
	commitHook func()
	blockRoll  bool
	closed     int
}

func (s *openingFaultState) record(event string) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *openingFaultState) snapshot() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...), s.closed
}

type openingFaultConnector struct{ state *openingFaultState }

func (c openingFaultConnector) Connect(context.Context) (driver.Conn, error) {
	return &openingFaultConn{state: c.state, autocommit: true}, nil
}
func (c openingFaultConnector) Driver() driver.Driver { return openingFaultDriver{state: c.state} }

type openingFaultDriver struct{ state *openingFaultState }

func (d openingFaultDriver) Open(string) (driver.Conn, error) {
	return &openingFaultConn{state: d.state, autocommit: true}, nil
}

type openingFaultConn struct {
	state      *openingFaultState
	autocommit bool
	closed     bool
}

func (c *openingFaultConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *openingFaultConn) Close() error {
	if !c.closed {
		c.closed = true
		c.state.mu.Lock()
		c.state.closed++
		c.state.mu.Unlock()
	}
	return nil
}
func (c *openingFaultConn) Begin() (driver.Tx, error) { return nil, errors.New("Begin not supported") }
func (c *openingFaultConn) GetAutocommit() bool       { return c.autocommit }

func (c *openingFaultConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	query = strings.ToUpper(strings.TrimSpace(query))
	switch query {
	case "BEGIN", "BEGIN IMMEDIATE":
		c.autocommit = false
		c.state.record("begin")
	case "COMMIT":
		c.state.record("commit")
		if c.state.commitHook != nil {
			c.state.commitHook()
		}
		if c.state.commitErr != nil {
			return nil, c.state.commitErr
		}
		c.autocommit = true
	case "ROLLBACK":
		c.state.record("rollback")
		if c.state.blockRoll {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		c.autocommit = true
	default:
		return nil, fmt.Errorf("unexpected SQL %q", query)
	}
	return driver.RowsAffected(0), nil
}

func (c *openingFaultConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("query not supported")
}

type openingFaultRows struct{}

func (openingFaultRows) Columns() []string         { return nil }
func (openingFaultRows) Close() error              { return nil }
func (openingFaultRows) Next([]driver.Value) error { return io.EOF }

func newOpeningFaultStore(t *testing.T, state *openingFaultState) *Store {
	t.Helper()
	db := sql.OpenDB(openingFaultConnector{state: state})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: "fault.db", writable: true}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenTxFailedCommitRollsBackAndPreservesCause(t *testing.T) {
	commitErr := errors.New("commit failed")
	state := &openingFaultState{commitErr: commitErr}
	store := newOpeningFaultStore(t, state)
	err := store.withTx(context.Background(), true, func(*sql.Conn) error { return nil })
	if !errors.Is(err, commitErr) {
		t.Fatalf("commit error cause lost: %v", err)
	}
	events, _ := state.snapshot()
	if !equalStrings(events, []string{"begin", "commit", "rollback"}) {
		t.Fatalf("failed commit cleanup events=%v", events)
	}
	if err := store.withTx(context.Background(), true, func(*sql.Conn) error { return nil }); !errors.Is(err, commitErr) {
		t.Fatalf("test driver unexpectedly stopped failing commit: %v", err)
	}
}

func TestOpenTxSuccessfulCommitSurvivesImmediateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	state := &openingFaultState{commitHook: cancel}
	store := newOpeningFaultStore(t, state)
	if err := store.withTx(ctx, true, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("successful commit was replaced with cancellation: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("commit hook did not cancel context")
	}
	events, _ := state.snapshot()
	if !equalStrings(events, []string{"begin", "commit"}) {
		t.Fatalf("successful commit unexpectedly cleaned up: %v", events)
	}
}

func TestOpenTxCleanupTimeoutDiscardsConnectionAndClosesStore(t *testing.T) {
	callbackErr := errors.New("callback failed")
	state := &openingFaultState{blockRoll: true}
	store := newOpeningFaultStore(t, state)
	started := time.Now()
	err := store.withTx(context.Background(), true, func(*sql.Conn) error { return callbackErr })
	if !errors.Is(err, callbackErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup timeout lost primary/cleanup causes: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cleanup timeout did not stay bounded: %s", elapsed)
	}
	events, closed := state.snapshot()
	if !equalStrings(events, []string{"begin", "rollback"}) || closed == 0 {
		t.Fatalf("failed cleanup did not discard connection and close store: events=%v closed=%d", events, closed)
	}
	if err := store.withTx(context.Background(), false, func(*sql.Conn) error { return nil }); err == nil {
		t.Fatal("store remained usable after transaction cleanup could not be established")
	}
}

func TestOpenTxPanicCleanupFailureStillRethrowsOriginal(t *testing.T) {
	state := &openingFaultState{blockRoll: true}
	store := newOpeningFaultStore(t, state)
	marker := &struct{ value string }{"panic survives cleanup failure"}
	defer func() {
		if got := recover(); got != marker {
			t.Fatalf("panic=%#v, want original %#v", got, marker)
		}
		_, closed := state.snapshot()
		if closed == 0 {
			t.Fatal("store did not close after failed panic cleanup")
		}
	}()
	_ = store.withTx(context.Background(), true, func(*sql.Conn) error { panic(marker) })
}
