// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"time"

	"github.com/evdanil/cn-mon/internal/target"
)

const liveRedrawPeriod = 500 * time.Millisecond

func (r *runner) liveEnabled() bool {
	hooks := r.deps.Live
	return hooks != nil && (hooks.Enabled == nil || hooks.Enabled()) && (hooks.Draw != nil || hooks.Change != nil)
}

func (r *runner) liveDrawEnabled() bool {
	return r.deps.Live != nil && r.deps.Live.Draw != nil && r.liveEnabled()
}

func (r *runner) drawLive(reading runReading, phase LivePhase, pending map[int]byte, pendingRows map[int]Row, pendingTimes map[int]time.Time, done, total int, nextSlot *time.Time) {
	if !r.liveDrawEnabled() {
		return
	}
	data := makeLiveData(r.tracker, r.plan.Request.Hosts, r.plan.Request.Ports, pending, pendingRows, pendingTimes)
	update := LiveUpdate{
		Request: cloneRequest(r.plan.Request), Data: data, Phase: phase,
		Now: reading.Run, Origin: r.origin, Until: cloneTime(r.until),
		Interval: cloneDuration(r.plan.Interval), Source: cloneString(r.plan.Source),
		CurrentRounds: r.outcome.CurrentRounds, SkippedSlots: r.outcome.SkippedSlots,
		Done: done, Total: total,
	}
	if nextSlot != nil {
		next := nextSlot.Sub(reading.Run)
		if next < 0 {
			next = 0
		}
		update.NextIn = &next
	}
	r.deps.Live.Draw(update)
}

func makeLiveData(tracker *Tracker, hosts []string, ports []int, pending map[int]byte, pendingRows map[int]Row, pendingTimes map[int]time.Time) LiveData {
	data := LiveData{}
	if tracker == nil {
		data.Cells = append([]byte(nil), pendingCells(hosts, pending)...)
		data.Seen = countPendingSeen(pending)
		return data
	}
	data.Stats = tracker.stats
	data.Cells = tracker.Cells(pending)
	data.Changed = tracker.Changed(pending)
	answeredPartly := make(map[int]bool)
	for index, row := range pendingRows {
		answeredPartly[index] = PartialReply(row)
	}
	data.Partial = tracker.Partial(pending, answeredPartly)
	data.Seen = tracker.stats.Seen
	rows := make([]LiveRow, 0, tracker.stats.Seen+len(pendingRows))
	for index, host := range hosts {
		if index >= len(tracker.everSeen) {
			break
		}
		committed := tracker.everSeen[index]
		observation, hasPending := pending[index]
		pendingSeen := hasPending && isSeen(observation)
		if !committed && !pendingSeen {
			continue
		}
		latest := ""
		if committed {
			if tracker.seenNow[index] {
				if tracker.hasSeenRow[index] {
					latest = tracker.lastSeenRows[index].Result
				}
			} else {
				latest, _ = ResultText(tracker.current[index])
			}
		}
		lastSeen := time.Time{}
		seenRounds := 0
		if committed {
			lastSeen = tracker.lastSeen[index]
			seenRounds = tracker.seenRounds[index]
		}
		rounds := tracker.stats.Rounds
		if hasPending {
			rounds++
			if row, exists := pendingRows[index]; exists {
				if row.Result != "" {
					latest = row.Result
				}
			} else if text, err := ResultText(observation); err == nil {
				latest = text
			}
			if pendingSeen {
				when := pendingTimes[index]
				if when.After(lastSeen) {
					lastSeen = when
				}
				seenRounds++
				if !committed {
					data.Seen++
				}
			}
		}
		rows = append(rows, LiveRow{
			Index: index, Host: host, Latest: latest, Cell: data.Cells[index],
			LastSeen: lastSeen, SeenRounds: seenRounds, Rounds: rounds,
		})
	}
	data.Rows = rows
	return data
}

func pendingCells(hosts []string, pending map[int]byte) []byte {
	cells := make([]byte, len(hosts))
	for index := range hosts {
		cells[index] = Unprobed
		if observation, ok := pending[index]; ok {
			cells[index] = observation
		}
	}
	return cells
}

func countPendingSeen(pending map[int]byte) int {
	count := 0
	for _, observation := range pending {
		if isSeen(observation) {
			count++
		}
	}
	return count
}

func cloneRequest(request target.Request) target.Request {
	request.Tokens = append([]string(nil), request.Tokens...)
	request.Hosts = append([]string(nil), request.Hosts...)
	request.Ports = append([]int(nil), request.Ports...)
	return request
}

func (r *runner) liveChange(changes []Change) {
	hooks := r.deps.Live
	if hooks == nil || hooks.Change == nil || !r.liveEnabled() {
		return
	}
	for _, change := range changes {
		hooks.Change(change)
	}
}

func (r *runner) liveDelay(now time.Duration, last *time.Duration) time.Duration {
	delay := heartbeatDelay(now, last)
	if r.deps.Live != nil && r.deps.Live.Draw != nil {
		delay = min(delay, liveRedrawPeriod)
	}
	return delay
}
