// Package monitor contains the pure observation and sampled-period model.
// New utility code in this package is licensed under Apache-2.0.
package monitor

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	EchoReply    byte = '!'
	Rejected     byte = 'R'
	TCPSeen      byte = 'T'
	Unreachable  byte = 'U'
	ExecutionErr byte = 'E'
	NoName       byte = '?'
	NoResponse   byte = '.'
	Gone         byte = 'x'
	Unprobed     byte = ' '

	gapFactor = 2.5
)

// Row is one host's current probe result. Rows are supplied for every host by
// the run engine, while Round.Rows normally stores details only for seen hosts.
type Row struct {
	Address string
	Result  string
	TCP     map[int]string
}

// Round is a completed sweep. Rows contains the details retained for seen
// hosts; Times contains their per-host sample times and defaults to Started.
// The caller supplies rounds in (started, round ID) order.
type Round struct {
	Started      time.Time
	Interval     time.Duration
	Observations string
	Rows         map[string]Row
	Times        map[string]time.Time
}

type Period struct {
	From time.Time
	To   time.Time
}

type Change struct {
	Index       int
	Host        string
	Time        time.Time
	Seen        bool
	Observation byte
}

type HostSummary struct {
	Index      int
	Host       string
	Address    string
	Latest     string
	TCP        map[int]string
	FirstSeen  time.Time
	LastSeen   time.Time
	SeenRounds int
	Rounds     int
	Periods    []Period
	Ongoing    bool
	Partial    bool
	Cell       byte
}

// Stats reports aggregate counts and the first/last folded round. HadError
// means an ICMP observation was E; command execution failures also include
// TCP errors and are checked separately with ExecutionFailure.
type Stats struct {
	Rounds     int
	Seen       int
	Answering  int
	FirstRound time.Time
	LastRound  time.Time
	HadError   bool
}

// Observation maps a current result row to the reference observation alphabet.
func Observation(row Row, ports []int) byte {
	if strings.HasPrefix(row.Result, "OK") {
		return EchoReply
	}
	if row.Result == "REJECTED" {
		return Rejected
	}
	for _, port := range ports {
		if row.TCP[port] == "open" || row.TCP[port] == "closed" {
			return TCPSeen
		}
	}
	if strings.HasPrefix(row.Result, "ERROR") {
		return ExecutionErr
	}
	if row.Result == "no such host" {
		return NoName
	}
	if row.Result == "UNREACHABLE" || row.Result == "NO ROUTE" {
		return Unreachable
	}
	return NoResponse
}

// ExecutionFailure reports probe execution failures independently of whether
// a response made the host seen. TCP errors only count for requested ports.
func ExecutionFailure(row Row, ports []int) bool {
	if strings.HasPrefix(row.Result, "ERROR") {
		return true
	}
	for _, port := range ports {
		if row.TCP[port] == "error" {
			return true
		}
	}
	return false
}

func PartialReply(row Row) bool { return strings.HasPrefix(row.Result, "OK (") }

// ResultText reconstructs the result represented by an unseen observation.
func ResultText(observation byte) (string, error) {
	switch observation {
	case NoResponse:
		return "NO RESPONSE", nil
	case Unreachable:
		return "UNREACHABLE", nil
	case ExecutionErr:
		return "ERROR", nil
	case NoName:
		return "no such host", nil
	case Unprobed:
		return "", nil
	default:
		return "", fmt.Errorf("observation %q has no unseen result text", observation)
	}
}

// ExpectedSpacing returns the lower median of consecutive sorted sample gaps.
// At least three starts (two gaps) are required before inferring a schedule.
func ExpectedSpacing(starts []time.Time) time.Duration {
	if len(starts) < 3 {
		return 0
	}
	ordered := append([]time.Time(nil), starts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })
	gaps := make([]time.Duration, 0, len(ordered)-1)
	for i := 1; i < len(ordered); i++ {
		gaps = append(gaps, ordered[i].Sub(ordered[i-1]))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[(len(gaps)-1)/2]
}

type Tracker struct {
	hosts            []string
	ports            []int
	oneRoundSpacing  time.Duration
	stats            Stats
	current          []byte
	seenNow          []bool
	everSeen         []bool
	unreachable      []bool
	firstSeen        []time.Time
	lastSeen         []time.Time
	seenRounds       []int
	lastSeenRows     []Row
	hasSeenRow       []bool
	partial          []bool
	changed          []bool
	periods          [][]Period
	periodOpen       []bool
	previousStarted  time.Time
	previousInterval time.Duration
	hasPrevious      bool
}

func NewTracker(hosts []string, ports []int, oneRoundSpacing time.Duration) *Tracker {
	copyHosts := append([]string(nil), hosts...)
	copyPorts := append([]int(nil), ports...)
	n := len(copyHosts)
	return &Tracker{
		hosts: copyHosts, ports: copyPorts, oneRoundSpacing: oneRoundSpacing,
		current: make([]byte, n), seenNow: make([]bool, n), everSeen: make([]bool, n),
		unreachable: make([]bool, n), firstSeen: make([]time.Time, n), lastSeen: make([]time.Time, n),
		seenRounds: make([]int, n), lastSeenRows: make([]Row, n), hasSeenRow: make([]bool, n),
		partial: make([]bool, n), changed: make([]bool, n), periods: make([][]Period, n), periodOpen: make([]bool, n),
	}
}

func (t *Tracker) spacing(interval time.Duration) time.Duration {
	if interval > 0 {
		return interval
	}
	return t.oneRoundSpacing
}

func (t *Tracker) gapBefore(round Round) bool {
	if !t.hasPrevious {
		return false
	}
	previous, current := t.spacing(t.previousInterval), t.spacing(round.Interval)
	if previous <= 0 && current <= 0 {
		return true
	}
	if current > previous {
		previous = current
	}
	return float64(round.Started.Sub(t.previousStarted)) > gapFactor*float64(previous)
}

// Add folds one completed round into aggregate host state and sampled periods.
// It validates the observation vector before changing tracker state.
func (t *Tracker) Add(round Round) ([]Change, error) {
	if len(round.Observations) != len(t.hosts) {
		return nil, fmt.Errorf("a round has %d observations for %d hosts", len(round.Observations), len(t.hosts))
	}
	for i := range round.Observations {
		if !validObservation(round.Observations[i]) {
			return nil, fmt.Errorf("invalid observation %q at host index %d", round.Observations[i], i)
		}
	}

	if t.gapBefore(round) {
		for i := range t.periodOpen {
			t.periodOpen[i] = false
		}
	}
	changes := make([]Change, 0)
	newSeen := make([]bool, len(t.hosts))
	newPartial := make([]bool, len(t.hosts))
	newChanged := make([]bool, len(t.hosts))
	for i, host := range t.hosts {
		observation := round.Observations[i]
		seen := isSeen(observation)
		when := round.Started
		if sampled, ok := round.Times[host]; ok {
			when = sampled
		}
		if t.everSeen[i] && when.Before(t.lastSeen[i]) {
			when = t.lastSeen[i]
		}
		if t.stats.Rounds > 0 && seen != t.seenNow[i] {
			changes = append(changes, Change{Index: i, Host: host, Time: when, Seen: seen, Observation: observation})
			newChanged[i] = true
		}
		if observation == Unreachable {
			t.unreachable[i] = true
		}
		if observation == ExecutionErr {
			t.stats.HadError = true
		}
		if !seen {
			if observation == NoResponse || observation == Unreachable || observation == ExecutionErr || observation == NoName {
				t.current[i] = observation
			} else {
				t.current[i] = NoResponse
			}
			t.periodOpen[i] = false
			continue
		}
		newSeen[i] = true
		t.current[i] = observation
		if !t.everSeen[i] {
			t.firstSeen[i] = when
		}
		t.everSeen[i] = true
		t.lastSeen[i] = when
		t.seenRounds[i]++
		if row, ok := round.Rows[host]; ok {
			t.lastSeenRows[i] = cloneRow(row)
			t.hasSeenRow[i] = true
			newPartial[i] = PartialReply(row)
		}
		if t.periodOpen[i] {
			t.periods[i][len(t.periods[i])-1].To = when
		} else {
			t.periods[i] = append(t.periods[i], Period{From: when, To: when})
			t.periodOpen[i] = true
		}
	}
	t.seenNow, t.partial, t.changed = newSeen, newPartial, newChanged
	if t.stats.Rounds == 0 {
		t.stats.FirstRound = round.Started
	}
	t.stats.Rounds++
	t.stats.LastRound = round.Started
	t.stats.Answering = countTrue(newSeen)
	t.stats.Seen = countTrue(t.everSeen)
	t.previousStarted, t.previousInterval, t.hasPrevious = round.Started, round.Interval, true
	return changes, nil
}

func validObservation(observation byte) bool {
	switch observation {
	case EchoReply, Rejected, TCPSeen, Unreachable, ExecutionErr, NoName, NoResponse:
		return true
	default:
		return false
	}
}

func isSeen(observation byte) bool {
	return observation == EchoReply || observation == Rejected || observation == TCPSeen
}

func cloneRow(row Row) Row {
	copyRow := Row{Address: row.Address, Result: row.Result, TCP: make(map[int]string, len(row.TCP))}
	for port, state := range row.TCP {
		copyRow.TCP[port] = state
	}
	return copyRow
}

func countTrue(values []bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func (t *Tracker) Cells(pending map[int]byte) []byte {
	cells := make([]byte, len(t.hosts))
	for i := range cells {
		observation := Unprobed
		if t.stats.Rounds > 0 {
			observation = t.current[i]
		}
		if char, ok := pending[i]; ok {
			observation = char
		}
		switch {
		case isSeen(observation):
			cells[i] = observation
		case t.everSeen[i]:
			cells[i] = Gone
		case t.unreachable[i] || observation == Unreachable:
			cells[i] = Unreachable
		default:
			cells[i] = observation
		}
	}
	return cells
}

func (t *Tracker) Changed(pending map[int]byte) []int {
	indexes := make([]int, 0)
	for i, changed := range t.changed {
		if char, reached := pending[i]; reached {
			if isSeen(char) != t.seenNow[i] {
				indexes = append(indexes, i)
			}
		} else if changed {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func (t *Tracker) Partial(pending map[int]byte, answeredPartly map[int]bool) []int {
	seen := make(map[int]bool)
	for index, isPartial := range answeredPartly {
		if isPartial {
			seen[index] = true
		}
	}
	for i, partial := range t.partial {
		if partial {
			if _, reached := pending[i]; !reached {
				seen[i] = true
			}
		}
	}
	indexes := make([]int, 0, len(seen))
	for index := range seen {
		if index >= 0 && index < len(t.hosts) {
			indexes = append(indexes, index)
		}
	}
	sort.Ints(indexes)
	return indexes
}

func (t *Tracker) Counts(pending map[int]byte) map[byte]int {
	counts := make(map[byte]int)
	for _, observation := range t.Cells(pending) {
		counts[observation]++
	}
	return counts
}

func (t *Tracker) Summaries(latestRoundLive bool) []HostSummary {
	rows := make([]HostSummary, 0, t.stats.Seen)
	cells := t.Cells(nil)
	for i, host := range t.hosts {
		if !t.everSeen[i] {
			continue
		}
		latest, tcp, address := "", map[int]string{}, ""
		if t.seenNow[i] {
			if t.hasSeenRow[i] {
				row := t.lastSeenRows[i]
				latest, address = row.Result, row.Address
				for port, state := range row.TCP {
					if containsPort(t.ports, port) {
						tcp[port] = state
					}
				}
			}
		} else {
			latest, _ = ResultText(t.current[i])
			if t.hasSeenRow[i] {
				address = t.lastSeenRows[i].Address
			}
		}
		periods := append([]Period(nil), t.periods[i]...)
		rows = append(rows, HostSummary{
			Index: i, Host: host, Address: address, Latest: latest, TCP: tcp,
			FirstSeen: t.firstSeen[i], LastSeen: t.lastSeen[i], SeenRounds: t.seenRounds[i], Rounds: t.stats.Rounds,
			Periods: periods, Ongoing: t.seenNow[i] && latestRoundLive, Partial: t.partial[i], Cell: cells[i],
		})
	}
	return rows
}

func containsPort(ports []int, port int) bool {
	for _, requested := range ports {
		if requested == port {
			return true
		}
	}
	return false
}

func (t *Tracker) Stats() Stats { return t.stats }
