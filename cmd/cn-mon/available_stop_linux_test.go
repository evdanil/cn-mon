//go:build linux

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/probe"
)

// stoppingProber raises SIGINT when the first round starts resolving and then
// waits for the run's work context to end, so the run stops before it has
// completed, and therefore recorded, any round.
type stoppingProber struct{ availableProber }

func (p stoppingProber) Resolve(ctx context.Context, hosts []string) ([]probe.Resolved, error) {
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, errors.New("the run was not stopped by SIGINT")
	}
}

// Mirrors test_available_run_stopped_before_its_first_round_lists_not_tested
// (cn-tool merge a739948c69ada9bab27d25058c1d695ca06d5aba, PR #178): the
// request and run are recorded before the first round, so a run stopped before
// it lists every host as Not tested with an empty Since. Linux only because the
// stop is a real SIGINT to the test process.
func TestAvailableRunStoppedBeforeItsFirstRoundListsNotTested(t *testing.T) {
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "cn.log")
	got := availableCLIAt(t, database, logPath, []string{"127.0.0.1", "192.0.2.1", "--available", "--format", "json"}, availableProber{}, func(_ string, runtime *commandRuntime) {
		runtime.installSignals = true
		runtime.exit = func(code int) { t.Errorf("the command exited with %d instead of stopping gracefully", code) }
		runtime.runDeps.Prober = stoppingProber{}
	})
	rows, present := availableUnseen(t, got.stdout)
	if got.code != 1 || !present || len(rows) != 2 {
		t.Fatalf("exit=%d unseen=%+v present=%t, want 1 and every host listed\nstderr=%q", got.code, rows, present, got.stderr)
	}
	for _, row := range rows {
		if row.Status != "Not tested" || row.Rounds != 0 || row.Since != "" || row.FailedRounds != 0 {
			t.Errorf("row = %+v, want Not tested over no rounds with an empty since", row)
		}
	}
	if strings.Contains(got.stderr, "never-seen") || strings.Contains(got.stderr, availableHistoryFailure) {
		t.Errorf("stderr = %q, want no never-seen claim and no history failure", got.stderr)
	}
}
