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

	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/target"
)

// WriteRunJSON writes a target-run report. The section and row meanings follow
// cn-tool 0.8.0's ping_monitor.py JSON path (commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa);
// the numeric interval and UTC-year formatting follow the reviewed native spec.
func WriteRunJSON(w io.Writer, request target.Request, outcome monitor.RunOutcome, rejected []target.Rejection) error {
	return writeRunJSON(w, request, outcome, rejected, false, false)
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
	From string  `json:"from"`
	To   *string `json:"to"`
}

type historyReport struct {
	Seen    []seenJSON    `json:"seen"`
	Latest  []latestJSON  `json:"latest"`
	Summary []summaryJSON `json:"summary"`
}

type requestsReport struct {
	Requests []requestJSON `json:"requests"`
}

type requestJSON struct {
	ID        string `json:"id"`
	Targets   string `json:"targets"`
	TCP       string `json:"tcp"`
	Runs      int64  `json:"runs"`
	Rounds    int64  `json:"rounds"`
	LastRound string `json:"last_round"`
	State     string `json:"state"`
	Detail    string `json:"detail"`
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
