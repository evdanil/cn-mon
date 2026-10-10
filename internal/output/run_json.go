// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

// Package output builds command reports without performing application I/O.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

// WriteRunJSON writes a target-run report. The section and row meanings follow
// cn-tool 0.8.0's ping_monitor.py JSON path (commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa);
// the numeric interval and UTC-year formatting follow the reviewed native spec.
func WriteRunJSON(w io.Writer, request target.Request, outcome monitor.RunOutcome, rejected []target.Rejection) error {
	ports := sortedPorts(request.Ports)
	report := runReport{
		Seen:    make([]seenJSON, 0, len(outcome.Seen)),
		Latest:  make([]latestJSON, 0, len(outcome.Latest)),
		Summary: make([]summaryJSON, 0, 1),
		NotFound: make([]notFoundJSON, 0,
			len(rejected)+len(outcome.Unresolved)),
	}

	for _, i := range orderedIndices(request.Hosts, outcome.Seen, func(row monitor.HostSummary) string { return row.Host }) {
		row := outcome.Seen[i]
		online := make([]onlineJSON, 0, len(row.Periods))
		for _, period := range row.Periods {
			from, to := formatTimestamp(period.From), formatTimestamp(period.To)
			online = append(online, onlineJSON{From: from, To: to})
		}
		report.Seen = append(report.Seen, seenJSON{
			host: row.Host, address: row.Address, latest: row.Latest,
			tcp: row.TCP, ports: ports,
			firstSeen: formatTimestamp(row.FirstSeen), lastSeen: formatTimestamp(row.LastSeen),
			seenRounds: row.SeenRounds, rounds: row.Rounds, online: online,
		})
	}

	for _, i := range orderedIndices(request.Hosts, outcome.Latest, func(row monitor.LatestRow) string { return row.Host }) {
		row := outcome.Latest[i]
		report.Latest = append(report.Latest, latestJSON{
			host: row.Host, address: row.Row.Address, result: row.Row.Result,
			tcp: row.Row.TCP, ports: ports,
		})
	}

	if outcome.Stats.Rounds > 0 {
		var interval *float64
		if outcome.Interval != nil {
			seconds := outcome.Interval.Seconds()
			interval = &seconds
		}
		report.Summary = append(report.Summary, summaryJSON{
			ID: shortID(request.Key), Started: formatTimestamp(outcome.Stats.FirstRound),
			Ended: formatTimestamp(outcome.Stats.LastRound), Rounds: outcome.CurrentRounds,
			EarlierRounds: outcome.EarlierRounds, Interval: interval,
			Hosts: len(request.Hosts), Seen: outcome.Stats.Seen, Answering: outcome.Stats.Answering,
		})
	}

	for _, item := range rejected {
		report.NotFound = append(report.NotFound, notFoundJSON{Object: item.Object, Reason: item.Reason})
	}
	unresolved := append([]string(nil), outcome.Unresolved...)
	sort.Strings(unresolved)
	for _, host := range unresolved {
		report.NotFound = append(report.NotFound, notFoundJSON{Object: host, Reason: "no such host"})
	}

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode target run JSON: %w", err)
	}
	return nil
}

type runReport struct {
	Seen     []seenJSON     `json:"seen"`
	Latest   []latestJSON   `json:"latest"`
	Summary  []summaryJSON  `json:"summary"`
	NotFound []notFoundJSON `json:"not_found"`
}

type summaryJSON struct {
	ID            string   `json:"id"`
	Started       string   `json:"started"`
	Ended         string   `json:"ended"`
	Rounds        int      `json:"rounds"`
	EarlierRounds int      `json:"earlier_rounds"`
	Interval      *float64 `json:"interval"`
	Hosts         int      `json:"hosts"`
	Seen          int      `json:"seen"`
	Answering     int      `json:"answering"`
}

type notFoundJSON struct {
	Object string `json:"object"`
	Reason string `json:"reason"`
}

type onlineJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type seenJSON struct {
	host, address, latest string
	tcp                   map[int]string
	ports                 []int
	firstSeen, lastSeen   string
	seenRounds, rounds    int
	online                []onlineJSON
}

func (row seenJSON) MarshalJSON() ([]byte, error) {
	fields := []orderedField{
		{name: "host", value: row.host},
		{name: "address", value: row.address},
		{name: "latest", value: row.latest},
	}
	fields = appendTCPFields(fields, row.ports, row.tcp)
	fields = append(fields,
		orderedField{name: "first_seen", value: row.firstSeen},
		orderedField{name: "last_seen", value: row.lastSeen},
		orderedField{name: "seen_rounds", value: row.seenRounds},
		orderedField{name: "rounds", value: row.rounds},
		orderedField{name: "online", value: row.online},
	)
	return marshalOrdered(fields)
}

type latestJSON struct {
	host, address, result string
	tcp                   map[int]string
	ports                 []int
}

func (row latestJSON) MarshalJSON() ([]byte, error) {
	fields := []orderedField{
		{name: "host", value: row.host},
		{name: "address", value: row.address},
		{name: "result", value: row.result},
	}
	fields = appendTCPFields(fields, row.ports, row.tcp)
	return marshalOrdered(fields)
}

type orderedField struct {
	name  string
	value any
}

func marshalOrdered(fields []orderedField) ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			out.WriteByte(',')
		}
		key, err := marshalCompact(field.name)
		if err != nil {
			return nil, err
		}
		value, err := marshalCompact(field.value)
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func marshalCompact(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func appendTCPFields(fields []orderedField, ports []int, values map[int]string) []orderedField {
	for _, port := range ports {
		fields = append(fields, orderedField{name: fmt.Sprintf("tcp_%d", port), value: values[port]})
	}
	return fields
}

func sortedPorts(ports []int) []int {
	sorted := append([]int(nil), ports...)
	sort.Ints(sorted)
	unique := sorted[:0]
	for _, port := range sorted {
		if len(unique) == 0 || unique[len(unique)-1] != port {
			unique = append(unique, port)
		}
	}
	return unique
}

func orderedIndices[T any](hosts []string, rows []T, hostOf func(T) string) []int {
	order := make(map[string]int, len(hosts))
	for i, host := range hosts {
		order[host] = i
	}
	indices := make([]int, len(rows))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		leftHost, rightHost := hostOf(rows[indices[i]]), hostOf(rows[indices[j]])
		left, leftOK := order[leftHost]
		right, rightOK := order[rightHost]
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && left != right {
			return left < right
		}
		return leftHost < rightHost
	})
	return indices
}

func formatTimestamp(value time.Time) string {
	utc := value.UTC()
	year, month, day := utc.Date()
	hour, minute, second := utc.Clock()
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d+00:00", year, month, day, hour, minute, second)
}

func shortID(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}
