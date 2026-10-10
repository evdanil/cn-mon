// Package history reads and writes the compatible cn-mon history database.
//
// These value types are the storage boundary shared by the monitor and history
// implementation. They deliberately contain no SQL or presentation behavior.
package history

import (
	"time"
)

type RequestMeta struct {
	ID       int64
	Key      string
	Targets  []string
	TCPPorts []int
}

type RequestMatch struct {
	ID  int64
	Key string
}

type Request struct {
	RequestMeta
	Hosts []string
}

type RequestSummary struct {
	RequestMeta
	Runs, Rounds          int64
	FirstRound, LastRound *time.Time
	NewestRun             *RunInfo
}

type RequestInfo struct {
	RequestSummary
	Hosts []string
}

type RunStart struct {
	ID        string
	Source    *string
	Started   time.Time
	Interval  *time.Duration
	Until     *time.Time
	Host      string
	PID       int64
	Heartbeat time.Time
}

type RunInfo struct {
	RunStart
	RequestID    int64
	RoundSeconds *time.Duration
	Ended        *time.Time
	Status       string
	Rounds       int64
}

type ResultRow struct {
	Host, Address, Result string
	Time                  time.Time
	TCP                   map[int]string
}

type RoundInput struct {
	Started      time.Time
	Observations string
	Rows         []ResultRow
}

type StoredRound struct {
	RoundInput
	ID       int64
	RunID    string
	Interval *time.Duration
	Answered int64
	Cursor   RoundCursor
}

type WindowFilter struct {
	Since, Before *time.Time
}

type RoundFilter struct {
	WindowFilter
	After     *RoundCursor
	NewerThan int64
	Limit     int
}

type WindowInfo struct {
	Count                     int64
	First, Last               *RoundCursor
	FirstStarted, LastStarted *time.Time
	Through                   int64
}

type RequestPage struct {
	Items []RequestSummary
	Next  *RequestCursor
}

type RunPage struct {
	Items []RunInfo
	Next  *RunCursor
}

type RoundPage struct {
	Items []StoredRound
	Next  *RoundCursor
}

// Cursor keys preserve the original SQLite REAL values and deterministic
// tie-breakers. Their fields remain private so callers can only pass values
// returned by this package.
type RequestCursor struct {
	activity    float64
	hasActivity bool
	id          int64
}

type RunCursor struct {
	started float64
	rowID   int64
}

type RoundCursor struct {
	started float64
	id      int64
}

type PIDState uint8

const (
	PIDUnknown PIDState = iota
	PIDAlive
	PIDDead
)
