// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
)

func TestWriteRunsJSONPreservesExactIDAndEmptyArray(t *testing.T) {
	started := time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC)
	ended := started.Add(time.Minute)
	row := RunRecord{ID: "Case/日本語/Δ", RequestID: strings.Repeat("ab", 32), Status: "finished", State: "finished", Started: started, Ended: &ended, Host: "host", PID: 42, Rounds: 3}
	var output bytes.Buffer
	if err := WriteRuns(&output, []RunRecord{row}, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode run JSON: %v; output=%q", err, output.String())
	}
	if len(report.Runs) != 1 {
		t.Fatalf("runs=%d, want one", len(report.Runs))
	}
	var actualID, actualKey, status, state, host string
	for key, target := range map[string]*string{"id": &actualID, "request_id": &actualKey, "status": &status, "state": &state, "host": &host} {
		if err := json.Unmarshal(report.Runs[0][key], target); err != nil {
			t.Fatalf("decode %s: %v", key, err)
		}
	}
	if actualID != row.ID || actualKey != row.RequestID || status != row.Status || state != row.State || host != row.Host {
		t.Fatalf("run JSON lost metadata: id=%q key=%q status=%q state=%q host=%q", actualID, actualKey, status, state, host)
	}
	output.Reset()
	if err := WriteRuns(&output, nil, ReportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Runs == nil || len(report.Runs) != 0 {
		t.Fatalf("empty run list=%q err=%v, want runs empty array", output.String(), err)
	}
}

func TestWriteRunsHumanFormatsDoNotTruncateID(t *testing.T) {
	id := "exact/very-long/Case|Δ\\run"
	row := RunRecord{ID: id, RequestID: strings.Repeat("a", 64), Status: "finished", State: "finished", Started: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), Host: "host", PID: 7, Rounds: 1}
	for _, format := range []string{"table", "md", "csv"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteRuns(&output, []RunRecord{row}, ReportOptions{Format: format, Width: 20}); err != nil {
				t.Fatal(err)
			}
			switch format {
			case "table":
				if !strings.Contains(strings.ReplaceAll(output.String(), "\n", ""), strconv.Quote(id)) {
					t.Fatalf("table lost complete quoted ID %q: %q", id, output.String())
				}
			case "md":
				if !strings.Contains(output.String(), `"exact/very-long/Case\|Δ\\run"`) {
					t.Fatalf("Markdown ID not fully JSON-quoted/pipe escaped: %q", output.String())
				}
			case "csv":
				records, err := csv.NewReader(bytes.NewReader(output.Bytes())).ReadAll()
				if err != nil || len(records) != 2 || records[1][0] != "runs" || records[1][1] != id {
					t.Fatalf("CSV exact ID=%q records=%v err=%v", id, records, err)
				}
			}
		})
	}
}

func TestWriteRunsHumanFormatsQuoteControlIDsAsJSONLiterals(t *testing.T) {
	id := "x\x00escape\x1b[31m"
	wantLiteral := `"x\u0000escape\u001b[31m"`
	row := RunRecord{ID: id, RequestID: strings.Repeat("b", 64), Status: "finished", State: "finished", Started: time.Now().UTC(), Host: "host", PID: 9, Rounds: 1}
	for _, format := range []string{"table", "md"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteRuns(&output, []RunRecord{row}, ReportOptions{Format: format, Width: 300}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), wantLiteral) || strings.ContainsRune(output.String(), '\x00') || strings.ContainsRune(output.String(), '\x1b') {
				t.Fatalf("%s did not emit a safe JSON string literal for the ID: %q", format, output.String())
			}
			var decoded string
			if err := json.Unmarshal([]byte(wantLiteral), &decoded); err != nil || decoded != id {
				t.Fatalf("quoted ID does not round-trip: decoded=%q err=%v", decoded, err)
			}
		})
	}
}

func TestWriteDeletionAllFormatsReportsFiveValues(t *testing.T) {
	result := history.DeleteResult{Requests: 2, Runs: 3, Rounds: 4, Results: 5, Committed: true}
	for _, format := range []string{"json", "table", "md", "csv"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteDeletion(&output, result, ReportOptions{Format: format}); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"true", "2", "3", "4", "5"} {
				if !strings.Contains(output.String(), value) {
					t.Errorf("%s deletion report missing %q: %q", format, value, output.String())
				}
			}
		})
	}
}
