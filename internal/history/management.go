package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"unicode/utf8"
)

var (
	ErrNotFound         = errors.New("history item not found")
	ErrAmbiguousRequest = errors.New("request ID prefix is ambiguous")
	ErrProtectedRun     = errors.New("run is active or cannot be safely verified")
)

type DeleteGuard struct {
	Hostname string
	PIDState func(int64) PIDState
}
type DeleteResult struct {
	Requests, Runs, Rounds, Results int64
	Committed                       bool
}

func OpenExistingWriter(ctx context.Context, path string) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("history write: nil context")
	}
	abs, err := resolveHistoryPath(path)
	if err != nil {
		return nil, fmt.Errorf("history write: %w", err)
	}
	exists, err := historyPathExists(abs)
	if err != nil {
		return nil, wrapHistoryPath("write", abs, err)
	}
	if !exists {
		return nil, nil
	}
	db, err := sql.Open("sqlite3", sqliteURIForAbsoluteMode(abs, "rw"))
	if err != nil {
		return nil, wrapHistoryPath("write", abs, err)
	}
	configurePool(db)
	store := &Store{db: db, path: abs, writable: true}
	if err := store.withTxOperation(ctx, "write", false, func(conn *sql.Conn) error { return validateSchema(ctx, conn) }); err != nil {
		if missing, statErr := historyPathMissing(abs); statErr == nil && missing {
			if closeErr := store.Close(); closeErr != nil {
				return nil, wrapHistoryPath("write", abs, closeErr)
			}
			return nil, nil
		}
		return nil, closeAfterOpenFailure(store, err)
	}
	return store, nil
}

func (s *Store) DeleteRun(ctx context.Context, id string, guard DeleteGuard) (DeleteResult, error) {
	if id == "" || !utf8.ValidString(id) {
		return DeleteResult{}, s.wrap("delete run", errors.New("run ID must be nonempty valid UTF-8"))
	}
	var result DeleteResult
	err := s.withTxOperationCommit(ctx, "delete run", true, func(conn *sql.Conn) error {
		if err := validateSchema(ctx, conn); err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx, `SELECT `+deleteRunSafetyProjection+` FROM run LEFT JOIN request q ON q.id=run.request_id WHERE run.id=?`, id)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		selected, err := summarizeDeleteRuns(ctx, rows, guard)
		if err != nil {
			return err
		}
		if selected.runs == 0 {
			return ErrNotFound
		}
		result.Runs = selected.runs
		result.Rounds = selected.rounds
		result.Results = selected.results
		deleted, err := conn.ExecContext(ctx, `DELETE FROM run WHERE id=?`, id)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		if count, err := deleted.RowsAffected(); err != nil {
			return normalizeDBError(ctx, err)
		} else if count != 1 {
			return errors.New("selected run was not deleted")
		}
		res, err := conn.ExecContext(ctx, `DELETE FROM request WHERE id=? AND NOT EXISTS(SELECT 1 FROM run WHERE request_id=?)`, selected.requestID, selected.requestID)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		result.Requests, err = res.RowsAffected()
		return normalizeDBError(ctx, err)
	}, &result.Committed)
	return finalizedDeleteResult(result, err)
}

func (s *Store) DeleteRequest(ctx context.Context, prefix string, guard DeleteGuard) (DeleteResult, error) {
	if !requestPrefixPattern.MatchString(prefix) {
		return DeleteResult{}, s.wrap("delete request", fmt.Errorf("invalid request ID prefix %q: expected 4 to 64 hexadecimal characters", prefix))
	}
	var result DeleteResult
	err := s.withTxOperationCommit(ctx, "delete request", true, func(conn *sql.Conn) error {
		if err := validateSchema(ctx, conn); err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx, `SELECT CASE WHEN typeof(id)='integer' THEN id ELSE -1 END,CASE WHEN typeof(key)='text' THEN key ELSE '' END,typeof(key) FROM request WHERE lower(substr(key,1,?))=? ORDER BY id`, len(prefix), strings.ToLower(prefix))
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		type requestKey struct {
			id  int64
			key string
		}
		var matches []requestKey
		for rows.Next() {
			var m requestKey
			var keyType string
			if err := rows.Scan(&m.id, &m.key, &keyType); err != nil {
				return closeDeleteRows(ctx, rows, normalizeDBError(ctx, err))
			}
			if keyType != "text" || m.id <= 0 || !validRequestKey(m.key) {
				return closeDeleteRows(ctx, rows, errors.New("matching request has invalid ID or key"))
			}
			matches = append(matches, m)
		}
		if err := rows.Err(); err != nil {
			return closeDeleteRows(ctx, rows, normalizeDBError(ctx, err))
		}
		if err := closeDeleteRows(ctx, rows, nil); err != nil {
			return err
		}
		if len(matches) == 0 {
			return ErrNotFound
		}
		if len(matches) > 1 {
			return ErrAmbiguousRequest
		}
		requestID := matches[0].id
		rows, err = conn.QueryContext(ctx, `SELECT `+deleteRunSafetyProjection+` FROM run LEFT JOIN request q ON q.id=run.request_id WHERE run.request_id=? ORDER BY run.id`, requestID)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		selected, err := summarizeDeleteRuns(ctx, rows, guard)
		if err != nil {
			return err
		}
		result.Requests = 1
		result.Runs = selected.runs
		result.Rounds = selected.rounds
		result.Results = selected.results
		deleted, err := conn.ExecContext(ctx, `DELETE FROM request WHERE id=?`, requestID)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		if count, err := deleted.RowsAffected(); err != nil {
			return normalizeDBError(ctx, err)
		} else if count != 1 {
			return errors.New("selected request was not deleted")
		}
		return nil
	}, &result.Committed)
	return finalizedDeleteResult(result, err)
}

func (s *Store) ClearHistory(ctx context.Context, guard DeleteGuard) (DeleteResult, error) {
	var result DeleteResult
	err := s.withTxOperationCommit(ctx, "clear history", true, func(conn *sql.Conn) error {
		if err := validateSchema(ctx, conn); err != nil {
			return err
		}
		rows, err := conn.QueryContext(ctx, `SELECT `+deleteRunSafetyProjection+` FROM run LEFT JOIN request q ON q.id=run.request_id ORDER BY run.id`)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		selected, err := summarizeDeleteRuns(ctx, rows, guard)
		if err != nil {
			return err
		}
		result.Runs = selected.runs
		for _, table := range []string{"result", "round", "run", "request"} {
			res, err := conn.ExecContext(ctx, "DELETE FROM "+table)
			if err != nil {
				return normalizeDBError(ctx, err)
			}
			count, err := res.RowsAffected()
			if err != nil {
				return normalizeDBError(ctx, err)
			}
			switch table {
			case "result":
				result.Results = count
			case "round":
				result.Rounds = count
			case "run":
				if count != selected.runs {
					return fmt.Errorf("deleted %d runs after validating %d", count, selected.runs)
				}
			case "request":
				result.Requests = count
			}
		}
		return nil
	}, &result.Committed)
	return finalizedDeleteResult(result, err)
}

func finalizedDeleteResult(result DeleteResult, err error) (DeleteResult, error) {
	if !result.Committed {
		return DeleteResult{}, err
	}
	return result, err
}

type deleteRunRow struct {
	id              string
	requestID       int64
	parentPresent   int
	parentKey       string
	parentKeyType   string
	status, host    string
	pid             int64
	ended           sql.NullFloat64
	rounds, results int64
}

type deleteRunSummary struct{ requestID, runs, rounds, results int64 }

const deleteRunSafetyProjection = `
CASE WHEN typeof(run.id)='text' THEN run.id ELSE '' END,
CASE WHEN typeof(run.request_id)='integer' THEN run.request_id ELSE -1 END,
CASE WHEN typeof(q.id)='integer' THEN 1 ELSE 0 END,
CASE WHEN typeof(q.key)='text' THEN q.key ELSE '' END, typeof(q.key),
CASE WHEN typeof(run.status)='text' THEN run.status ELSE '' END, typeof(run.status),
CASE WHEN typeof(run.host)='text' THEN run.host ELSE '' END, typeof(run.host),
CASE WHEN typeof(run.pid)='integer' THEN run.pid ELSE -1 END, typeof(run.pid),
CASE WHEN typeof(run.ended) IN ('integer','real') THEN run.ended ELSE NULL END, typeof(run.ended),
(SELECT COUNT(*) FROM round WHERE run_id=run.id),
(SELECT COUNT(*) FROM result WHERE round_id IN (SELECT id FROM round WHERE run_id=run.id))`

func summarizeDeleteRuns(ctx context.Context, rows *sql.Rows, guard DeleteGuard) (deleteRunSummary, error) {
	var summary deleteRunSummary
	for rows.Next() {
		var r deleteRunRow
		var statusType, hostType, pidType, endedType string
		if err := rows.Scan(&r.id, &r.requestID, &r.parentPresent, &r.parentKey, &r.parentKeyType, &r.status, &statusType, &r.host, &hostType, &r.pid, &pidType, &r.ended, &endedType, &r.rounds, &r.results); err != nil {
			return summary, closeDeleteRows(ctx, rows, normalizeDBError(ctx, err))
		}
		if statusType != "text" || hostType != "text" || pidType != "integer" || (endedType != "null" && endedType != "integer" && endedType != "real") || r.id == "" || !utf8.ValidString(r.id) || r.requestID <= 0 || r.parentPresent != 1 || r.parentKeyType != "text" || !validRequestKey(r.parentKey) || !utf8.ValidString(r.status) || !utf8.ValidString(r.host) || r.rounds < 0 || r.results < 0 {
			return summary, closeDeleteRows(ctx, rows, errors.New("run or parent request has invalid safety fields"))
		}
		if err := guardDeleteRun(r, guard); err != nil {
			return summary, closeDeleteRows(ctx, rows, err)
		}
		if summary.runs == 0 {
			summary.requestID = r.requestID
		}
		summary.runs++
		summary.rounds += r.rounds
		summary.results += r.results
	}
	if err := rows.Err(); err != nil {
		return summary, closeDeleteRows(ctx, rows, normalizeDBError(ctx, err))
	}
	if err := closeDeleteRows(ctx, rows, nil); err != nil {
		return summary, err
	}
	return summary, nil
}

func guardDeleteRun(r deleteRunRow, guard DeleteGuard) error {
	switch r.status {
	case "running":
		if r.ended.Valid {
			return fmt.Errorf("run %q has an end time while status is running", r.id)
		}
		if guard.Hostname == "" || !utf8.ValidString(guard.Hostname) || r.host == "" || r.host != guard.Hostname || !representablePID(r.pid) || guard.PIDState == nil || guard.PIDState(r.pid) != PIDDead {
			return fmt.Errorf("run %q: %w", r.id, ErrProtectedRun)
		}
	case "finished", "stopped", "failed":
		if !r.ended.Valid {
			return fmt.Errorf("run %q has invalid end time", r.id)
		}
		if _, err := timeFromEpoch(r.ended.Float64); err != nil {
			return fmt.Errorf("run %q has invalid end time: %w", r.id, err)
		}
	default:
		return fmt.Errorf("run %q has invalid status %q", r.id, r.status)
	}
	return nil
}
func representablePID(pid int64) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return uint64(pid) <= math.MaxUint32
	}
	return pid <= math.MaxInt32
}
func closeDeleteRows(ctx context.Context, rows *sql.Rows, primary error) error {
	if e := rows.Close(); e != nil {
		e = markCleanup(normalizeDBError(ctx, e))
		if primary != nil {
			return errors.Join(primary, e)
		}
		return e
	}
	return primary
}
