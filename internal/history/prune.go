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
