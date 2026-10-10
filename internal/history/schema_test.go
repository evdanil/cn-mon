package history

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestOpenSchemaAcceptsCompatibleExtraNonuniqueIndex(t *testing.T) {
	store, err := OpenWriter(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE INDEX extra_result_host ON result(host)`); err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := validateSchema(context.Background(), conn); err != nil {
		t.Fatalf("extra nonunique index rejected: %v", err)
	}
}

func TestOpenSchemaRejectsChangedUniqueness(t *testing.T) {
	store, err := OpenWriter(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE UNIQUE INDEX extra_unique_result_host ON result(host)`); err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := validateSchema(context.Background(), conn); err == nil {
		t.Fatal("unsupported unique index accepted")
	}
}

func TestOpenSchemaMetadataUsesSourceConstraints(t *testing.T) {
	store, err := OpenWriter(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var withoutRowID int
	if err := store.db.QueryRow(`SELECT instr(upper(sql), 'WITHOUT ROWID') FROM sqlite_schema WHERE type='table' AND name='result'`).Scan(&withoutRowID); err != nil || withoutRowID == 0 {
		t.Fatalf("result table is not WITHOUT ROWID: position=%d err=%v", withoutRowID, err)
	}
	for _, table := range []string{"request", "round"} {
		var createSQL string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&createSQL); err != nil {
			t.Fatal(err)
		}
		if !containsSQLToken(createSQL, "AUTOINCREMENT") {
			t.Errorf("%s primary id lost AUTOINCREMENT: %s", table, createSQL)
		}
	}
	var indexes int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='index' AND name IN ('run_by_request','round_by_run')`).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("required indexes=%d err=%v", indexes, err)
	}
}

func TestOpenValidateSchemaRejectsMalformedMarkerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	dsn, err := sqliteURI(path, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta VALUES('schema_version','1')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := OpenReader(context.Background(), path)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("reader accepted marker-only schema")
	}
}

func containsSQLToken(sql, token string) bool {
	return strings.Contains(strings.ToUpper(sql), token)
}
