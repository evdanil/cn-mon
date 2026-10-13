// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evdanil/cn-mon/internal/history"
)

func requestRow(summary history.RequestSummary, stateRun *history.RunInfo, now time.Time, deps ReadDeps) (RequestRow, error) {
	row := RequestRow{
		ID:      summary.Key[:min(len(summary.Key), 8)],
		Targets: formatRequestTargets(summary.Targets, summary.NewestRun),
		TCP:     formatTCPPorts(summary.TCPPorts), Runs: summary.Runs, Rounds: summary.Rounds,
	}
	if summary.LastRound != nil {
		last := *summary.LastRound
		row.LastRound = &last
	}
	if stateRun == nil {
		return row, nil
	}
	state := history.RunState(*stateRun, now, deps.Hostname, pidStateFunc(deps))
	switch state {
	case "stopped":
		row.State = "stopped"
	case "running":
		row.State = state
		var err error
		row.Detail, err = runningDetail(*stateRun, now)
		if err != nil {
			return RequestRow{}, err
		}
	case "lost":
		row.State = state
		row.Detail = "at " + relativeClock(stateRun.Heartbeat, now)
	default:
		row.State = state
	}
	return row, nil
}

func pidStateFunc(deps ReadDeps) func(int64) history.PIDState {
	if deps.PIDState != nil {
		return deps.PIDState
	}
	return history.LocalPIDState
}

func formatRequestTargets(tokens []string, newest *history.RunInfo) string {
	const tokenBudget = 44
	var label strings.Builder
	if newest != nil && newest.Source != nil && *newest.Source != "" {
		label.WriteString(filepath.Base(*newest.Source))
		label.WriteString(": ")
	}
	joined := 0
	used := 0
	for i, token := range tokens {
		runes := utf8.RuneCountInString(token)
		extra := runes
		if i > 0 {
			extra += 2
		}
		if i > 0 && used+extra > tokenBudget {
			break
		}
		if joined > 0 {
			label.WriteString(", ")
		}
		label.WriteString(token)
		used += extra
		joined++
	}
	if omitted := len(tokens) - joined; omitted > 0 {
		fmt.Fprintf(&label, " +%d", omitted)
	}
	return label.String()
}

func formatTCPPorts(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	ordered := append([]int(nil), ports...)
	sort.Ints(ordered)
	values := make([]string, len(ordered))
	for i, port := range ordered {
		values[i] = fmt.Sprint(port)
	}
	return strings.Join(values, ",")
}

func runningDetail(run history.RunInfo, now time.Time) (string, error) {
	if run.Interval == nil {
		return "one round, in progress", nil
	}
	interval := *run.Interval
	if interval <= 0 {
		return "", errors.New("running history row has a nonpositive interval")
	}
	intervalNS := big.NewInt(int64(interval))
	elapsedNS := new(big.Int).Sub(unixNanosBig(now), unixNanosBig(run.Started))
	nextSlot, remainder := new(big.Int), new(big.Int)
	nextSlot.QuoRem(elapsedNS, intervalNS, remainder)
	if remainder.Sign() > 0 {
		nextSlot.Add(nextSlot, big.NewInt(1))
	}
	var detail strings.Builder
	fmt.Fprintf(&detail, "round %d", run.Rounds)
	if run.Until == nil {
		detail.WriteString(" until stopped")
	} else {
		windowNS := new(big.Int).Sub(unixNanosBig(*run.Until), unixNanosBig(run.Started))
		maxSlots, maxRemainder := new(big.Int), new(big.Int)
		maxSlots.QuoRem(windowNS, intervalNS, maxRemainder)
		if maxRemainder.Sign() > 0 {
			maxSlots.Add(maxSlots, big.NewInt(1))
		}
		fmt.Fprintf(&detail, " of up to %s", maxSlots.String())
	}
	nextNS := new(big.Int).Mul(nextSlot, intervalNS)
	next := timeFromUnixNanos(new(big.Int).Add(unixNanosBig(run.Started), nextNS))
	if run.Until == nil || next.Before(*run.Until) {
		fmt.Fprintf(&detail, ", next at %s", relativeClock(next, now))
	}
	return detail.String(), nil
}

func unixNanosBig(value time.Time) *big.Int {
	seconds := big.NewInt(value.Unix())
	seconds.Mul(seconds, big.NewInt(1_000_000_000))
	return seconds.Add(seconds, big.NewInt(int64(value.Nanosecond())))
}

func timeFromUnixNanos(value *big.Int) time.Time {
	seconds, nanos := new(big.Int), new(big.Int)
	seconds.QuoRem(value, big.NewInt(1_000_000_000), nanos)
	if nanos.Sign() < 0 {
		seconds.Sub(seconds, big.NewInt(1))
		nanos.Add(nanos, big.NewInt(1_000_000_000))
	}
	if !seconds.IsInt64() || !nanos.IsInt64() {
		return time.Time{}
	}
	return time.Unix(seconds.Int64(), nanos.Int64()).UTC()
}

func relativeClock(value, reference time.Time) string {
	value, reference = value.UTC(), reference.UTC()
	if value.Year() != reference.Year() || value.YearDay() != reference.YearDay() {
		return value.Format("2006-01-02 15:04:05")
	}
	return value.Format("15:04:05")
}

func readIntervalCount(value int64) (int, error) {
	if value < 0 || value > int64(math.MaxInt) {
		return 0, errors.New("history round count cannot fit in this process")
	}
	return int(value), nil
}
