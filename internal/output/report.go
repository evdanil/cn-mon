// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/target"
)

// ReportOptions controls human report presentation. Structured formats ignore
// width, theme, terminal and color settings.
type ReportOptions struct {
	Format, Theme     string
	Width             int
	Terminal, NoColor bool
}

type reportSection struct {
	name    string
	headers []string
	rows    [][]string
}

func WriteRun(w io.Writer, request target.Request, out monitor.RunOutcome, rejected []target.Rejection, options ReportOptions) error {
	format := normalizeFormat(options.Format)
	if format == "json" {
		return writeRunJSON(w, request, out, rejected, false, false)
	}
	sections := runSections(request, out, rejected, false, false, format == "table")
	return writeHuman(w, sections, options, format)
}

func WriteHistory(w io.Writer, out monitor.ReadOutcome, options ReportOptions) error {
	format := normalizeFormat(options.Format)
	if format == "json" {
		return writeHistoryJSON(w, out)
	}
	return writeHuman(w, runSections(out.Request, out.Run, nil, true, out.Running, format == "table"), options, format)
}

func WriteRequests(w io.Writer, rows []monitor.RequestRow, options ReportOptions) error {
	format := normalizeFormat(options.Format)
	if format == "json" {
		return writeRequestsJSON(w, rows)
	}
	section := reportSection{name: "requests", headers: []string{"ID", "Targets", "TCP", "Runs", "Rounds", "Last round", "State"}}
	for _, row := range rows {
		section.rows = append(section.rows, []string{row.ID, row.Targets, row.TCP, fmt.Sprint(row.Runs), fmt.Sprint(row.Rounds), formatOptionalTime(row.LastRound), stateText(row.State, row.Detail)})
	}
	return writeHuman(w, []reportSection{section}, options, format)
}

func normalizeFormat(format string) string {
	if format == "" {
		return "table"
	}
	return format
}

func runSections(request target.Request, out monitor.RunOutcome, rejected []target.Rejection, historical, running, table bool) []reportSection {
	ports := sortedPorts(request.Ports)
	seenHeaders := []string{"Host", "Address", "Latest"}
	seenHeaders = appendPortHeaders(seenHeaders, ports)
	seenHeaders = append(seenHeaders, "First seen", "Last seen", "Seen", "Online")
	seen := reportSection{name: "seen", headers: seenHeaders}
	withDates := !out.Stats.FirstRound.IsZero() && !sameUTCDay(out.Stats.FirstRound, out.Stats.LastRound)
	for _, i := range orderedIndices(request.Hosts, out.Seen, func(row monitor.HostSummary) string { return row.Host }) {
		row := out.Seen[i]
		values := []string{row.Host, row.Address, row.Latest}
		for _, port := range ports {
			values = append(values, row.TCP[port])
		}
		values = append(values, formatTimestamp(row.FirstSeen), formatTimestamp(row.LastSeen), fmt.Sprintf("%d of %d rounds", row.SeenRounds, row.Rounds), formatPeriods(row.Periods, row.Ongoing && historical && running, withDates, table))
		seen.rows = append(seen.rows, values)
	}

	latestHeaders := []string{"Host", "Address", "Result"}
	latestHeaders = appendPortHeaders(latestHeaders, ports)
	latest := reportSection{name: "latest", headers: latestHeaders}
	for _, i := range orderedIndices(request.Hosts, out.Latest, func(row monitor.LatestRow) string { return row.Host }) {
		row := out.Latest[i]
		values := []string{row.Host, row.Row.Address, row.Row.Result}
		for _, port := range ports {
			values = append(values, row.Row.TCP[port])
		}
		latest.rows = append(latest.rows, values)
	}

	sections := []reportSection{seen, latest}
	if out.Stats.Rounds > 0 {
		interval := "one round"
		if out.Interval != nil {
			interval = formatSpan(*out.Interval)
		}
		sections = append(sections, reportSection{name: "summary", headers: []string{"ID", "Started", "Ended", "Rounds", "Earlier rounds", "Interval", "Hosts", "Seen", "Answering"}, rows: [][]string{{shortID(request.Key), formatTimestamp(out.Stats.FirstRound), formatTimestamp(out.Stats.LastRound), fmt.Sprint(out.CurrentRounds), fmt.Sprint(out.EarlierRounds), interval, fmt.Sprint(len(request.Hosts)), fmt.Sprint(out.Stats.Seen), fmt.Sprint(out.Stats.Answering)}}})
	}
	var notFound reportSection
	if !historical && len(rejected)+len(out.Unresolved) > 0 {
		notFound = reportSection{name: "not_found", headers: []string{"Object", "Reason"}}
		for _, item := range rejected {
			notFound.rows = append(notFound.rows, []string{item.Object, item.Reason})
		}
		unresolved := append([]string(nil), out.Unresolved...)
		sort.Strings(unresolved)
		for _, host := range unresolved {
			notFound.rows = append(notFound.rows, []string{host, "no such host"})
		}
		sections = append(sections, notFound)
	}
	return sections
}

func appendPortHeaders(headers []string, ports []int) []string {
	for _, port := range ports {
		headers = append(headers, fmt.Sprintf("TCP %d", port))
	}
	return headers
}

func formatPeriods(periods []monitor.Period, open, withDates, table bool) string {
	if len(periods) == 0 {
		return ""
	}
	start := 0
	if table && len(periods) > 3 {
		start = len(periods) - 3
	}
	var values []string
	for i := start; i < len(periods); i++ {
		period := periods[i]
		from, to := period.From.UTC(), period.To.UTC()
		left := from.Format("15:04")
		right := to.Format("15:04")
		if withDates {
			left = from.Format("01-02 15:04")
			right = to.Format("01-02 15:04")
		}
		if open && i == len(periods)-1 {
			right = "now"
		}
		values = append(values, left+"–"+right)
	}
	result := strings.Join(values, ", ")
	if start > 0 {
		result += fmt.Sprintf(" (+%d earlier)", start)
	}
	return result
}

func sameUTCDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

func formatSpan(span time.Duration) string {
	seconds := int64(span / time.Second)
	for _, unit := range []struct {
		name string
		secs int64
	}{{"d", 86400}, {"h", 3600}, {"m", 60}} {
		if seconds >= unit.secs && seconds%unit.secs == 0 {
			return fmt.Sprintf("%d%s", seconds/unit.secs, unit.name)
		}
	}
	return fmt.Sprintf("%ds", seconds)
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTimestamp(*value)
}

func stateText(state, detail string) string {
	if state == "stopped" {
		return "stopped early"
	}
	if state == "running" && detail != "" {
		return "running: " + detail
	}
	return strings.TrimSpace(state + " " + detail)
}

func writeHuman(w io.Writer, sections []reportSection, options ReportOptions, format string) error {
	sections = nonemptySections(sections)
	switch format {
	case "csv":
		if len(sections) == 0 {
			return nil
		}
		return writeCSV(w, sections)
	case "md":
		if len(sections) == 0 {
			return nil
		}
		return writeMarkdown(w, sections)
	case "table":
		if len(sections) == 0 {
			return nil
		}
		return writeTable(w, sections, options)
	default:
		return fmt.Errorf("unsupported report format %q", format)
	}
}

func nonemptySections(sections []reportSection) []reportSection {
	result := sections[:0]
	for _, section := range sections {
		if len(section.rows) > 0 {
			result = append(result, section)
		}
	}
	return result
}

func writeCSV(w io.Writer, sections []reportSection) error {
	columns := make([]string, 0)
	seen := make(map[string]bool)
	for _, section := range sections {
		for _, header := range section.headers {
			if !seen[header] {
				seen[header] = true
				columns = append(columns, header)
			}
		}
	}
	cw := csv.NewWriter(w)
	header := append([]string{"section"}, columns...)
	if err := cw.Write(header); err != nil {
		return fmt.Errorf("write report CSV: %w", err)
	}
	for _, section := range sections {
		positions := make(map[string]int, len(section.headers))
		for i, column := range section.headers {
			positions[column] = i
		}
		for _, row := range section.rows {
			record := make([]string, len(columns)+1)
			record[0] = section.name
			for i, column := range columns {
				if pos, ok := positions[column]; ok && pos < len(row) {
					record[i+1] = row[pos]
				}
			}
			if err := cw.Write(record); err != nil {
				return fmt.Errorf("write report CSV: %w", err)
			}
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("write report CSV: %w", err)
	}
	return nil
}

func writeMarkdown(w io.Writer, sections []reportSection) error {
	var blocks []string
	for _, section := range sections {
		var b strings.Builder
		fmt.Fprintf(&b, "## %s\n\n", strings.ToUpper(section.name))
		writeMDRow(&b, section.headers)
		separators := make([]string, len(section.headers))
		for i := range separators {
			separators[i] = "---"
		}
		writeMDRow(&b, separators)
		for _, row := range section.rows {
			writeMDRow(&b, row)
		}
		blocks = append(blocks, strings.TrimSuffix(b.String(), "\n"))
	}
	_, err := io.WriteString(w, strings.Join(blocks, "\n\n")+"\n")
	if err != nil {
		return fmt.Errorf("write report Markdown: %w", err)
	}
	return nil
}

func writeMDRow(w io.Writer, cells []string) {
	io.WriteString(w, "| ")
	for i, cell := range cells {
		if i > 0 {
			io.WriteString(w, " | ")
		}
		cell = strings.ReplaceAll(cell, "|", `\|`)
		cell = strings.ReplaceAll(cell, "\n", "<br/>")
		io.WriteString(w, cell)
	}
	io.WriteString(w, " |\n")
}

func writeTable(w io.Writer, sections []reportSection, options ReportOptions) error {
	if options.Width <= 0 {
		options.Width = 100
	}
	lines := make([]line, 0)
	for s, section := range sections {
		if s > 0 {
			lines = append(lines, nil)
		}
		for _, title := range wrapCells(terminalText(strings.ToUpper(section.name)), options.Width) {
			lines = append(lines, line{{text: title, style: "title"}})
		}
		lines = append(lines, layoutTable(section.headers, section.rows, options.Width)...)
	}
	if err := writeStyled(w, lines, options.Theme, options.Terminal, options.Terminal && !options.NoColor); err != nil {
		return fmt.Errorf("write report table: %w", err)
	}
	return nil
}

func layoutTable(headers []string, rows [][]string, width int) []line {
	if width <= 0 {
		width = 100
	}
	if len(headers) == 0 {
		return nil
	}
	if width < len(headers)*3 {
		var lines []line
		for _, row := range rows {
			for i, header := range headers {
				value := ""
				if i < len(row) {
					value = row[i]
				}
				for _, part := range wrapCells(header+": "+terminalText(value), max(1, width)) {
					lines = append(lines, line{{text: part, style: tableRole(i)}})
				}
			}
		}
		return lines
	}
	sep := 2 * (len(headers) - 1)
	natural := make([]int, len(headers))
	for i, header := range headers {
		natural[i] = max(1, displayWidth(terminalText(header)))
	}
	for _, row := range rows {
		for i := range headers {
			if i < len(row) {
				natural[i] = max(natural[i], displayWidth(terminalText(row[i])))
			}
		}
	}
	widths := allocateTableWidths(natural, width-sep)
	buildRow := func(values []string) []line {
		parts := make([][]string, len(headers))
		height := 1
		for i := range headers {
			value := ""
			if i < len(values) {
				value = terminalText(values[i])
			}
			parts[i] = wrapCells(value, widths[i])
			height = max(height, len(parts[i]))
		}
		out := make([]line, height)
		for rowIndex := 0; rowIndex < height; rowIndex++ {
			for column := range headers {
				if column > 0 {
					out[rowIndex] = append(out[rowIndex], segment{text: "  "})
				}
				value := ""
				if rowIndex < len(parts[column]) {
					value = parts[column][rowIndex]
				}
				pad := max(0, widths[column]-displayWidth(value))
				out[rowIndex] = append(out[rowIndex], segment{text: value, style: tableRole(column)})
				if pad > 0 {
					out[rowIndex] = append(out[rowIndex], segment{text: strings.Repeat(" ", pad)})
				}
			}
		}
		return out
	}
	result := buildRow(headers)
	for _, row := range rows {
		result = append(result, buildRow(row)...)
	}
	return result
}

func allocateTableWidths(natural []int, budget int) []int {
	widths := make([]int, len(natural))
	if len(natural) == 0 {
		return widths
	}
	total := 0
	maxWidth := 0
	for _, width := range natural {
		total += width
		maxWidth = max(maxWidth, width)
	}
	if total <= budget {
		copy(widths, natural)
		return widths
	}
	low, high := 1, maxWidth
	for low < high {
		middle := low + (high-low+1)/2
		used := 0
		for _, width := range natural {
			used += min(width, middle)
		}
		if used <= budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	used := 0
	for i, width := range natural {
		widths[i] = min(width, low)
		used += widths[i]
	}
	for remaining := budget - used; remaining > 0; {
		progress := false
		for i, width := range natural {
			if widths[i] >= width {
				continue
			}
			widths[i]++
			remaining--
			progress = true
			if remaining == 0 {
				break
			}
		}
		if !progress {
			break
		}
	}
	return widths
}

func tableRole(index int) string { return tableRoles[index%len(tableRoles)] }

func writeRunJSON(w io.Writer, request target.Request, outcome monitor.RunOutcome, rejected []target.Rejection, historical, running bool) error {
	report := buildRunReport(request, outcome, rejected, historical, running)
	return encodeReport(w, report)
}

func writeHistoryJSON(w io.Writer, out monitor.ReadOutcome) error {
	report := buildRunReport(out.Request, out.Run, nil, true, out.Running)
	return encodeReport(w, historyReport{Seen: report.Seen, Latest: report.Latest, Summary: report.Summary})
}

func buildRunReport(request target.Request, outcome monitor.RunOutcome, rejected []target.Rejection, historical, running bool) runReport {
	ports := sortedPorts(request.Ports)
	report := runReport{Seen: make([]seenJSON, 0, len(outcome.Seen)), Latest: make([]latestJSON, 0, len(outcome.Latest)), Summary: make([]summaryJSON, 0, 1), NotFound: make([]notFoundJSON, 0, len(rejected)+len(outcome.Unresolved))}
	for _, i := range orderedIndices(request.Hosts, outcome.Seen, func(row monitor.HostSummary) string { return row.Host }) {
		row := outcome.Seen[i]
		online := make([]onlineJSON, 0, len(row.Periods))
		for p, period := range row.Periods {
			from, to := formatTimestamp(period.From), formatTimestamp(period.To)
			var end *string = &to
			if historical && running && row.Ongoing && p == len(row.Periods)-1 {
				end = nil
			}
			online = append(online, onlineJSON{From: from, To: end})
		}
		report.Seen = append(report.Seen, seenJSON{host: row.Host, address: row.Address, latest: row.Latest, tcp: row.TCP, ports: ports, firstSeen: formatTimestamp(row.FirstSeen), lastSeen: formatTimestamp(row.LastSeen), seenRounds: row.SeenRounds, rounds: row.Rounds, online: online})
	}
	for _, i := range orderedIndices(request.Hosts, outcome.Latest, func(row monitor.LatestRow) string { return row.Host }) {
		row := outcome.Latest[i]
		report.Latest = append(report.Latest, latestJSON{host: row.Host, address: row.Row.Address, result: row.Row.Result, tcp: row.Row.TCP, ports: ports})
	}
	if outcome.Stats.Rounds > 0 {
		var interval *float64
		if outcome.Interval != nil {
			seconds := outcome.Interval.Seconds()
			interval = &seconds
		}
		report.Summary = append(report.Summary, summaryJSON{ID: shortID(request.Key), Started: formatTimestamp(outcome.Stats.FirstRound), Ended: formatTimestamp(outcome.Stats.LastRound), Rounds: outcome.CurrentRounds, EarlierRounds: outcome.EarlierRounds, Interval: interval, Hosts: len(request.Hosts), Seen: outcome.Stats.Seen, Answering: outcome.Stats.Answering})
	}
	for _, item := range rejected {
		report.NotFound = append(report.NotFound, notFoundJSON{Object: item.Object, Reason: item.Reason})
	}
	unresolved := append([]string(nil), outcome.Unresolved...)
	sort.Strings(unresolved)
	for _, host := range unresolved {
		report.NotFound = append(report.NotFound, notFoundJSON{Object: host, Reason: "no such host"})
	}
	return report
}

func encodeReport(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode report JSON: %w", err)
	}
	return nil
}

func writeRequestsJSON(w io.Writer, rows []monitor.RequestRow) error {
	encoded := make([]requestJSON, 0, len(rows))
	for _, row := range rows {
		encoded = append(encoded, requestJSON{ID: row.ID, Targets: row.Targets, TCP: row.TCP, Runs: row.Runs, Rounds: row.Rounds, LastRound: formatOptionalTime(row.LastRound), State: row.State, Detail: row.Detail})
	}
	return encodeReport(w, requestsReport{Requests: encoded})
}
