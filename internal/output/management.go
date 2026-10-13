// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
)

// RunRecord is the presentation-safe history row emitted by --list-runs.
// RequestID contains the full canonical key rather than the CLI prefix.
type RunRecord struct {
	ID        string
	RequestID string
	Status    string
	State     string
	Started   time.Time
	Ended     *time.Time
	Host      string
	PID       int64
	Rounds    int64
}

func WriteRuns(w io.Writer, rows []RunRecord, options ReportOptions) error {
	format := normalizeFormat(options.Format)
	if format == "json" {
		encoded := make([]runJSON, 0, len(rows))
		for _, row := range rows {
			encoded = append(encoded, runJSON{ID: row.ID, RequestID: row.RequestID, Status: row.Status, State: row.State, Started: formatTimestamp(row.Started), Ended: formatNullableTime(row.Ended), Host: row.Host, PID: row.PID, Rounds: row.Rounds})
		}
		return encodeReport(w, struct {
			Runs []runJSON `json:"runs"`
		}{Runs: encoded})
	}
	if len(rows) == 0 {
		return nil
	}
	section := reportSection{name: "runs", headers: []string{"ID", "Request ID", "Status", "State", "Started", "Ended", "Host", "PID", "Rounds"}}
	for _, row := range rows {
		id := row.ID
		if format == "table" || format == "md" {
			quoted, err := json.Marshal(id)
			if err != nil {
				return fmt.Errorf("quote run ID: %w", err)
			}
			id = string(quoted)
		}
		section.rows = append(section.rows, []string{
			id, row.RequestID, row.Status, row.State, formatTimestamp(row.Started),
			formatOptionalTime(row.Ended), row.Host, fmt.Sprint(row.PID), fmt.Sprint(row.Rounds),
		})
	}
	return writeHuman(w, []reportSection{section}, options, format)
}

func WriteDeletion(w io.Writer, result history.DeleteResult, options ReportOptions) error {
	format := normalizeFormat(options.Format)
	deleted := deletionCounts{Requests: result.Requests, Runs: result.Runs, Rounds: result.Rounds, Results: result.Results}
	if format == "json" {
		return encodeReport(w, struct {
			Committed bool           `json:"committed"`
			Deleted   deletionCounts `json:"deleted"`
		}{Committed: result.Committed, Deleted: deleted})
	}
	section := reportSection{name: "deletion", headers: []string{"Committed", "Requests", "Runs", "Rounds", "Results"}, rows: [][]string{{
		fmt.Sprint(result.Committed), fmt.Sprint(result.Requests), fmt.Sprint(result.Runs), fmt.Sprint(result.Rounds), fmt.Sprint(result.Results),
	}}}
	return writeHuman(w, []reportSection{section}, options, format)
}

type runJSON struct {
	ID        string  `json:"id"`
	RequestID string  `json:"request_id"`
	Status    string  `json:"status"`
	State     string  `json:"state"`
	Started   string  `json:"started"`
	Ended     *string `json:"ended"`
	Host      string  `json:"host"`
	PID       int64   `json:"pid"`
	Rounds    int64   `json:"rounds"`
}

func formatNullableTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTimestamp(*value)
	return &formatted
}

type deletionCounts struct {
	Requests int64 `json:"requests"`
	Runs     int64 `json:"runs"`
	Rounds   int64 `json:"rounds"`
	Results  int64 `json:"results"`
}
