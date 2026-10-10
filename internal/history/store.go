package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type Store struct {
	db        *sql.DB
	path      string
	writable  bool
	closeOnce sync.Once
	closeErr  error
}

// Opening and schema-gate behavior adapts cn_tool/utils/ping_store.py:232–333
// from cn-tool v0.8.0, commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa
// (Apache-2.0), with the stricter structural validation required by this tool.
func OpenWriter(ctx context.Context, path string) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("history write: nil context")
	}
	abs, err := resolveHistoryPath(path)
	if err != nil {
		return nil, fmt.Errorf("history write: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, wrapHistoryPath("write", abs, err)
	}
	exists, err := historyPathExists(abs)
	if err != nil {
		return nil, wrapHistoryPath("write", abs, err)
	}
	if exists {
		if err := preflightSchema(ctx, abs); err != nil {
			return nil, err
		}
	}
	uri := sqliteURIForAbsolute(abs, false)
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return nil, wrapHistoryPath("write", abs, err)
	}
	configurePool(db)
	store := &Store{db: db, path: abs, writable: true}
	if err := store.withTx(ctx, true, func(conn *sql.Conn) error {
		empty, err := schemaIsEmpty(ctx, conn)
		if err != nil {
			return err
		}
		if empty {
			if err := createSchema(ctx, conn); err != nil {
				return err
			}
		}
		return validateSchema(ctx, conn)
	}); err != nil {
		return nil, closeAfterOpenFailure(store, err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, closeAfterOpenFailure(store, store.wrap("write", normalizeDBError(ctx, err)))
	}
	var journalMode string
	err = conn.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&journalMode)
	if err != nil {
		err = normalizeDBError(ctx, err)
	}
	closeErr := conn.Close()
	if err == nil && closeErr != nil {
		err = closeErr
	}
	if err == nil && journalMode != "wal" {
		err = fmt.Errorf("SQLite selected journal mode %q, want wal", journalMode)
	}
	if err != nil {
		return nil, closeAfterOpenFailure(store, store.wrap("write", err))
	}
	return store, nil
}

func OpenReader(ctx context.Context, path string) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("history read: nil context")
	}
	abs, err := resolveHistoryPath(path)
	if err != nil {
		return nil, fmt.Errorf("history read: %w", err)
	}
	exists, err := historyPathExists(abs)
	if err != nil {
		return nil, wrapHistoryPath("read", abs, err)
	}
	if !exists {
		return nil, nil
	}
	db, err := sql.Open("sqlite3", sqliteURIForAbsolute(abs, true))
	if err != nil {
		if missing, statErr := historyPathMissing(abs); statErr != nil {
			return nil, wrapHistoryPath("read", abs, statErr)
		} else if missing {
			return nil, nil
		}
		return nil, wrapHistoryPath("read", abs, err)
	}
	configurePool(db)
	store := &Store{db: db, path: abs}
	if err := store.withTx(ctx, false, func(conn *sql.Conn) error { return validateSchema(ctx, conn) }); err != nil {
		if missing, statErr := historyPathMissing(abs); statErr != nil {
			return nil, closeAfterOpenFailure(store, errors.Join(err, wrapHistoryPath("read", abs, statErr)))
		} else if missing {
			if closeErr := store.Close(); closeErr != nil {
				return nil, wrapHistoryPath("read", abs, closeErr)
			}
			return nil, nil
		}
		return nil, closeAfterOpenFailure(store, err)
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
	})
	return s.closeErr
}

func (s *Store) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if s == nil || s.path == "" {
		return fmt.Errorf("history %s: %w", op, err)
	}
	return wrapHistoryPath(op, s.path, err)
}

func wrapHistoryPath(op, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("history %s %q: %w", op, path, err)
}

func preflightSchema(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite3", sqliteURIForAbsolute(path, true))
	if err != nil {
		return wrapHistoryPath("write", path, err)
	}
	configurePool(db)
	store := &Store{db: db, path: path}
	checkErr := store.withTxOperation(ctx, "write", false, func(conn *sql.Conn) error {
		empty, err := schemaIsEmpty(ctx, conn)
		if err != nil || empty {
			return err
		}
		return validateSchema(ctx, conn)
	})
	closeErr := store.Close()
	if checkErr == nil && closeErr != nil {
		return store.wrap("write", closeErr)
	}
	return errors.Join(checkErr, closeErr)
}

func historyPathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func historyPathMissing(path string) (bool, error) {
	exists, err := historyPathExists(path)
	return !exists && err == nil, err
}

func configurePool(db *sql.DB) {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
}

func closeAfterOpenFailure(store *Store, err error) error {
	if closeErr := store.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	return err
}
