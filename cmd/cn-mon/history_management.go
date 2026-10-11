// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/output"
)

func runHistoryManagementCLI(
	opts cliOptions,
	stdout, stderr io.Writer,
	cfg settings,
	logger *fileLogger,
	logFailure func(error),
	ctx context.Context,
	control *commandControl,
	runtime commandRuntime,
) int {
	closed := false
	finish := func(code int) int {
		if closed {
			return code
		}
		closed = true
		if err := logger.Close(); err != nil {
			logFailure(err)
		}
		return code
	}
	writeRuns := func(rows []output.RunRecord, code int) int {
		reportOptions := commandReportOptionsFor(runtime, stdout, opts.Format, cfg.Theme)
		err := writeCommandReport(control, stdout, reportOptions, func() error {
			return output.WriteRuns(stdout, rows, reportOptions)
		})
		if err != nil {
			if isClosedOutputPipe(err, stdout) {
				return finish(code)
			}
			commandNote(runtime.screenState, stderr, "write runs report: "+err.Error())
			return finish(3)
		}
		return finish(code)
	}
	writeDeletion := func(result history.DeleteResult, operationErr error) int {
		if operationErr != nil && result.Committed {
			commandNote(runtime.screenState, stderr, "history mutation committed with a cleanup error: "+operationErr.Error())
		}
		reportOptions := commandReportOptionsFor(runtime, stdout, opts.Format, cfg.Theme)
		writeErr := writeCommandReport(control, stdout, reportOptions, func() error {
			return output.WriteDeletion(stdout, result, reportOptions)
		})
		if writeErr != nil {
			if isClosedOutputPipe(writeErr, stdout) {
				if operationErr != nil {
					return finish(3)
				}
				return finish(0)
			}
			if result.Committed {
				commandNote(runtime.screenState, stderr, fmt.Sprintf("history mutation committed; write deletion report: %v", writeErr))
			} else {
				commandNote(runtime.screenState, stderr, fmt.Sprintf("write deletion report: %v", writeErr))
			}
			return finish(3)
		}
		if operationErr != nil {
			return finish(3)
		}
		return finish(0)
	}
	fail := func(err error) int {
		code := historyManagementErrorCode(err)
		commandNote(runtime.screenState, stderr, "history management: "+err.Error())
		logFailure(logger.Log("ERROR", "history management operation failed"))
		return finish(code)
	}
	if opts.Mode == modeListRuns {
		rows, found, err := readCommandRuns(ctx, cfg.HistoryFile, opts.ID, runtime.readDeps)
		if err != nil {
			return fail(err)
		}
		if !found {
			return fail(history.ErrNotFound)
		}
		logFailure(logger.Log("INFO", "list-runs operation finished"))
		return writeRuns(rows, 0)
	}

	if opts.Mode != modeDeleteRun && opts.Mode != modeDeleteRequest && opts.Mode != modeClearHistory {
		return finish(3)
	}
	guard, err := managementDeleteGuard(runtime.readDeps)
	if err != nil {
		return fail(err)
	}
	store, err := history.OpenExistingWriter(ctx, cfg.HistoryFile)
	if err != nil {
		return fail(err)
	}
	if store == nil {
		if opts.Mode == modeClearHistory {
			return writeDeletion(history.DeleteResult{}, nil)
		}
		return fail(history.ErrNotFound)
	}
	var result history.DeleteResult
	switch opts.Mode {
	case modeDeleteRun:
		result, err = store.DeleteRun(ctx, opts.ID, guard)
	case modeDeleteRequest:
		result, err = store.DeleteRequest(ctx, opts.ID, guard)
	case modeClearHistory:
		result, err = store.ClearHistory(ctx, guard)
	}
	closeErr := store.Close()
	if closeErr != nil {
		if err != nil {
			err = fmt.Errorf("%v; close history store: %w", err, closeErr)
		} else {
			err = fmt.Errorf("close history store: %w", closeErr)
		}
	}
	if err != nil && !result.Committed {
		return fail(err)
	}
	logFailure(logger.Log("INFO", readerModeName(opts.Mode)+" operation finished"))
	return writeDeletion(result, err)
}

func readCommandRuns(ctx context.Context, path, prefix string, deps monitor.ReadDeps) ([]output.RunRecord, bool, error) {
	if deps.OpenHistory == nil {
		deps.OpenHistory = openReadOnlyHistory
	}
	if deps.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return nil, false, fmt.Errorf("resolve host name: %w", err)
		}
		deps.Hostname = hostname
	}
	if deps.PIDState == nil {
		deps.PIDState = history.LocalPIDState
	}
	reader, err := deps.OpenHistory(ctx, path)
	if err != nil {
		return nil, false, err
	}
	if reader == nil {
		return nil, false, nil
	}
	var rows []output.RunRecord
	var found bool
	now := time.Now().UTC()
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		matches, err := snapshot.Find(prefix)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return history.ErrNotFound
		}
		if len(matches) > 1 {
			return history.ErrAmbiguousRequest
		}
		request, err := snapshot.Request(matches[0].Key)
		if err != nil {
			return err
		}
		if request == nil || request.ID != matches[0].ID {
			return history.ErrNotFound
		}
		found = true
		rows = make([]output.RunRecord, 0)
		var after *history.RunCursor
		for {
			page, err := snapshot.Runs(request.ID, false, after, 500)
			if err != nil {
				return err
			}
			for _, run := range page.Items {
				ended := run.Ended
				rows = append(rows, output.RunRecord{
					ID: run.ID, RequestID: request.Key, Status: run.Status,
					State:   history.RunState(run, now, deps.Hostname, deps.PIDState),
					Started: run.Started, Ended: ended, Host: run.Host, PID: run.PID, Rounds: run.Rounds,
				})
			}
			if page.Next == nil {
				return nil
			}
			after = page.Next
		}
	})
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		if closeErr != nil {
			if err != nil {
				return nil, false, fmt.Errorf("%v; close history reader: %w", err, closeErr)
			}
			return nil, false, fmt.Errorf("close history reader: %w", closeErr)
		}
		return nil, false, err
	}
	return rows, found, nil
}

func managementDeleteGuard(deps monitor.ReadDeps) (history.DeleteGuard, error) {
	hostname := deps.Hostname
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return history.DeleteGuard{}, fmt.Errorf("resolve host name: %w", err)
		}
	}
	pidState := deps.PIDState
	if pidState == nil {
		pidState = history.LocalPIDState
	}
	return history.DeleteGuard{Hostname: hostname, PIDState: pidState}, nil
}

func historyManagementErrorCode(err error) int {
	category := 0
	var classify func(error) bool
	classify = func(current error) bool {
		if current == nil {
			return false
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			children := joined.Unwrap()
			if len(children) == 0 {
				return false
			}
			valid := true
			for _, child := range children {
				valid = classify(child) && valid
			}
			return valid
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			return classify(wrapped.Unwrap())
		}
		leaf := 0
		switch {
		case errors.Is(current, history.ErrNotFound):
			leaf = 1
		case errors.Is(current, history.ErrAmbiguousRequest):
			leaf = 2
		default:
			return false
		}
		if category != 0 && category != leaf {
			return false
		}
		category = leaf
		return true
	}
	if !classify(err) {
		return 3
	}
	return category
}
