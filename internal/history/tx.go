package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

const rollbackTimeout = 250 * time.Millisecond

// Transaction boundaries adapt PingStore.snapshot/_transaction at
// cn_tool/utils/ping_store.py:280–315, pinned cn-tool v0.8.0 commit
// 2e2f3c40ef6718486f1ca4477f9145029ec495fa (Apache-2.0). Context-aware
// connection acquisition and bounded cleanup are Go-specific safeguards.
func (s *Store) withTx(ctx context.Context, write bool, fn func(*sql.Conn) error) error {
	op := "read"
	if write {
		op = "write"
	}
	return s.withTxOperation(ctx, op, write, fn)
}

func (s *Store) withTxOperation(ctx context.Context, op string, write bool, fn func(*sql.Conn) error) error {
	return s.withTxOperationCommit(ctx, op, write, fn, nil)
}

func (s *Store) withTxOperationCommit(ctx context.Context, op string, write bool, fn func(*sql.Conn) error, committed *bool) error {
	if ctx == nil {
		return s.wrap(op, errors.New("nil context"))
	}
	if s == nil || s.db == nil {
		return s.wrap(op, errors.New("history store is closed"))
	}
	if write && !s.writable {
		return s.wrap(op, errors.New("history store is read-only"))
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return s.wrap(op, normalizeDBError(ctx, err))
	}
	begin := "BEGIN"
	if write {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		primary := normalizeDBError(ctx, err)
		if closeErr := conn.Close(); closeErr != nil {
			primary = errors.Join(primary, markCleanup(closeErr))
		}
		return s.wrap(op, primary)
	}

	callbackErr, panicValue := invokeTransactionCallback(conn, fn)
	if panicValue != nil {
		cleanupErr := rollbackTransaction(conn)
		if cleanupErr != nil {
			_ = discardConnection(conn)
		}
		_ = conn.Close()
		if cleanupErr != nil {
			_ = s.Close()
		}
		panic(panicValue)
	}
	if callbackErr != nil {
		return s.finishFailedTransaction(ctx, op, conn, callbackErr)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		primary := normalizeDBError(ctx, err)
		return s.finishFailedTransaction(ctx, op, conn, primary)
	}
	if committed != nil {
		*committed = true
	}
	if err := s.closeTxConnection(conn); err != nil {
		return s.wrap(op, markCleanup(err))
	}
	return nil
}

func (s *Store) closeTxConnection(conn *sql.Conn) error {
	if s.closeConnection != nil {
		return s.closeConnection(conn)
	}
	return conn.Close()
}

func invokeTransactionCallback(conn *sql.Conn, fn func(*sql.Conn) error) (err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	if fn == nil {
		return errors.New("nil transaction callback"), nil
	}
	err = fn(conn)
	return err, nil
}

func (s *Store) finishFailedTransaction(ctx context.Context, op string, conn *sql.Conn, primary error) error {
	rollbackErr := rollbackTransaction(conn)
	if rollbackErr != nil {
		if discardErr := discardConnection(conn); discardErr != nil {
			rollbackErr = errors.Join(rollbackErr, markCleanup(discardErr))
		}
	}
	closeErr := conn.Close()
	if rollbackErr != nil {
		// Close the Store only after releasing its dedicated connection.
		if storeCloseErr := s.Close(); storeCloseErr != nil {
			primary = errors.Join(primary, fmt.Errorf("close store after rollback failure: %w", storeCloseErr))
		}
	}
	if closeErr != nil {
		primary = errors.Join(primary, markCleanup(closeErr))
	}
	if rollbackErr != nil {
		primary = errors.Join(primary, markCleanup(fmt.Errorf("rollback failed: %w", rollbackErr)))
	}
	return s.wrap(op, primary)
}

func rollbackTransaction(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	_, err := conn.ExecContext(ctx, "ROLLBACK")
	if err == nil {
		return nil
	}
	rollbackErr := normalizeDBError(ctx, err)
	rawErr := conn.Raw(func(raw any) error {
		autocommit, ok := raw.(interface{ GetAutocommit() bool })
		if ok && autocommit.GetAutocommit() {
			return nil
		}
		return rollbackErr
	})
	if rawErr == nil {
		return nil
	}
	return rollbackErr
}

func discardConnection(conn *sql.Conn) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}
