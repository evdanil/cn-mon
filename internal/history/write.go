package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// Write operations adapt PingStore.begin_run, record_round, heartbeat and
// end_run at cn_tool/utils/ping_store.py:363-418, pinned cn-tool v0.8.0,
// commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa (Apache-2.0).
func (s *Store) BeginRun(ctx context.Context, request Request, run RunStart) (int64, error) {
	targetsJSON, portsJSON, hostsJSON, err := encodeRequest(request)
	if err != nil {
		return 0, s.wrap("write", err)
	}
	if run.ID == "" || !utf8.ValidString(run.ID) || !utf8.ValidString(run.Host) {
		return 0, s.wrap("write", errors.New("run ID must be nonempty and run ID/host must be valid UTF-8"))
	}
	if run.Source != nil && !utf8.ValidString(*run.Source) {
		return 0, s.wrap("write", errors.New("run source is not valid UTF-8"))
	}
	started, err := epochSeconds(run.Started)
	if err != nil {
		return 0, s.wrap("write", fmt.Errorf("invalid run start: %w", err))
	}
	heartbeat, err := epochSeconds(run.Heartbeat)
	if err != nil {
		return 0, s.wrap("write", fmt.Errorf("invalid run heartbeat: %w", err))
	}
	var interval any
	if run.Interval != nil {
		seconds, err := durationSeconds(*run.Interval)
		if err != nil {
			return 0, s.wrap("write", fmt.Errorf("invalid run interval: %w", err))
		}
		interval = seconds
	}
	var until any
	if run.Until != nil {
		seconds, err := epochSeconds(*run.Until)
		if err != nil {
			return 0, s.wrap("write", fmt.Errorf("invalid run end time: %w", err))
		}
		until = seconds
	}
	var source any
	if run.Source != nil {
		source = *run.Source
	}

	var requestID int64
	err = s.withTx(ctx, true, func(conn *sql.Conn) error {
		var storedID int64
		var key, storedTargets, storedPorts, storedHosts string
		err := conn.QueryRowContext(ctx, `SELECT id, key, targets, tcp_ports, hosts FROM request WHERE key = ?`, request.Key).Scan(
			&storedID, &key, &storedTargets, &storedPorts, &storedHosts,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return normalizeDBError(ctx, err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			result, err := conn.ExecContext(ctx, `INSERT INTO request (key, targets, tcp_ports, hosts) VALUES (?, ?, ?, ?)`,
				request.Key, targetsJSON, portsJSON, hostsJSON,
			)
			if err != nil {
				return normalizeDBError(ctx, err)
			}
			storedID, err = result.LastInsertId()
			if err != nil {
				return normalizeDBError(ctx, err)
			}
		} else {
			stored, err := decodeRequest(storedID, key, storedTargets, storedPorts, storedHosts)
			if err != nil {
				return fmt.Errorf("existing request %q is invalid: %w", request.Key, err)
			}
			if !slices.Equal(stored.Targets, request.Targets) ||
				!slices.Equal(stored.TCPPorts, request.TCPPorts) ||
				!slices.Equal(stored.Hosts, request.Hosts) {
				return fmt.Errorf("request key %q already has different target, port, or host metadata", request.Key)
			}
		}

		_, err = conn.ExecContext(ctx, `INSERT INTO run
			(id, request_id, source, started, interval, until, host, pid, heartbeat, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'running')`,
			run.ID, storedID, source, started, interval, until, run.Host, run.PID, heartbeat,
		)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		requestID = storedID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return requestID, nil
}

func (s *Store) RecordRound(ctx context.Context, runID string, round RoundInput) (int64, error) {
	started, err := epochSeconds(round.Started)
	if err != nil {
		return 0, s.wrap("write", fmt.Errorf("invalid round start: %w", err))
	}
	rows := make([]preparedResult, len(round.Rows))
	for i, row := range round.Rows {
		at, err := epochSeconds(row.Time)
		if err != nil {
			return 0, s.wrap("write", fmt.Errorf("invalid result time for %q: %w", row.Host, err))
		}
		tcp, err := encodeTCP(row.TCP)
		if err != nil {
			return 0, s.wrap("write", fmt.Errorf("invalid TCP data for %q: %w", row.Host, err))
		}
		rows[i] = preparedResult{host: row.Host, address: row.Address, at: at, result: row.Result, tcp: tcp}
	}

	var roundID int64
	err = s.withTx(ctx, true, func(conn *sql.Conn) error {
		var requestID int64
		var key, targets, ports, hostsJSON string
		err := conn.QueryRowContext(ctx, `SELECT request.id, request.key, request.targets, request.tcp_ports, request.hosts
			FROM run JOIN request ON request.id = run.request_id WHERE run.id = ?`, runID).Scan(
			&requestID, &key, &targets, &ports, &hostsJSON,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no run %q", runID)
		}
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		request, err := decodeRequest(requestID, key, targets, ports, hostsJSON)
		if err != nil {
			return fmt.Errorf("run %q has invalid request metadata: %w", runID, err)
		}
		if err := validateRound(request.Hosts, round); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO round (run_id, started, observations, answered) VALUES (?, ?, ?, ?)`,
			runID, started, round.Observations, len(rows),
		)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		for _, row := range rows {
			if _, err := conn.ExecContext(ctx, `INSERT INTO result (round_id, host, address, t, result, tcp) VALUES (?, ?, ?, ?, ?, ?)`,
				id, row.host, row.address, row.at, row.result, row.tcp,
			); err != nil {
				return normalizeDBError(ctx, err)
			}
		}
		roundID = id
		return nil
	})
	if err != nil {
		return 0, err
	}
	return roundID, nil
}

type preparedResult struct {
	host, address string
	at            float64
	result        string
	tcp           sql.NullString
}

func (s *Store) Heartbeat(ctx context.Context, runID string, at time.Time, elapsed *time.Duration) error {
	heartbeat, err := epochSeconds(at)
	if err != nil {
		return s.wrap("write", fmt.Errorf("invalid heartbeat: %w", err))
	}
	if elapsed == nil {
		return s.withTx(ctx, true, func(conn *sql.Conn) error {
			result, err := conn.ExecContext(ctx, `UPDATE run SET heartbeat = ? WHERE id = ?`, heartbeat, runID)
			if err != nil {
				return normalizeDBError(ctx, err)
			}
			return requireUpdatedRun(ctx, result, runID)
		})
	}
	seconds, err := durationSeconds(*elapsed)
	if err != nil {
		return s.wrap("write", fmt.Errorf("invalid elapsed duration: %w", err))
	}
	return s.withTx(ctx, true, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE run SET heartbeat = ?, round_seconds = ? WHERE id = ?`, heartbeat, seconds, runID)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		return requireUpdatedRun(ctx, result, runID)
	})
}

func (s *Store) EndRun(ctx context.Context, runID string, ended time.Time, status string) error {
	if status != "finished" && status != "stopped" && status != "failed" {
		return s.wrap("write", fmt.Errorf("invalid final run status %q", status))
	}
	seconds, err := epochSeconds(ended)
	if err != nil {
		return s.wrap("write", fmt.Errorf("invalid run end time: %w", err))
	}
	return s.withTx(ctx, true, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE run SET ended = ?, status = ? WHERE id = ?`, seconds, status, runID)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		return requireUpdatedRun(ctx, result, runID)
	})
}

func requireUpdatedRun(ctx context.Context, result sql.Result, runID string) error {
	updated, err := result.RowsAffected()
	if err != nil {
		return normalizeDBError(ctx, err)
	}
	if updated == 0 {
		return fmt.Errorf("no run %q", runID)
	}
	return nil
}
