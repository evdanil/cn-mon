// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"math/big"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/probe"
	"github.com/evdanil/cn-mon/internal/target"
)

type ClockReading struct {
	Wall time.Time
	Mono time.Duration
}

type Clock interface {
	Now() ClockReading
	After(time.Duration) <-chan time.Time
}

type Prober interface {
	Resolve(context.Context, []string) ([]probe.Resolved, error)
	ProbeBatch(context.Context, []probe.Resolved, []int) ([]probe.Result, error)
}

type History interface {
	Close() error
	BeginRun(context.Context, history.Request, history.RunStart) (int64, error)
	RecordRound(context.Context, string, history.RoundInput) (int64, error)
	Heartbeat(context.Context, string, time.Time, *time.Duration) error
	EndRun(context.Context, string, time.Time, string) error
	Prune(context.Context, time.Time, *big.Int) (int64, error)
	View(context.Context, func(*history.Snapshot) error) error
}

type RunPlan struct {
	Request     target.Request
	Interval    *time.Duration
	Duration    *time.Duration
	Since       *time.Time
	OwnRunOnly  bool
	Source      *string
	HistoryFile string
	HistoryDays *big.Int
	RunID       string
	Hostname    string
	PID         int64
}

type RunDeps struct {
	Clock       Clock
	Prober      Prober
	OpenHistory func(context.Context, string) (History, error)
	Stop        <-chan struct{}
	Live        *LiveHooks
	Note        func(string)
}

type LatestRow struct {
	Host string
	Row  Row
}

type RunOutcome struct {
	Stats                                      Stats
	Seen                                       []HostSummary
	Latest                                     []LatestRow
	CurrentRounds, EarlierRounds               int
	SkippedSlots                               int64
	Interval                                   *time.Duration
	Unresolved                                 []string
	ProbeFailed, TCPOpen, TCPRequired, Stopped bool
	HistoryError                               error
}

type runReading struct {
	Run  time.Time
	Wall time.Time
	Mono time.Duration
}

type runner struct {
	plan        RunPlan
	deps        RunDeps
	hardCtx     context.Context
	workCtx     context.Context
	clock       *runClock
	origin      time.Time
	until       *time.Time
	store       History
	runRecorded bool
	tracker     *Tracker
	withoutOwn  *Tracker
	outcome     RunOutcome
	resolved    map[string]struct{}
	unresolved  map[string]struct{}
}
