package history

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const schemaVersion = "1"

// These statements preserve the schema from cn-tool v0.8.0,
// commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa,
// cn_tool/utils/ping_store.py::_SCHEMA. Source is Apache-2.0 licensed.
var schemaStatements = [...]string{
	"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)",
	"CREATE TABLE request (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT UNIQUE NOT NULL, targets TEXT NOT NULL, tcp_ports TEXT NOT NULL, hosts TEXT NOT NULL)",
	"CREATE TABLE run (id TEXT PRIMARY KEY, request_id INTEGER NOT NULL REFERENCES request ON DELETE CASCADE, source TEXT, started REAL NOT NULL, interval REAL, until REAL, host TEXT NOT NULL, pid INTEGER NOT NULL, heartbeat REAL NOT NULL, round_seconds REAL, ended REAL, status TEXT NOT NULL)",
	"CREATE TABLE round (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES run ON DELETE CASCADE, started REAL NOT NULL, observations TEXT NOT NULL, answered INTEGER NOT NULL)",
	"CREATE TABLE result (round_id INTEGER NOT NULL REFERENCES round ON DELETE CASCADE, host TEXT NOT NULL, address TEXT NOT NULL, t REAL NOT NULL, result TEXT NOT NULL, tcp TEXT, PRIMARY KEY (round_id, host)) WITHOUT ROWID",
	"CREATE INDEX run_by_request ON run (request_id, started)",
	"CREATE INDEX round_by_run ON round (run_id, started)",
}

type schemaColumn struct {
	name     string
	typeName string
	notNull  int
	primary  int
}

var expectedColumns = map[string][]schemaColumn{
	"meta": {
		{"key", "TEXT", 0, 1}, {"value", "TEXT", 0, 0},
	},
	"request": {
		{"id", "INTEGER", 0, 1}, {"key", "TEXT", 1, 0}, {"targets", "TEXT", 1, 0}, {"tcp_ports", "TEXT", 1, 0}, {"hosts", "TEXT", 1, 0},
	},
	"run": {
		{"id", "TEXT", 0, 1}, {"request_id", "INTEGER", 1, 0}, {"source", "TEXT", 0, 0}, {"started", "REAL", 1, 0}, {"interval", "REAL", 0, 0}, {"until", "REAL", 0, 0}, {"host", "TEXT", 1, 0}, {"pid", "INTEGER", 1, 0}, {"heartbeat", "REAL", 1, 0}, {"round_seconds", "REAL", 0, 0}, {"ended", "REAL", 0, 0}, {"status", "TEXT", 1, 0},
	},
	"round": {
		{"id", "INTEGER", 0, 1}, {"run_id", "TEXT", 1, 0}, {"started", "REAL", 1, 0}, {"observations", "TEXT", 1, 0}, {"answered", "INTEGER", 1, 0},
	},
	"result": {
		{"round_id", "INTEGER", 1, 1}, {"host", "TEXT", 1, 2}, {"address", "TEXT", 1, 0}, {"t", "REAL", 1, 0}, {"result", "TEXT", 1, 0}, {"tcp", "TEXT", 0, 0},
	},
}

var requiredIndexes = map[string]struct {
	table   string
	columns []string
}{
	"run_by_request": {"run", []string{"request_id", "started"}},
	"round_by_run":   {"round", []string{"run_id", "started"}},
}

var expectedUnique = map[string][][]string{
	"meta":    {{"key"}},
	"request": {{"key"}},
	"run":     {{"id"}},
	"result":  {{"round_id", "host"}},
}

func createSchema(ctx context.Context, conn *sql.Conn) error {
	for _, statement := range schemaStatements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return normalizeDBError(ctx, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('schema_version', ?)`, schemaVersion); err != nil {
		return normalizeDBError(ctx, err)
	}
	return nil
}

func schemaIsEmpty(ctx context.Context, conn *sql.Conn) (bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT type, name FROM sqlite_schema WHERE substr(name, 1, 7) != 'sqlite_'`)
	if err != nil {
		return false, normalizeDBError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var objectType, name string
		if err := rows.Scan(&objectType, &name); err != nil {
			return false, normalizeDBError(ctx, err)
		}
		return false, nil
	}
	if err := rows.Err(); err != nil {
		return false, normalizeDBError(ctx, err)
	}
	return true, nil
}

func validateSchema(ctx context.Context, conn *sql.Conn) error {
	objects, err := loadSchemaObjects(ctx, conn)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.kind != "table" {
			if object.kind == "index" {
				continue
			}
			return fmt.Errorf("unsupported schema object %s %q", object.kind, object.name)
		}
		if _, ok := expectedColumns[object.name]; !ok {
			return fmt.Errorf("unsupported table %q", object.name)
		}
	}
	for table := range expectedColumns {
		if !hasSchemaObject(objects, "table", table) {
			return fmt.Errorf("required table %q is missing", table)
		}
		if err := validateColumns(ctx, conn, table); err != nil {
			return err
		}
	}
	if err := validateTableOptions(ctx, conn); err != nil {
		return err
	}
	if err := validateIndexes(ctx, conn); err != nil {
		return err
	}
	if err := validateForeignKeys(ctx, conn); err != nil {
		return err
	}
	var version sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return normalizeDBError(ctx, fmt.Errorf("read schema version: %w", err))
	}
	if !version.Valid || version.String != schemaVersion {
		if version.Valid && version.String > schemaVersion {
			return fmt.Errorf("history was written by a newer schema version %q", version.String)
		}
		return fmt.Errorf("unsupported history schema version %q", version.String)
	}
	return nil
}

type schemaObject struct {
	kind, name, table string
	sql               sql.NullString
}

func loadSchemaObjects(ctx context.Context, conn *sql.Conn) ([]schemaObject, error) {
	rows, err := conn.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_schema WHERE substr(name, 1, 7) != 'sqlite_' ORDER BY type, name`)
	if err != nil {
		return nil, normalizeDBError(ctx, err)
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.table, &object.sql); err != nil {
			return nil, normalizeDBError(ctx, err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeDBError(ctx, err)
	}
	return objects, nil
}

func hasSchemaObject(objects []schemaObject, kind, name string) bool {
	for _, object := range objects {
		if object.kind == kind && object.name == name {
			return true
		}
	}
	return false
}

func validateColumns(ctx context.Context, conn *sql.Conn, table string) error {
	query := `PRAGMA table_xinfo(` + quoteIdentifier(table) + `)`
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return normalizeDBError(ctx, err)
	}
	defer rows.Close()
	expected := expectedColumns[table]
	seen := make(map[string]bool, len(expected))
	count := 0
	for rows.Next() {
		var cid, notNull, primary, hidden int
		var name, typeName string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &primary, &hidden); err != nil {
			return normalizeDBError(ctx, err)
		}
		count++
		want, ok := expectedColumn(table, name)
		if !ok {
			return fmt.Errorf("table %q has unexpected column %q", table, name)
		}
		if seen[name] {
			return fmt.Errorf("table %q repeats column %q", table, name)
		}
		seen[name] = true
		if !strings.EqualFold(typeName, want.typeName) || notNull != want.notNull || primary != want.primary || defaultValue.Valid || hidden != 0 {
			return fmt.Errorf("table %q column %q has incompatible definition", table, name)
		}
	}
	if err := rows.Err(); err != nil {
		return normalizeDBError(ctx, err)
	}
	if count != len(expected) {
		return fmt.Errorf("table %q has %d columns, want %d", table, count, len(expected))
	}
	return nil
}

func expectedColumn(table, name string) (schemaColumn, bool) {
	for _, column := range expectedColumns[table] {
		if column.name == name {
			return column, true
		}
	}
	return schemaColumn{}, false
}

func validateTableOptions(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema WHERE type='table' AND substr(name, 1, 7) != 'sqlite_'`)
	if err != nil {
		return normalizeDBError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var createSQL sql.NullString
		if err := rows.Scan(&name, &createSQL); err != nil {
			return normalizeDBError(ctx, err)
		}
		if !createSQL.Valid {
			return fmt.Errorf("table %q has no schema SQL", name)
		}
		words := schemaDDLWords(createSQL.String)
		if hasDDLSequence(words, "ON", "CONFLICT") {
			return fmt.Errorf("table %q uses an unsupported conflict action", name)
		}
		for _, unsupported := range []string{"CHECK", "COLLATE", "DEFERRABLE", "INITIALLY", "STRICT"} {
			if hasDDLSequence(words, unsupported) {
				return fmt.Errorf("table %q uses unsupported %s semantics", name, unsupported)
			}
		}
		if name == "request" || name == "round" {
			if !hasDDLSequence(words, "AUTOINCREMENT") {
				return fmt.Errorf("table %q is missing its AUTOINCREMENT primary key", name)
			}
		} else if hasDDLSequence(words, "AUTOINCREMENT") {
			return fmt.Errorf("table %q has an unexpected AUTOINCREMENT key", name)
		}
		if name == "result" {
			if !hasDDLSequence(words, "WITHOUT", "ROWID") {
				return fmt.Errorf("result table is missing WITHOUT ROWID")
			}
		} else if hasDDLSequence(words, "WITHOUT", "ROWID") {
			return fmt.Errorf("table %q unexpectedly uses WITHOUT ROWID", name)
		}
	}
	if err := rows.Err(); err != nil {
		return normalizeDBError(ctx, err)
	}
	return nil
}

func schemaDDLWords(statement string) []string {
	words := make([]string, 0, len(statement)/4)
	for i := 0; i < len(statement); {
		switch {
		case i+1 < len(statement) && statement[i:i+2] == "--":
			i += 2
			for i < len(statement) && statement[i] != '\n' {
				i++
			}
		case i+1 < len(statement) && statement[i:i+2] == "/*":
			i += 2
			for i+1 < len(statement) && statement[i:i+2] != "*/" {
				i++
			}
			if i+1 < len(statement) {
				i += 2
			}
		case statement[i] == '\'' || statement[i] == '"' || statement[i] == '`':
			quote := statement[i]
			i++
			for i < len(statement) {
				if statement[i] == quote {
					i++
					if i < len(statement) && statement[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case statement[i] == '[':
			i++
			for i < len(statement) && statement[i] != ']' {
				i++
			}
			if i < len(statement) {
				i++
			}
		case isDDLWordByte(statement[i]):
			start := i
			for i < len(statement) && isDDLWordByte(statement[i]) {
				i++
			}
			words = append(words, strings.ToUpper(statement[start:i]))
		default:
			i++
		}
	}
	return words
}

func isDDLWordByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_'
}

func hasDDLSequence(words []string, sequence ...string) bool {
	if len(sequence) == 0 || len(sequence) > len(words) {
		return false
	}
	for i := 0; i <= len(words)-len(sequence); i++ {
		match := true
		for j := range sequence {
			if words[i+j] != sequence[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func validateIndexes(ctx context.Context, conn *sql.Conn) error {
	seenRequired := make(map[string]bool, len(requiredIndexes))
	seenUnique := make(map[string]map[string]bool, len(expectedUnique))
	for table := range expectedColumns {
		rows, err := conn.QueryContext(ctx, `PRAGMA index_list(`+quoteIdentifier(table)+`)`)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		for rows.Next() {
			var seq, unique, partial int
			var name, origin string
			if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				rows.Close()
				return normalizeDBError(ctx, err)
			}
			if required, ok := requiredIndexes[name]; ok {
				columns, semanticsOK, err := indexColumns(ctx, conn, name)
				if err != nil {
					rows.Close()
					return err
				}
				if required.table != table || unique != 0 || partial != 0 || !semanticsOK || !equalStrings(columns, required.columns) {
					rows.Close()
					return fmt.Errorf("index %q has an incompatible definition", name)
				}
				seenRequired[name] = true
				continue
			}
			if strings.HasPrefix(name, "sqlite_autoindex_") {
				if unique == 0 || partial != 0 {
					rows.Close()
					return fmt.Errorf("table %q has an unexpected automatic index %q", table, name)
				}
				columns, semanticsOK, err := indexColumns(ctx, conn, name)
				if err != nil {
					rows.Close()
					return err
				}
				if !semanticsOK || !expectedUniqueColumns(table, columns) {
					rows.Close()
					return fmt.Errorf("table %q has an unexpected automatic index %q", table, name)
				}
				key := table + ":" + strings.Join(columns, ",")
				if seenUnique[table] == nil {
					seenUnique[table] = make(map[string]bool)
				}
				seenUnique[table][key] = true
				continue
			}
			if unique != 0 {
				rows.Close()
				return fmt.Errorf("unsupported unique index %q", name)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return normalizeDBError(ctx, err)
		}
		if err := rows.Close(); err != nil {
			return normalizeDBError(ctx, err)
		}
	}
	for name := range requiredIndexes {
		if !seenRequired[name] {
			return fmt.Errorf("required index %q is missing", name)
		}
	}
	for table, constraints := range expectedUnique {
		for _, columns := range constraints {
			key := table + ":" + strings.Join(columns, ",")
			if !seenUnique[table][key] {
				return fmt.Errorf("table %q is missing unique constraint on %v", table, columns)
			}
		}
	}
	return nil
}

func expectedUniqueColumns(table string, columns []string) bool {
	for _, expected := range expectedUnique[table] {
		if equalStrings(columns, expected) {
			return true
		}
	}
	return false
}

func indexColumns(ctx context.Context, conn *sql.Conn, name string) ([]string, bool, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA index_xinfo(`+quoteIdentifier(name)+`)`)
	if err != nil {
		return nil, false, normalizeDBError(ctx, err)
	}
	defer rows.Close()
	var ordered []struct {
		seq, cid, desc, key int
		col, collation      string
	}
	for rows.Next() {
		var seq, cid, desc, key int
		var column sql.NullString
		var collation string
		if err := rows.Scan(&seq, &cid, &column, &desc, &collation, &key); err != nil {
			return nil, false, normalizeDBError(ctx, err)
		}
		if key == 0 {
			continue
		}
		ordered = append(ordered, struct {
			seq, cid, desc, key int
			col, collation      string
		}{seq, cid, desc, key, column.String, collation})
	}
	if err := rows.Err(); err != nil {
		return nil, false, normalizeDBError(ctx, err)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].seq < ordered[j].seq })
	columns := make([]string, len(ordered))
	semanticsOK := true
	for i := range ordered {
		columns[i] = ordered[i].col
		if ordered[i].cid < 0 || ordered[i].desc != 0 || !strings.EqualFold(ordered[i].collation, "BINARY") {
			semanticsOK = false
		}
	}
	return columns, semanticsOK, nil
}

func validateForeignKeys(ctx context.Context, conn *sql.Conn) error {
	want := map[string]struct{ table, from string }{
		"run":    {"request", "request_id"},
		"round":  {"run", "run_id"},
		"result": {"round", "round_id"},
	}
	for child, expected := range want {
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteIdentifier(child)+`)`)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		count := 0
		for rows.Next() {
			var id, seq int
			var table, from string
			var to sql.NullString
			var onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				rows.Close()
				return normalizeDBError(ctx, err)
			}
			count++
			if id != 0 || seq != 0 || table != expected.table || from != expected.from || to.Valid || onUpdate != "NO ACTION" || onDelete != "CASCADE" || match != "NONE" {
				rows.Close()
				return fmt.Errorf("table %q has an incompatible foreign key", child)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return normalizeDBError(ctx, err)
		}
		if err := rows.Close(); err != nil {
			return normalizeDBError(ctx, err)
		}
		if count != 1 {
			return fmt.Errorf("table %q has %d foreign keys, want one", child, count)
		}
	}
	for _, table := range []string{"meta", "request"} {
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteIdentifier(table)+`)`)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		if rows.Next() {
			rows.Close()
			return fmt.Errorf("table %q has an unexpected foreign key", table)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return normalizeDBError(ctx, err)
		}
		if err := rows.Close(); err != nil {
			return normalizeDBError(ctx, err)
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
