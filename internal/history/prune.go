package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const pruneGapFactor = 2.5

// Prune adapts PingStore.prune at cn_tool/utils/ping_store.py:420-442,
// pinned cn-tool v0.8.0, commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa
// (Apache-2.0). Big-integer cutoffs preserve the source retention semantics
// without narrowing caller input or overflowing timestamp arithmetic.
func (s *Store) Prune(ctx context.Context, now time.Time, days *big.Int) (int64, error) {
	if days == nil {
		return 0, s.wrap("prune", errors.New("retention days are nil"))
	}
	nowSeconds, err := epochSeconds(now)
	if err != nil {
		return 0, s.wrap("prune", fmt.Errorf("invalid prune time: %w", err))
	}
	cutoff, canDeleteRounds, err := pruneCutoff(now, nowSeconds, days)
	if err != nil {
		return 0, s.wrap("prune", err)
	}

	var deletedRounds int64
	err = s.withTx(ctx, true, func(conn *sql.Conn) error {
		// Validate all persisted states before deleting rounds: an old round
		// can become an empty run during this prune, so checking only rows that
		// are already empty would silently discard malformed history.
		var invalidRunID string
		var invalidStatus sql.NullString
		err := conn.QueryRowContext(ctx, `SELECT id, status FROM run
			WHERE status IS NULL OR status NOT IN ('running', 'finished', 'stopped', 'failed') LIMIT 1`).Scan(&invalidRunID, &invalidStatus)
		if err == nil {
			status := "<NULL>"
			if invalidStatus.Valid {
				status = invalidStatus.String
			}
			return fmt.Errorf("run %q has invalid stored status %q", invalidRunID, status)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return normalizeDBError(ctx, err)
		}
		if canDeleteRounds {
			if err := validatePruneOperands(ctx, conn, cutoff); err != nil {
				return err
			}
		}

		canDeleteRoundsInt := 0
		if canDeleteRounds {
			canDeleteRoundsInt = 1
			result, err := conn.ExecContext(ctx, `DELETE FROM round WHERE started < ?`, cutoff)
			if err != nil {
				return normalizeDBError(ctx, err)
			}
			deletedRounds, err = result.RowsAffected()
			if err != nil {
				return normalizeDBError(ctx, err)
			}
		}

		result, err := conn.ExecContext(ctx, `DELETE FROM run
			WHERE NOT EXISTS (SELECT 1 FROM round WHERE round.run_id = run.id)
			AND (status != 'running' OR (? = 1 AND heartbeat < ?
				AND heartbeat + ? * COALESCE(interval, 0) < ?))`,
			canDeleteRoundsInt, cutoff, pruneGapFactor, nowSeconds,
		)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		if _, err := result.RowsAffected(); err != nil {
			return normalizeDBError(ctx, err)
		}

		result, err = conn.ExecContext(ctx, `DELETE FROM request
			WHERE NOT EXISTS (SELECT 1 FROM run WHERE run.request_id = request.id)`)
		if err != nil {
			return normalizeDBError(ctx, err)
		}
		if _, err := result.RowsAffected(); err != nil {
			return normalizeDBError(ctx, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deletedRounds, nil
}

// validatePruneOperands checks only stored scalars used by the subsequent
// retention predicates. It streams one row at a time and closes each result
// set before issuing the next query or any DELETE.
func validatePruneOperands(ctx context.Context, conn *sql.Conn, cutoff float64) error {
	rows, err := conn.QueryContext(ctx, `SELECT id, started, typeof(started) FROM round`)
	if err != nil {
		return normalizeDBError(ctx, err)
	}
	for rows.Next() {
		var id int64
		var started sql.NullFloat64
		var storageType string
		if err := rows.Scan(&id, &started, &storageType); err != nil {
			return closePruneRows(ctx, rows, normalizeDBError(ctx, err))
		}
		if !started.Valid || (storageType != "integer" && storageType != "real") {
			return closePruneRows(ctx, rows, fmt.Errorf("round %d has nonnumeric started value", id))
		}
		if _, err := timeFromEpoch(started.Float64); err != nil {
			return closePruneRows(ctx, rows, fmt.Errorf("round %d has invalid started value: %w", id, err))
		}
	}
	if err := rows.Err(); err != nil {
		return closePruneRows(ctx, rows, normalizeDBError(ctx, err))
	}
	if err := closePruneRows(ctx, rows, nil); err != nil {
		return err
	}

	rows, err = conn.QueryContext(ctx, `SELECT id, heartbeat, typeof(heartbeat), interval, typeof(interval)
		FROM run
		WHERE status = 'running'
		AND NOT EXISTS (SELECT 1 FROM round WHERE round.run_id = run.id AND round.started >= ?)`, cutoff)
	if err != nil {
		return normalizeDBError(ctx, err)
	}
	for rows.Next() {
		var id, heartbeatType, intervalType string
		var heartbeat, interval sql.NullFloat64
		if err := rows.Scan(&id, &heartbeat, &heartbeatType, &interval, &intervalType); err != nil {
			return closePruneRows(ctx, rows, normalizeDBError(ctx, err))
		}
		if !heartbeat.Valid || (heartbeatType != "integer" && heartbeatType != "real") {
			return closePruneRows(ctx, rows, fmt.Errorf("running run %q has nonnumeric heartbeat", id))
		}
		if _, err := timeFromEpoch(heartbeat.Float64); err != nil {
			return closePruneRows(ctx, rows, fmt.Errorf("running run %q has invalid heartbeat: %w", id, err))
		}
		if interval.Valid {
			if intervalType != "integer" && intervalType != "real" {
				return closePruneRows(ctx, rows, fmt.Errorf("running run %q has nonnumeric interval", id))
			}
			if _, err := durationFromSeconds(interval.Float64); err != nil {
				return closePruneRows(ctx, rows, fmt.Errorf("running run %q has invalid interval: %w", id, err))
			}
		} else if intervalType != "null" {
			return closePruneRows(ctx, rows, fmt.Errorf("running run %q has invalid nullable interval", id))
		}
	}
	if err := rows.Err(); err != nil {
		return closePruneRows(ctx, rows, normalizeDBError(ctx, err))
	}
	return closePruneRows(ctx, rows, nil)
}

func closePruneRows(ctx context.Context, rows *sql.Rows, primary error) error {
	if closeErr := rows.Close(); closeErr != nil {
		closeErr = normalizeDBError(ctx, closeErr)
		if primary != nil {
			return errors.Join(primary, closeErr)
		}
		return closeErr
	}
	return primary
}

// pruneCutoff copies and clamps days before doing arithmetic. When the requested
// age would precede the oldest supported history timestamp, rounds and running
// empty runs are kept rather than converting an enormous integer unsafely.
func pruneCutoff(now time.Time, nowSeconds float64, days *big.Int) (float64, bool, error) {
	if days == nil {
		return 0, false, errors.New("retention days are nil")
	}
	retentionDays := new(big.Int).Set(days)
	if retentionDays.Sign() < 1 {
		retentionDays.SetInt64(1)
	}
	ageSeconds := new(big.Int).Mul(retentionDays, big.NewInt(24*60*60))
	available := big.NewInt(now.UTC().Unix() - minUnixSeconds)
	if ageSeconds.Cmp(available) > 0 {
		return 0, false, nil
	}
	return nowSeconds - float64(ageSeconds.Int64()), true, nil
}
