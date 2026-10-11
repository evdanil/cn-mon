// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"time"

	"github.com/evdanil/gotest/internal/target"
)

// LiveRow is the bounded, display-only summary for one host. It deliberately
// excludes the tracker's sampled periods and TCP detail map.
type LiveRow struct {
	Index              int
	Host, Latest       string
	Cell               byte
	LastSeen           time.Time
	SeenRounds, Rounds int
}

// LiveData contains detached values suitable for synchronous display hooks.
// Its slices are owned by the caller and must not be retained or mutated.
type LiveData struct {
	Stats            Stats
	Cells            []byte
	Changed, Partial []int
	Rows             []LiveRow
	Seen             int
}

type LivePhase uint8

const (
	LiveResolving LivePhase = iota
	LiveProbing
	LiveWaiting
	LiveFollowing
	LiveEnded
)

// LiveUpdate is one synchronous state-owner publication. Pending display
// values do not alter persisted rounds, tracker state, or run outcomes.
type LiveUpdate struct {
	Request       target.Request
	Data          LiveData
	Phase         LivePhase
	Now, Origin   time.Time
	Until         *time.Time
	Interval      *time.Duration
	Source        *string
	CurrentRounds int
	SkippedSlots  int64
	Done, Total   int
	NextIn        *time.Duration
}

// LiveHooks are optional synchronous observers called only by the Run owner.
type LiveHooks struct {
	Enabled func() bool
	Draw    func(LiveUpdate)
	Change  func(Change)
}
