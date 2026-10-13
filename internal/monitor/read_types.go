// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/target"
)

// Reader is the narrow read-only boundary used by list, show, and follow.
type Reader interface {
	Close() error
	View(context.Context, func(*history.Snapshot) error) error
}

type ReadDeps struct {
	Clock          Clock
	OpenHistory    func(context.Context, string) (Reader, error)
	Hostname       string
	PIDState       func(int64) history.PIDState
	Stop           <-chan struct{}
	EnterFollowing func()
	Live           *LiveHooks
	Note           func(string)
}

type ReadPlan struct {
	HistoryFile string
	Prefix      string
	Since       *time.Time
	Follow      bool
}

type RequestRow struct {
	ID, Targets, TCP string
	Runs, Rounds     int64
	LastRound        *time.Time
	State, Detail    string
}

type ReadOutcome struct {
	Request target.Request
	Run     RunOutcome
	Found   bool
	Running bool
	Problem string
}
