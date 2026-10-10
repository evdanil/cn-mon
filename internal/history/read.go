// Read queries and spacing adapt the request/round readers in
// cn_tool/utils/ping_store.py:_request_infos, rounds, and round_starts
// (cn-tool 0.8.0, commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa).
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxReadPage = 500
const maxRoundPageHostBudget = 65536

var requestPrefixPattern = regexp.MustCompile(`(?i)^[0-9a-f]{4,64}$`)

// Snapshot is valid only for the duration of Store.View's callback.
type Snapshot struct {
	conn    *sql.Conn
	ctx     context.Context
	through int64
	active  bool
}

// View runs callback under one read transaction and fixes the committed round
// high-water mark before any snapshot data is exposed.
func (s *Store) View(ctx context.Context, callback func(*Snapshot) error) error {
	return s.withTx(ctx, false, func(conn *sql.Conn) error {
		if callback == nil {
			return errors.New("nil history snapshot callback")
		}
		var through int64
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM round`).Scan(&through); err != nil {
			return normalizeDBError(ctx, err)
		}
		if through < 0 {
			return errors.New("invalid negative round high-water mark")
		}
		snapshot := &Snapshot{conn: conn, ctx: ctx, through: through, active: true}
		defer func() { snapshot.active = false }()
		return callback(snapshot)
	})
}

func (s *Snapshot) checkActive() error {
	if s == nil || !s.active || s.conn == nil {
		return errors.New("history snapshot is not active")
	}
	if s.ctx == nil {
		return errors.New("history snapshot has no context")
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *Snapshot) queryRows(query string, args ...any) (*sql.Rows, error) {
	if err := s.checkActive(); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(s.ctx, query, args...)
	if err != nil {
		return nil, normalizeDBError(s.ctx, err)
	}
	return rows, nil
}

func (s *Snapshot) normalize(err error) error {
	return normalizeDBError(s.ctx, err)
}

func (s *Snapshot) closeRows(rows *sql.Rows) error {
	return s.normalize(rows.Close())
}

func (s *Snapshot) failRows(rows *sql.Rows, err error) error {
	primary := err
	if closeErr := rows.Close(); closeErr != nil {
		primary = errors.Join(primary, s.normalize(closeErr))
	}
	return primary
}

func (s *Snapshot) Find(prefix string) ([]RequestMatch, error) {
	if err := s.checkActive(); err != nil {
		return nil, err
	}
	if !requestPrefixPattern.MatchString(prefix) {
		return nil, fmt.Errorf("invalid request ID prefix %q: expected 4 to 64 hexadecimal characters", prefix)
	}
	query := requestActivityCTE + `
SELECT id, key FROM request_rows
WHERE lower(substr(key, 1, ?)) = ?
ORDER BY activity IS NULL, activity DESC, id DESC`
	rows, err := s.queryRows(query, s.through, len(prefix), strings.ToLower(prefix))
	if err != nil {
		return nil, err
	}
	var matches []RequestMatch
	for rows.Next() {
		var match RequestMatch
		if err := rows.Scan(&match.ID, &match.Key); err != nil {
			return nil, s.failRows(rows, s.normalize(err))
		}
		if match.ID <= 0 || !validRequestKey(match.Key) {
			return nil, s.failRows(rows, errors.New("request row has an invalid ID or key"))
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, s.failRows(rows, s.normalize(err))
	}
	if err := s.closeRows(rows); err != nil {
		return nil, err
	}
	return matches, nil
}

func (s *Snapshot) Request(key string) (*RequestInfo, error) {
	if err := s.checkActive(); err != nil {
		return nil, err
	}
	if !validRequestKey(key) {
		return nil, fmt.Errorf("invalid full request key")
	}
	items, _, err := s.requestPage(`WHERE key = ?`, []any{key}, 1, false)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	info := &RequestInfo{RequestSummary: items[0].summary}
	var storedTargets, storedPorts, storedHosts string
	err = s.conn.QueryRowContext(s.ctx, `SELECT targets, tcp_ports, hosts FROM request WHERE id = ?`, info.ID).Scan(&storedTargets, &storedPorts, &storedHosts)
	if err != nil {
		return nil, s.normalize(err)
	}
	request, err := decodeRequest(info.ID, info.Key, storedTargets, storedPorts, storedHosts)
	if err != nil {
		return nil, fmt.Errorf("invalid stored request: %w", err)
	}
	info.Hosts = request.Hosts
	return info, nil
}

func (s *Snapshot) Requests(after *RequestCursor, limit int) (RequestPage, error) {
	if err := s.checkActive(); err != nil {
		return RequestPage{}, err
	}
	limit, err := readPageLimit(limit)
	if err != nil {
		return RequestPage{}, err
	}
	where := ""
	var args []any
	if after != nil {
		if !validRequestCursor(after) {
			return RequestPage{}, errors.New("invalid request cursor")
		}
		if after.hasActivity {
			where = `WHERE activity IS NULL OR activity < ? OR (activity = ? AND id < ?)`
			args = append(args, after.activity, after.activity, after.id)
		} else {
			where = `WHERE activity IS NULL AND id < ?`
			args = append(args, after.id)
		}
	}
	items, more, err := s.requestPage(where, args, limit, true)
	if err != nil {
		return RequestPage{}, err
	}
	page := RequestPage{Items: make([]RequestSummary, len(items))}
	for i := range items {
		page.Items[i] = items[i].summary
	}
	if more && len(items) != 0 {
		last := items[len(items)-1]
		page.Next = &RequestCursor{activity: last.activity.Float64, hasActivity: last.activity.Valid, id: last.summary.ID}
	}
	return page, nil
}

type requestSummaryRow struct {
	summary  RequestSummary
	activity sql.NullFloat64
}

func (s *Snapshot) requestPage(where string, args []any, limit int, paged bool) ([]requestSummaryRow, bool, error) {
	query := requestSummaryCTE + `
SELECT id, key, targets, tcp_ports, hosts, run_count, round_count, first_round, last_round,
       newest_id, newest_source, newest_started, newest_interval, newest_until, newest_host,
       newest_pid, newest_heartbeat, newest_rounds, newest_round_seconds, newest_ended, newest_status,
       activity
FROM request_summary ` + where + ` ORDER BY activity IS NULL, activity DESC, id DESC`
	if paged {
		query += ` LIMIT ?`
	}
	queryArgs := make([]any, 0, len(args)+2)
	queryArgs = append(queryArgs, s.through)
	queryArgs = append(queryArgs, args...)
	if paged {
		queryArgs = append(queryArgs, limit+1)
	}
	rows, err := s.queryRows(query, queryArgs...)
	if err != nil {
		return nil, false, err
	}
	items := make([]requestSummaryRow, 0, min(limit+1, maxReadPage+1))
	for rows.Next() {
		item, err := readRequestSummary(s.ctx, rows)
		if err != nil {
			return nil, false, s.failRows(rows, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, s.failRows(rows, s.normalize(err))
	}
	if err := s.closeRows(rows); err != nil {
		return nil, false, err
	}
	more := paged && len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func readPageLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, errors.New("page limit cannot be negative")
	}
	if limit == 0 || limit > maxReadPage {
		return maxReadPage, nil
	}
	return limit, nil
}

func validRequestCursor(cursor *RequestCursor) bool {
	if cursor == nil || cursor.id <= 0 {
		return false
	}
	return !cursor.hasActivity || finiteFloat(cursor.activity)
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

const requestActivityCTE = `WITH params(through) AS (VALUES (?)), request_activity AS (
 SELECT q.id, q.key, q.targets, q.tcp_ports, q.hosts,
        (SELECT MAX(rd.started) FROM round rd JOIN run rr ON rr.id=rd.run_id
         WHERE rr.request_id=q.id AND rd.id <= (SELECT through FROM params)) AS last_round,
        (SELECT r.rowid FROM run r WHERE r.request_id=q.id
         ORDER BY r.started DESC, r.rowid DESC LIMIT 1) AS newest_rowid
 FROM request q
), request_rows AS (
 SELECT a.id, a.key, a.targets, a.tcp_ports, a.hosts, a.last_round,
        n.id AS newest_id, n.source AS newest_source, n.started AS newest_started,
        n.interval AS newest_interval, n.until AS newest_until, n.host AS newest_host,
        n.pid AS newest_pid, n.heartbeat AS newest_heartbeat,
        n.round_seconds AS newest_round_seconds, n.ended AS newest_ended, n.status AS newest_status,
        CASE WHEN a.last_round IS NULL THEN n.started
             WHEN n.started IS NULL THEN a.last_round
             WHEN a.last_round >= n.started THEN a.last_round ELSE n.started END AS activity
 FROM request_activity a LEFT JOIN run n ON n.rowid=a.newest_rowid
)`

const requestSummaryCTE = requestActivityCTE + `,
request_summary AS (
 SELECT request_rows.*,
        (SELECT COUNT(*) FROM run r WHERE r.request_id=request_rows.id) AS run_count,
        (SELECT COUNT(*) FROM round rd JOIN run rr ON rr.id=rd.run_id
         WHERE rr.request_id=request_rows.id AND rd.id <= (SELECT through FROM params)) AS round_count,
        (SELECT MIN(rd.started) FROM round rd JOIN run rr ON rr.id=rd.run_id
         WHERE rr.request_id=request_rows.id AND rd.id <= (SELECT through FROM params)) AS first_round,
        (SELECT MAX(rd.started) FROM round rd JOIN run rr ON rr.id=rd.run_id
         WHERE rr.request_id=request_rows.id AND rd.id <= (SELECT through FROM params)) AS last_round_summary,
        CASE WHEN request_rows.newest_started IS NULL THEN NULL ELSE
        (SELECT COUNT(*) FROM round rd WHERE rd.run_id=request_rows.newest_id
         AND rd.id <= (SELECT through FROM params)) END AS newest_rounds
 FROM request_rows
)`

func readRequestSummary(ctx context.Context, scanner interface{ Scan(...any) error }) (requestSummaryRow, error) {
	var row requestSummaryRow
	var id, runs, rounds int64
	var key, targets, ports, hosts string
	var firstRound, lastRound sql.NullFloat64
	var newestID, newestSource, newestHost, newestStatus sql.NullString
	var newestStarted, newestInterval, newestUntil, newestHeartbeat, newestRoundSeconds, newestEnded sql.NullFloat64
	var newestPID, newestRounds sql.NullInt64
	err := scanner.Scan(
		&id, &key, &targets, &ports, &hosts, &runs, &rounds, &firstRound, &lastRound,
		&newestID, &newestSource, &newestStarted, &newestInterval, &newestUntil, &newestHost,
		&newestPID, &newestHeartbeat, &newestRounds, &newestRoundSeconds, &newestEnded, &newestStatus,
		&row.activity,
	)
	if err != nil {
		return row, normalizeDBError(ctx, err)
	}
	// Validate the complete stored request; keep only its list metadata in the page.
	request, err := decodeRequest(id, key, targets, ports, hosts)
	if err != nil {
		return row, fmt.Errorf("invalid request summary: %w", err)
	}
	meta := request.RequestMeta
	if runs < 0 || rounds < 0 || (row.activity.Valid && !finiteFloat(row.activity.Float64)) {
		return row, errors.New("request summary contains invalid counts or activity")
	}
	row.summary = RequestSummary{RequestMeta: meta, Runs: runs, Rounds: rounds}
	if row.summary.FirstRound, err = nullableTime(firstRound); err != nil {
		return row, fmt.Errorf("invalid first round time: %w", err)
	}
	if row.summary.LastRound, err = nullableTime(lastRound); err != nil {
		return row, fmt.Errorf("invalid last round time: %w", err)
	}
	hasNewestValues := newestID.Valid || newestStarted.Valid || newestInterval.Valid || newestUntil.Valid || newestHost.Valid || newestPID.Valid || newestHeartbeat.Valid || newestRounds.Valid || newestRoundSeconds.Valid || newestEnded.Valid || newestStatus.Valid
	if (runs == 0 && hasNewestValues) || (runs > 0 && !newestID.Valid) {
		return row, errors.New("newest run fields do not match the stored run count")
	}
	if newestID.Valid {
		if !newestStarted.Valid || !newestHeartbeat.Valid || !newestPID.Valid || !newestHost.Valid || !newestStatus.Valid || !newestRounds.Valid {
			return row, errors.New("newest run has missing required fields")
		}
		if newestID.String == "" || newestRounds.Int64 < 0 || !utf8.ValidString(newestID.String) || !utf8.ValidString(newestHost.String) || !utf8.ValidString(newestStatus.String) || (newestSource.Valid && !utf8.ValidString(newestSource.String)) {
			return row, errors.New("newest run has invalid text or round count")
		}
		if !validStoredRunStatus(newestStatus.String) {
			return row, fmt.Errorf("unknown stored run status %q", newestStatus.String)
		}
		run := &RunInfo{RunStart: RunStart{ID: newestID.String, Source: nullableStringPointer(newestSource), Host: newestHost.String, PID: newestPID.Int64}, RequestID: id, Status: newestStatus.String, Rounds: newestRounds.Int64}
		if run.Started, err = timeFromEpoch(newestStarted.Float64); err != nil {
			return row, fmt.Errorf("invalid newest run start: %w", err)
		}
		if run.Heartbeat, err = timeFromEpoch(newestHeartbeat.Float64); err != nil {
			return row, fmt.Errorf("invalid newest run heartbeat: %w", err)
		}
		if run.Interval, err = nullableDuration(newestInterval); err != nil {
			return row, fmt.Errorf("invalid newest run interval: %w", err)
		}
		if run.Until, err = nullableTime(newestUntil); err != nil {
			return row, fmt.Errorf("invalid newest run end time: %w", err)
		}
		if run.RoundSeconds, err = nullableDuration(newestRoundSeconds); err != nil {
			return row, fmt.Errorf("invalid newest run round duration: %w", err)
		}
		if run.Ended, err = nullableTime(newestEnded); err != nil {
			return row, fmt.Errorf("invalid newest run finish time: %w", err)
		}
		row.summary.NewestRun = run
	}
	return row, nil
}

func nullableTime(value sql.NullFloat64) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	t, err := timeFromEpoch(value.Float64)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullableDuration(value sql.NullFloat64) (*time.Duration, error) {
	if !value.Valid {
		return nil, nil
	}
	d, err := durationFromSeconds(value.Float64)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func validStoredRunStatus(status string) bool {
	switch status {
	case "running", "finished", "stopped", "failed":
		return true
	default:
		return false
	}
}

func (s *Snapshot) Runs(requestID int64, runningOnly bool, after *RunCursor, limit int) (RunPage, error) {
	if err := s.checkActive(); err != nil {
		return RunPage{}, err
	}
	if requestID <= 0 {
		return RunPage{}, errors.New("request ID must be positive")
	}
	limit, err := readPageLimit(limit)
	if err != nil {
		return RunPage{}, err
	}
	if after != nil && (after.rowID <= 0 || !finiteFloat(after.started)) {
		return RunPage{}, errors.New("invalid run cursor")
	}
	if runningOnly {
		var invalidStatus sql.NullString
		err := s.conn.QueryRowContext(s.ctx, `SELECT status FROM run
WHERE request_id = ? AND (status IS NULL OR status NOT IN ('running','finished','stopped','failed'))
LIMIT 1`, requestID).Scan(&invalidStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return RunPage{}, s.normalize(err)
		}
		if err == nil {
			if !invalidStatus.Valid {
				return RunPage{}, errors.New("request contains an invalid stored run status")
			}
			return RunPage{}, fmt.Errorf("unknown stored run status %q", invalidStatus.String)
		}
	}
	query := `SELECT r.id, r.request_id, r.source, r.started, r.interval, r.until,
       r.host, r.pid, r.heartbeat, r.round_seconds, r.ended, r.status,
       (SELECT COUNT(*) FROM round rd WHERE rd.run_id=r.id AND rd.id <= ?), r.rowid
FROM run r WHERE r.request_id = ?`
	args := []any{s.through, requestID}
	if runningOnly {
		query += ` AND r.status='running'`
	}
	if after != nil {
		query += ` AND (r.started < ? OR (r.started = ? AND r.rowid < ?))`
		args = append(args, after.started, after.started, after.rowID)
	}
	query += ` ORDER BY r.started DESC, r.rowid DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.queryRows(query, args...)
	if err != nil {
		return RunPage{}, err
	}
	items := make([]runRowData, 0, min(limit+1, maxReadPage+1))
	for rows.Next() {
		item, err := readRunInfo(s.ctx, rows)
		if err != nil {
			return RunPage{}, s.failRows(rows, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RunPage{}, s.failRows(rows, s.normalize(err))
	}
	if err := s.closeRows(rows); err != nil {
		return RunPage{}, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	page := RunPage{Items: make([]RunInfo, len(items))}
	for i, item := range items {
		page.Items[i] = item.info
	}
	if more && len(items) != 0 {
		last := items[len(items)-1]
		page.Next = &RunCursor{started: last.started, rowID: last.rowID}
	}
	return page, nil
}

func readRunInfo(ctx context.Context, scanner interface{ Scan(...any) error }) (runRowData, error) {
	var row runRowData
	var id, host, status string
	var requestID, pid, rounds, rowID int64
	var source sql.NullString
	var started, interval, until, heartbeat, roundSeconds, ended sql.NullFloat64
	if err := scanner.Scan(&id, &requestID, &source, &started, &interval, &until, &host, &pid, &heartbeat, &roundSeconds, &ended, &status, &rounds, &rowID); err != nil {
		return row, normalizeDBError(ctx, err)
	}
	if id == "" || requestID <= 0 || !started.Valid || !heartbeat.Valid || rounds < 0 || rowID <= 0 || !utf8.ValidString(id) || !utf8.ValidString(host) || !utf8.ValidString(status) || (source.Valid && !utf8.ValidString(source.String)) {
		return row, errors.New("stored run has invalid required fields")
	}
	if !validStoredRunStatus(status) {
		return row, fmt.Errorf("unknown stored run status %q", status)
	}
	info := RunInfo{RunStart: RunStart{ID: id, Source: nullableStringPointer(source), Host: host, PID: pid}, RequestID: requestID, Status: status, Rounds: rounds}
	var err error
	if info.Started, err = timeFromEpoch(started.Float64); err != nil {
		return row, fmt.Errorf("invalid run start: %w", err)
	}
	if info.Heartbeat, err = timeFromEpoch(heartbeat.Float64); err != nil {
		return row, fmt.Errorf("invalid run heartbeat: %w", err)
	}
	if info.Interval, err = nullableDuration(interval); err != nil {
		return row, fmt.Errorf("invalid run interval: %w", err)
	}
	if info.Until, err = nullableTime(until); err != nil {
		return row, fmt.Errorf("invalid run end time: %w", err)
	}
	if info.RoundSeconds, err = nullableDuration(roundSeconds); err != nil {
		return row, fmt.Errorf("invalid run round duration: %w", err)
	}
	if info.Ended, err = nullableTime(ended); err != nil {
		return row, fmt.Errorf("invalid run finish time: %w", err)
	}
	row.info, row.started, row.rowID = info, started.Float64, rowID
	return row, nil
}

type runRowData struct {
	info    RunInfo
	started float64
	rowID   int64
}

type roundHeader struct {
	id, answered        int64
	runID, observations string
	started             float64
	interval            sql.NullFloat64
}

// Window reports the selected stored-round count and exact raw endpoint keys.
func (s *Snapshot) Window(requestID int64, filter WindowFilter) (WindowInfo, error) {
	if err := s.checkActive(); err != nil {
		return WindowInfo{}, err
	}
	if requestID <= 0 {
		return WindowInfo{}, errors.New("request ID must be positive")
	}
	since, before, err := encodeWindowFilter(filter)
	if err != nil {
		return WindowInfo{}, err
	}
	info := WindowInfo{Through: s.through}
	var firstStarted, lastStarted sql.NullFloat64
	var firstID, lastID sql.NullInt64
	err = s.conn.QueryRowContext(s.ctx, `
SELECT COUNT(*), MIN(rd.started), MAX(rd.started),
       (SELECT id FROM round WHERE run_id IN (SELECT id FROM run WHERE request_id=?)
          AND id <= ? AND (? IS NULL OR started >= ?) AND (? IS NULL OR started < ?)
          ORDER BY started ASC, id ASC LIMIT 1),
       (SELECT id FROM round WHERE run_id IN (SELECT id FROM run WHERE request_id=?)
          AND id <= ? AND (? IS NULL OR started >= ?) AND (? IS NULL OR started < ?)
          ORDER BY started DESC, id DESC LIMIT 1)
FROM round rd JOIN run r ON r.id=rd.run_id
WHERE r.request_id=? AND rd.id<=? AND (? IS NULL OR rd.started>=?) AND (? IS NULL OR rd.started<?)`,
		requestID, s.through, since, since, before, before,
		requestID, s.through, since, since, before, before,
		requestID, s.through, since, since, before, before,
	).Scan(&info.Count, &firstStarted, &lastStarted, &firstID, &lastID)
	if err != nil {
		return WindowInfo{}, s.normalize(err)
	}
	if info.Count < 0 {
		return WindowInfo{}, errors.New("stored round count is invalid")
	}
	if info.Count == 0 {
		if firstStarted.Valid || lastStarted.Valid || firstID.Valid || lastID.Valid {
			return WindowInfo{}, errors.New("empty round window has endpoints")
		}
		return info, nil
	}
	if !firstStarted.Valid || !lastStarted.Valid || !firstID.Valid || !lastID.Valid || firstID.Int64 <= 0 || lastID.Int64 <= 0 || !finiteFloat(firstStarted.Float64) || !finiteFloat(lastStarted.Float64) {
		return WindowInfo{}, errors.New("stored round window has invalid endpoints")
	}
	firstTime, err := timeFromEpoch(firstStarted.Float64)
	if err != nil {
		return WindowInfo{}, fmt.Errorf("invalid first round time: %w", err)
	}
	lastTime, err := timeFromEpoch(lastStarted.Float64)
	if err != nil {
		return WindowInfo{}, fmt.Errorf("invalid last round time: %w", err)
	}
	info.First = &RoundCursor{started: firstStarted.Float64, id: firstID.Int64}
	info.Last = &RoundCursor{started: lastStarted.Float64, id: lastID.Int64}
	info.FirstStarted, info.LastStarted = &firstTime, &lastTime
	return info, nil
}

// Rounds reads at most one adaptive page of headers and then only those
// rounds' details, all within the View transaction.
func (s *Snapshot) Rounds(requestID int64, filter RoundFilter) (RoundPage, error) {
	if err := s.checkActive(); err != nil {
		return RoundPage{}, err
	}
	if requestID <= 0 {
		return RoundPage{}, errors.New("request ID must be positive")
	}
	if filter.NewerThan < 0 {
		return RoundPage{}, errors.New("newer-than round ID cannot be negative")
	}
	limit, err := readPageLimit(filter.Limit)
	if err != nil {
		return RoundPage{}, err
	}
	if filter.After != nil && (filter.After.id <= 0 || !finiteFloat(filter.After.started)) {
		return RoundPage{}, errors.New("invalid round cursor")
	}
	since, before, err := encodeWindowFilter(filter.WindowFilter)
	if err != nil {
		return RoundPage{}, err
	}
	var key, targetsJSON, portsJSON, hostsJSON string
	err = s.conn.QueryRowContext(s.ctx, `SELECT key, targets, tcp_ports, hosts FROM request WHERE id=?`, requestID).Scan(&key, &targetsJSON, &portsJSON, &hostsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundPage{Items: []StoredRound{}}, nil
	}
	if err != nil {
		return RoundPage{}, s.normalize(err)
	}
	request, err := decodeRequest(requestID, key, targetsJSON, portsJSON, hostsJSON)
	if err != nil {
		return RoundPage{}, fmt.Errorf("stored request is invalid: %w", err)
	}
	hosts := request.Hosts
	effective := roundPageLimit(limit, len(hosts))
	query := `SELECT rd.id, rd.run_id, rd.started, rd.observations, rd.answered, r.interval
FROM round rd JOIN run r ON r.id=rd.run_id
WHERE r.request_id=? AND rd.id<=? AND rd.id>? AND (? IS NULL OR rd.started>=?) AND (? IS NULL OR rd.started<?)`
	args := []any{requestID, s.through, filter.NewerThan, since, since, before, before}
	if filter.After != nil {
		query += ` AND (rd.started>? OR (rd.started=? AND rd.id>?))`
		args = append(args, filter.After.started, filter.After.started, filter.After.id)
	}
	query += ` ORDER BY rd.started ASC, rd.id ASC LIMIT ?`
	args = append(args, effective+1)
	rows, err := s.queryRows(query, args...)
	if err != nil {
		return RoundPage{}, err
	}
	headers := make([]roundHeader, 0, effective+1)
	for rows.Next() {
		var item roundHeader
		if err := rows.Scan(&item.id, &item.runID, &item.started, &item.observations, &item.answered, &item.interval); err != nil {
			return RoundPage{}, s.failRows(rows, s.normalize(err))
		}
		if item.id <= 0 || item.runID == "" || !utf8.ValidString(item.runID) || !utf8.ValidString(item.observations) || item.answered < 0 || !finiteFloat(item.started) {
			return RoundPage{}, s.failRows(rows, errors.New("stored round header is invalid"))
		}
		if _, err := timeFromEpoch(item.started); err != nil {
			return RoundPage{}, s.failRows(rows, fmt.Errorf("invalid stored round time: %w", err))
		}
		if item.interval.Valid {
			if _, err := durationFromSeconds(item.interval.Float64); err != nil {
				return RoundPage{}, s.failRows(rows, fmt.Errorf("invalid stored run interval: %w", err))
			}
		}
		headers = append(headers, item)
	}
	if err := rows.Err(); err != nil {
		return RoundPage{}, s.failRows(rows, s.normalize(err))
	}
	if err := s.closeRows(rows); err != nil {
		return RoundPage{}, err
	}
	more := len(headers) > effective
	if more {
		headers = headers[:effective]
	}
	page := RoundPage{Items: make([]StoredRound, 0, len(headers))}
	for _, item := range headers {
		stored, err := s.readRoundDetails(item, hosts)
		if err != nil {
			return RoundPage{}, err
		}
		page.Items = append(page.Items, stored)
	}
	if more && len(headers) != 0 {
		last := headers[len(headers)-1]
		page.Next = &RoundCursor{started: last.started, id: last.id}
	}
	return page, nil
}

func roundPageLimit(limit, hostCount int) int {
	if limit == 0 || limit > maxReadPage {
		limit = maxReadPage
	}
	effective := limit
	if hostCount > 0 {
		effective = min(effective, max(1, maxRoundPageHostBudget/hostCount))
	}
	return effective
}

func (s *Snapshot) readRoundDetails(item roundHeader, hosts []string) (StoredRound, error) {
	started, err := timeFromEpoch(item.started)
	if err != nil {
		return StoredRound{}, fmt.Errorf("invalid stored round time: %w", err)
	}
	interval, err := nullableDuration(item.interval)
	if err != nil {
		return StoredRound{}, fmt.Errorf("invalid stored run interval: %w", err)
	}
	seenHosts, err := roundSeenHosts(item.observations, hosts)
	if err != nil {
		return StoredRound{}, fmt.Errorf("invalid stored round header: %w", err)
	}
	if int64(len(seenHosts)) != item.answered {
		return StoredRound{}, errors.New("stored round answered count does not match observations")
	}
	rows, err := s.queryRows(`SELECT host, address, t, result, tcp FROM result WHERE round_id=? ORDER BY host`, item.id)
	if err != nil {
		return StoredRound{}, err
	}
	results := make([]ResultRow, 0, min(len(hosts), maxReadPage))
	for rows.Next() {
		var host, address, result string
		var seconds float64
		var tcp sql.NullString
		if err := rows.Scan(&host, &address, &seconds, &result, &tcp); err != nil {
			return StoredRound{}, s.failRows(rows, s.normalize(err))
		}
		if _, ok := seenHosts[host]; !ok {
			return StoredRound{}, s.failRows(rows, fmt.Errorf("unexpected or unseen result host %q", host))
		}
		delete(seenHosts, host)
		if int64(len(results)) >= item.answered {
			return StoredRound{}, s.failRows(rows, errors.New("stored round has more detail rows than answered hosts"))
		}
		if !utf8.ValidString(host) || !utf8.ValidString(address) || !utf8.ValidString(result) || !finiteFloat(seconds) {
			return StoredRound{}, s.failRows(rows, errors.New("stored result contains invalid text or time"))
		}
		at, err := timeFromEpoch(seconds)
		if err != nil {
			return StoredRound{}, s.failRows(rows, fmt.Errorf("invalid stored result time: %w", err))
		}
		decodedTCP, err := decodeTCP(tcp)
		if err != nil {
			return StoredRound{}, s.failRows(rows, fmt.Errorf("invalid stored TCP result: %w", err))
		}
		results = append(results, ResultRow{Host: host, Address: address, Time: at, Result: result, TCP: decodedTCP})
	}
	if err := rows.Err(); err != nil {
		return StoredRound{}, s.failRows(rows, s.normalize(err))
	}
	if err := s.closeRows(rows); err != nil {
		return StoredRound{}, err
	}
	if item.answered != int64(len(results)) {
		return StoredRound{}, errors.New("stored round answered count does not match result rows")
	}
	if len(seenHosts) != 0 {
		return StoredRound{}, errors.New("stored round is missing detail rows for answered hosts")
	}
	input := RoundInput{Started: started, Observations: item.observations, Rows: results}
	if err := validateRound(hosts, input); err != nil {
		return StoredRound{}, fmt.Errorf("invalid stored round: %w", err)
	}
	return StoredRound{RoundInput: input, ID: item.id, RunID: item.runID, Interval: interval, Answered: item.answered, Cursor: RoundCursor{started: item.started, id: item.id}}, nil
}

func roundSeenHosts(observations string, hosts []string) (map[string]struct{}, error) {
	if len(observations) != len(hosts) {
		return nil, errors.New("observation count does not match host count")
	}
	seen := make(map[string]struct{})
	for index, state := range []byte(observations) {
		switch state {
		case '!', 'R', 'T':
			seen[hosts[index]] = struct{}{}
		case 'U', '.', 'E', '?':
		default:
			return nil, fmt.Errorf("invalid observation byte %q", state)
		}
	}
	return seen, nil
}

func (s *Snapshot) Spacing(requestID int64, filter WindowFilter, extraStart *time.Time) (time.Duration, error) {
	if err := s.checkActive(); err != nil {
		return 0, err
	}
	if requestID <= 0 {
		return 0, errors.New("request ID must be positive")
	}
	since, before, err := encodeWindowFilter(filter)
	if err != nil {
		return 0, err
	}
	var extra any
	var hasExtra int
	if extraStart != nil {
		value, err := epochSeconds(*extraStart)
		if err != nil {
			return 0, fmt.Errorf("invalid extra spacing start: %w", err)
		}
		extra, hasExtra = value, 1
	}
	var invalidTimestampCount, gapCount int64
	var median sql.NullFloat64
	err = s.conn.QueryRowContext(s.ctx, `WITH raw_selected(started, valid) AS (
	 SELECT rd.started, CASE WHEN typeof(rd.started) IN ('integer','real') AND rd.started>=-62135596800.0 AND rd.started<253402300800.0 THEN 1 ELSE 0 END
	 FROM round rd JOIN run r ON r.id=rd.run_id
 WHERE r.request_id=? AND rd.id<=? AND r.interval IS NULL
   AND (? IS NULL OR rd.started>=?) AND (? IS NULL OR rd.started<?)
	 UNION ALL SELECT ?, 1 WHERE ?=1
	), invalid AS (
	 SELECT COUNT(*) AS count FROM raw_selected WHERE valid=0
	), selected(started) AS (
	 SELECT CAST(started AS REAL) FROM raw_selected WHERE valid=1
), gaps(gap) AS (
 SELECT started-LAG(started) OVER (ORDER BY started) FROM selected
), ranked AS (
 SELECT gap, ROW_NUMBER() OVER (ORDER BY gap) AS pos, COUNT(*) OVER () AS n
 FROM gaps WHERE gap IS NOT NULL
)
	SELECT (SELECT count FROM invalid), COUNT(*), (SELECT gap FROM ranked WHERE pos=(n+1)/2)
	FROM ranked`,
		requestID, s.through, since, since, before, before, extra, hasExtra,
	).Scan(&invalidTimestampCount, &gapCount, &median)
	if err != nil {
		return 0, s.normalize(err)
	}
	if invalidTimestampCount != 0 {
		return 0, errors.New("invalid stored round timestamp")
	}
	if gapCount < 2 {
		return 0, nil
	}
	if !median.Valid || !finiteFloat(median.Float64) || median.Float64 < 0 {
		return 0, errors.New("stored spacing result is invalid")
	}
	duration, err := durationFromSeconds(median.Float64)
	if err != nil {
		return 0, fmt.Errorf("stored spacing is outside duration range: %w", err)
	}
	return duration, nil
}

func encodeWindowFilter(filter WindowFilter) (any, any, error) {
	var since, before any
	if filter.Since != nil {
		value, err := epochSeconds(*filter.Since)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid since time: %w", err)
		}
		since = value
	}
	if filter.Before != nil {
		value, err := epochSeconds(*filter.Before)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid before time: %w", err)
		}
		before = value
	}
	return since, before, nil
}
