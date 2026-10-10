package history

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestOpenReaderMissingDoesNotCreateFileOrParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "nested", "history.db")
	store, err := OpenReader(context.Background(), path)
	if err != nil || store != nil {
		t.Fatalf("OpenReader missing returned %v, %v", store, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reader created a parent or unexpected path result: %v", err)
	}
}

func TestOpenWriterCreatesSchemaAndEnablesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.db")
	store, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.path != mustAbsPath(t, path) || !store.writable {
		t.Fatalf("store identity mismatch: path=%q writable=%v", store.path, store.writable)
	}
	var journal string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("writer journal mode=%q, want wal", journal)
	}
	var version string
	if err := store.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != "1" {
		t.Fatalf("schema version=%q err=%v", version, err)
	}
}

func TestOpenWriterInitializesExistingEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version string
	if err := store.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version=%q err=%v", version, err)
	}
}

func TestOpenWriterRejectsBlockedParentAndPreservesCause(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("block parent directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenWriter(context.Background(), filepath.Join(parent, "history.db"))
	if err == nil || !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("writer did not preserve directory creation failure: %v", err)
	}
}

func TestOpenStoreCloseIsIdempotent(t *testing.T) {
	store, err := OpenWriter(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close returned %v", err)
	}
}

func TestOpenWriterRejectsMalformedSchemaWithoutChangingJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	dsn, err := sqliteURI(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta VALUES('schema_version','1');`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWriter(context.Background(), path); err == nil {
		t.Fatal("writer accepted marker-only schema")
	} else if strings.Contains(err.Error(), "history read ") {
		t.Fatalf("writer preflight reported the wrong/doubled operation context: %v", err)
	}
	check, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var after string
	if err := check.QueryRow(`PRAGMA journal_mode`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || before != "delete" {
		t.Fatalf("failed preflight changed journal mode: before=%q after=%q", before, after)
	}
}

func TestOpenWriterRejectsUnrelatedExistingObjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	dsn, err := sqliteURI(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := OpenWriter(context.Background(), path); err == nil {
		t.Fatal("writer treated a foreign table as an empty database")
	}
}

func TestOpenWriterRejectsGeneratedColumnBeforeWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated.db")
	db := createOpeningSchemaFile(t, path, nil)
	if _, err := db.Exec(`ALTER TABLE request ADD COLUMN extra TEXT GENERATED ALWAYS AS ('x') VIRTUAL`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertOpeningWriterRejectsWithoutWAL(t, path)
}

func TestOpenWriterRejectsChangedUniqueCollationBeforeWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collation.db")
	db := createOpeningSchemaFile(t, path, func(statement string) string {
		if strings.HasPrefix(statement, "CREATE TABLE request ") {
			return strings.Replace(statement, "key TEXT UNIQUE", "key TEXT COLLATE NOCASE UNIQUE", 1)
		}
		return statement
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertOpeningWriterRejectsWithoutWAL(t, path)
}

func TestOpenWriterRejectsPrimaryKeyConflictReplaceBeforeWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflict-action.db")
	db := createOpeningSchemaFile(t, path, func(statement string) string {
		if strings.HasPrefix(statement, "CREATE TABLE run ") {
			return strings.Replace(statement, "id TEXT PRIMARY KEY", "id TEXT PRIMARY KEY ON CONFLICT REPLACE", 1)
		}
		return statement
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertOpeningWriterRejectsWithoutWAL(t, path)
}

func TestOpenWriterRejectsExtraCheckAndStrictTableOptionBeforeWAL(t *testing.T) {
	cases := []struct {
		name      string
		transform func(string) string
	}{
		{
			name: "check constraint",
			transform: func(statement string) string {
				if strings.HasPrefix(statement, "CREATE TABLE request ") {
					return strings.TrimSuffix(statement, ")") + ", CHECK (length(key) > 0))"
				}
				return statement
			},
		},
		{
			name: "strict table option",
			transform: func(statement string) string {
				if strings.HasPrefix(statement, "CREATE TABLE request ") {
					return statement + " STRICT"
				}
				return statement
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsupported.db")
			db := createOpeningSchemaFile(t, path, test.transform)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			assertOpeningWriterRejectsWithoutWAL(t, path)
		})
	}
}

func TestOpenWriterRejectsAutoincrementCommentSpoofBeforeWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autoincrement-comment.db")
	db := createOpeningSchemaFile(t, path, func(statement string) string {
		if strings.HasPrefix(statement, "CREATE TABLE request ") {
			return strings.Replace(statement, "id INTEGER PRIMARY KEY AUTOINCREMENT", "id INTEGER PRIMARY KEY /* id INTEGER PRIMARY KEY AUTOINCREMENT */", 1)
		}
		return statement
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertOpeningWriterRejectsWithoutWAL(t, path)
}

func TestOpenWriterAcceptsQuotedAutoincrementID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quoted-id.db")
	db := createOpeningSchemaFile(t, path, func(statement string) string {
		if strings.HasPrefix(statement, "CREATE TABLE request ") {
			return strings.Replace(statement, "id INTEGER PRIMARY KEY AUTOINCREMENT", `"id" INTEGER PRIMARY KEY AUTOINCREMENT`, 1)
		}
		return statement
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenWriter(context.Background(), path)
	if err != nil {
		t.Fatalf("equivalent quoted id rejected: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsNewerSchemaWithoutChangingBytesOrJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")
	db := createOpeningSchemaFileWithVersion(t, path, "2", nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modeBefore := openingJournalMode(t, path)
	if modeBefore != "delete" {
		t.Fatalf("newer fixture journal mode=%q, want delete", modeBefore)
	}
	if store, err := OpenReader(context.Background(), path); err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("reader accepted newer schema version")
	}
	if _, err := OpenWriter(context.Background(), path); err == nil {
		t.Fatal("writer accepted newer schema version")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("failed newer-schema opens changed database bytes")
	}
	if modeAfter := openingJournalMode(t, path); modeAfter != modeBefore {
		t.Fatalf("newer-schema open changed journal mode: before=%q after=%q", modeBefore, modeAfter)
	}
}

func TestOpenRejectsNonSQLiteFileWithoutChangingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	contents := []byte("this is not a SQLite database\x00and must never be truncated\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if store, err := OpenReader(context.Background(), path); err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("reader accepted non-SQLite bytes")
	}
	if _, err := OpenWriter(context.Background(), path); err == nil {
		t.Fatal("writer accepted non-SQLite bytes")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("failed corrupt-file opens changed bytes: before=%q after=%q", before, after)
	}
}

func createOpeningSchemaFile(t *testing.T, path string, transform func(string) string) *sql.DB {
	return createOpeningSchemaFileWithVersion(t, path, schemaVersion, transform)
}

func createOpeningSchemaFileWithVersion(t *testing.T, path, version string, transform func(string) string) *sql.DB {
	t.Helper()
	dsn, err := sqliteURI(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range schemaStatements {
		if transform != nil {
			statement = transform(statement)
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES('schema_version',?)`, version); err != nil {
		t.Fatal(err)
	}
	return db
}

func openingJournalMode(t *testing.T, path string) string {
	t.Helper()
	dsn, err := sqliteURI(path, true)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func assertOpeningWriterRejectsWithoutWAL(t *testing.T, path string) {
	t.Helper()
	dsn, err := sqliteURI(path, true)
	if err != nil {
		t.Fatal(err)
	}
	check, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := check.QueryRow(`PRAGMA journal_mode`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_ = check.Close()
	if before != "delete" {
		t.Fatalf("fixture journal mode=%q, want delete", before)
	}
	if _, err := OpenWriter(context.Background(), path); err == nil {
		t.Fatal("writer accepted malformed schema")
	}
	verify, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	var after string
	if err := verify.QueryRow(`PRAGMA journal_mode`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("failed preflight changed journal mode: before=%q after=%q", before, after)
	}
}

func mustAbsPath(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(got)
}
