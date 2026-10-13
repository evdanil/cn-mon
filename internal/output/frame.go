// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"fmt"
	"io"
	"math"
	"net/netip"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/target"
)

type segment struct{ text, style string }
type line []segment

func writeStyledFrame(w io.Writer, lines []line, theme string, decorate, color bool) error {
	return writeStyledLines(w, lines, theme, decorate, color, false)
}

func writeStyled(w io.Writer, lines []line, theme string, decorate, color bool) error {
	return writeStyledLines(w, lines, theme, decorate, color, true)
}

func writeStyledLines(w io.Writer, lines []line, theme string, decorate, color, trailingNewline bool) error {
	for i, current := range lines {
		for _, part := range current {
			text := terminalText(part.text)
			if decorate {
				style := styleSequence(part.style, theme, color)
				if style != "" {
					if _, err := io.WriteString(w, style); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(w, text); err != nil {
					return err
				}
				if style != "" {
					if _, err := io.WriteString(w, "\x1b[0m"); err != nil {
						return err
					}
				}
			} else if _, err := io.WriteString(w, text); err != nil {
				return err
			}
		}
		if trailingNewline || i+1 < len(lines) {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

func changeLine(change monitor.Change) line {
	text, style := changeText(change.Observation, change.Seen)
	return line{
		{text: change.Time.UTC().Format("15:04:05") + "  ", style: "dim"},
		{text: change.Host, style: "description"},
		{text: "  " + text, style: style},
	}
}

func changeText(observation byte, seen bool) (string, string) {
	if !seen {
		switch observation {
		case monitor.Unreachable:
			return "went silent (unreachable)", "warning"
		case monitor.ExecutionErr:
			return "went silent (error)", "warning"
		case monitor.NoName:
			return "went silent (name did not resolve)", "warning"
		default:
			return "went silent", "warning"
		}
	}
	switch observation {
	case monitor.EchoReply:
		return "replied", "success"
	case monitor.Rejected:
		return "rejected", "info"
	case monitor.TCPSeen:
		return "answered on TCP", "info"
	default:
		return fmt.Sprintf("observed %q", observation), "text"
	}
}

func joinLines(lines []line) string {
	var b strings.Builder
	_ = writeStyled(&b, lines, "", false, false)
	return b.String()
}

type liveBlock struct {
	title   string
	prefix  netip.Prefix
	count   uint64
	members []int
	names   []hostName
}

type hostName struct {
	text  string
	index int
}

type liveLayout struct {
	request target.Request
	index   map[string]int
	blocks  []liveBlock
}

func newLiveLayout(request target.Request) *liveLayout {
	request.Tokens = append([]string(nil), request.Tokens...)
	request.Ports = append([]int(nil), request.Ports...)
	request.Hosts = append([]string(nil), request.Hosts...)
	layout := &liveLayout{request: request, index: make(map[string]int, len(request.Hosts))}
	represented := make([]bool, len(request.Hosts))
	for i, host := range request.Hosts {
		layout.index[host] = i
	}
	var hostLabels []hostName
	for _, input := range target.LayoutInputs(request.Tokens) {
		if input.Network.IsValid() {
			prefix := input.Network.Masked()
			block := liveBlock{title: prefix.String(), prefix: prefix, count: prefixAddressCount(prefix)}
			for i, host := range request.Hosts {
				if address, err := netip.ParseAddr(host); err == nil && prefix.Contains(address) && address.String() == host {
					block.members = append(block.members, i)
					represented[i] = true
				}
			}
			layout.blocks = append(layout.blocks, block)
			continue
		}
		for _, host := range input.Hosts {
			if index, ok := layout.index[host]; ok {
				hostLabels = append(hostLabels, hostName{text: host, index: index})
				represented[index] = true
			}
		}
	}
	for i, host := range request.Hosts {
		if !represented[i] {
			hostLabels = append(hostLabels, hostName{text: host, index: i})
		}
	}
	if len(hostLabels) > 0 {
		block := liveBlock{title: "Hosts", names: hostLabels}
		for _, host := range hostLabels {
			block.members = append(block.members, host.index)
		}
		layout.blocks = append(layout.blocks, block)
	}
	return layout
}

func (layout *liveLayout) matches(request target.Request) bool {
	return layout != nil && layout.request.Key == request.Key && equalStrings(layout.request.Tokens, request.Tokens) && equalStrings(layout.request.Hosts, request.Hosts) && equalInts(layout.request.Ports, request.Ports)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func prefixAddressCount(prefix netip.Prefix) uint64 {
	shift := prefix.Addr().BitLen() - prefix.Bits()
	if shift >= 64 {
		return math.MaxUint64
	}
	return uint64(1) << uint(shift)
}

func addressAt(prefix netip.Prefix, offset uint64) netip.Addr {
	address := prefix.Masked().Addr()
	if address.Is4() {
		base := address.As4()
		value := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
		value += uint32(offset)
		return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
	}
	base := address.As16()
	carry := offset
	for i := len(base) - 1; i >= 0 && carry != 0; i-- {
		sum := uint64(base[i]) + (carry & 0xff)
		base[i] = byte(sum)
		carry = (carry >> 8) + (sum >> 8)
	}
	return netip.AddrFrom16(base)
}

func (layout *liveLayout) frame(update monitor.LiveUpdate, width, height int, table bool) []line {
	width = max(width, 1)
	height = max(height, 1)
	if layout == nil || !layout.matches(update.Request) {
		layout = newLiveLayout(update.Request)
	}
	header := liveHeader(update, width)
	counters := liveCounters(update, width)
	legend := liveLegend(update.Data.Cells, width)
	room := max(height-5-len(legend)-3, 1)
	var body []line
	if table {
		body = liveTable(update, width, room)
	} else {
		body = layout.grid(update, width, room)
	}
	lines := []line{{{text: header, style: "header"}}, {{text: counters, style: "text"}}, nil}
	lines = append(lines, body...)
	lines = append(lines, nil)
	lines = append(lines, legend...)
	footer := "q: stop · v: table · Ctrl+C: stop"
	if update.Phase == monitor.LiveFollowing {
		footer = "q: stop following, not the writer"
	}
	if update.Phase == monitor.LiveEnded {
		footer = "ended · q: stop following"
	}
	lines = append(lines, line{{text: cropCells(terminalText(footer), width), style: "dim"}})
	return cropFrame(lines, width, height)
}

func liveHeader(update monitor.LiveUpdate, width int) string {
	id := shortID(update.Request.Key)
	if update.Source != nil && *update.Source != "" {
		id = filepath.Base(*update.Source)
	}
	parts := []rankedPart{{"Ping Monitor", 1}}
	if update.Phase == monitor.LiveFollowing || update.Phase == monitor.LiveEnded {
		parts = append(parts, rankedPart{"request " + shortID(update.Request.Key), 5})
		last := "no round"
		if !update.Data.Stats.LastRound.IsZero() {
			last = update.Data.Stats.LastRound.UTC().Format("2006-01-02 15:04:05 UTC")
		}
		parts = append(parts, rankedPart{fmt.Sprintf("%d rounds · last %s", update.Data.Stats.Rounds, last), 7})
		if update.Phase == monitor.LiveEnded {
			parts = append(parts, rankedPart{"ended", 3})
		}
	} else {
		parts = append(parts, rankedPart{id, 5})
		round := update.CurrentRounds
		inFlight := update.Phase == monitor.LiveResolving || update.Phase == monitor.LiveProbing
		if inFlight {
			round++
		}
		roundText := fmt.Sprintf("round %d", round)
		if update.Until != nil && update.Interval != nil && *update.Interval > 0 {
			total := int(math.Ceil(update.Until.Sub(update.Origin).Seconds() / update.Interval.Seconds()))
			if total > 0 {
				roundText += fmt.Sprintf(" of up to %d", total)
			}
		}
		if inFlight && update.Total > 0 {
			roundText += fmt.Sprintf(": %s of %s pinged", groupedCount(update.Done), groupedCount(update.Total))
		}
		parts = append(parts, rankedPart{roundText, 7})
	}
	if !update.Now.IsZero() {
		parts = append(parts, rankedPart{update.Now.UTC().Format("15:04:05") + " UTC", 2})
	}
	if update.Phase != monitor.LiveFollowing && update.Phase != monitor.LiveEnded {
		if update.Interval != nil && *update.Interval > 0 {
			schedule := "every " + formatSpan(*update.Interval)
			if update.Until != nil {
				schedule += " until " + liveClock(*update.Until, update.Now)
			}
			parts = append(parts, rankedPart{schedule, 3})
		}
		if update.SkippedSlots > 0 {
			parts = append(parts, rankedPart{fmt.Sprintf("%d started late", update.SkippedSlots), 4})
		}
		if update.NextIn != nil {
			parts = append(parts, rankedPart{fmt.Sprintf("next round in %ds", int(math.Ceil(update.NextIn.Seconds()))), 6})
		}
	}
	return cropCells(fitParts(parts, width), width)
}

type rankedPart struct {
	text string
	rank int
}

func fitParts(parts []rankedPart, width int) string {
	if len(parts) == 0 {
		return ""
	}
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return parts[order[i]].rank > parts[order[j]].rank })
	if displayWidth(terminalText(parts[order[0]].text)) > width {
		return cropCells(terminalText(parts[order[0]].text), width)
	}
	kept := make(map[int]bool)
	for _, candidate := range order {
		trial := make([]string, 0, len(kept)+1)
		for i, part := range parts {
			if kept[i] || i == candidate {
				trial = append(trial, terminalText(part.text))
			}
		}
		if displayWidth(strings.Join(trial, " · ")) <= width {
			kept[candidate] = true
		}
	}
	var values []string
	for i, part := range parts {
		if kept[i] {
			values = append(values, terminalText(part.text))
		}
	}
	return strings.Join(values, " · ")
}

func liveCounters(update monitor.LiveUpdate, width int) string {
	counts := make(map[byte]int)
	for _, cell := range update.Data.Cells {
		counts[cell]++
	}
	var parts []string
	for _, entry := range []struct {
		cell byte
		word string
	}{{'!', "replying"}, {'R', "rejecting"}, {'T', "TCP only"}, {'U', "unreachable"}, {'x', "gone"}} {
		if counts[entry.cell] > 0 {
			parts = append(parts, fmt.Sprintf("%s %s", groupedCount(counts[entry.cell]), entry.word))
		}
	}
	parts = append(parts, fmt.Sprintf("%s seen of %s", groupedCount(update.Data.Seen), groupedCount(len(update.Request.Hosts))))
	return cropCells(terminalText(strings.Join(parts, " · ")), width)
}

func liveLegend(cells []byte, width int) []line {
	present := make(map[byte]bool)
	for _, cell := range cells {
		if cell != monitor.Unprobed {
			present[cell] = true
		}
	}
	entries := []struct {
		cell  byte
		text  string
		style string
	}{{'!', "ICMP reply", "success"}, {'R', "rejected (up, firewalled)", "info"}, {'T', "TCP response (open or closed)", "info"}, {'U', "unreachable (reported since first probed)", "error"}, {'x', "gone", "warning"}, {'.', "silent", "dim"}, {'E', "error", "error"}, {'?', "name did not resolve", "error"}}
	var out []line
	current := line{}
	used := 0
	for _, entry := range entries {
		if !present[entry.cell] {
			continue
		}
		piece := string(entry.cell) + " " + entry.text
		pieceWidth := displayWidth(piece)
		gap := 2
		if used == 0 {
			gap = 0
		}
		if used+gap+pieceWidth > width && used > 0 {
			out = append(out, current)
			current = line{}
			used = 0
			gap = 0
		}
		if gap > 0 {
			current = append(current, segment{text: "  "})
			used += gap
		}
		current = append(current, segment{text: string(entry.cell), style: entry.style}, segment{text: " " + entry.text})
		used += pieceWidth
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

func (layout *liveLayout) grid(update monitor.LiveUpdate, width, height int) []line {
	chars := update.Data.Cells
	if len(chars) != len(layout.request.Hosts) {
		chars = make([]byte, len(layout.request.Hosts))
		for i := range chars {
			chars[i] = monitor.Unprobed
		}
	}
	changed, partial := make(map[int]bool), make(map[int]bool)
	for _, index := range update.Data.Changed {
		changed[index] = true
	}
	for _, index := range update.Data.Partial {
		partial[index] = true
	}
	size := gridCellSize(layout.blocks, width, height)
	if size == 0 {
		return compactGrid(layout.blocks, chars, height, width)
	}
	var lines []line
	labelWidth := gridLabelWidth(layout.blocks, size)
	for bi, block := range layout.blocks {
		if bi > 0 {
			lines = append(lines, nil)
		}
		if block.prefix.IsValid() {
			rowCells := min(uint64(size), block.count)
			lines = append(lines, gridHeader(block, rowCells, labelWidth))
			rowCount := ceilDiv(block.count, uint64(size))
			for row := uint64(0); row < rowCount; row++ {
				label := gridRowLabel(block.prefix, row*uint64(size))
				rowLine := line{{text: label + strings.Repeat(" ", max(0, labelWidth-displayWidth(label))) + " ", style: "description"}}
				remaining := min(uint64(size), block.count-row*uint64(size))
				for i := uint64(0); i < remaining; i++ {
					if i > 0 && i%8 == 0 {
						rowLine = append(rowLine, segment{text: " "})
					}
					index, exists := layout.index[addressAt(block.prefix, row*uint64(size)+i).String()]
					if !exists {
						rowLine = append(rowLine, segment{text: " ", style: "dim"})
						continue
					}
					char := chars[index]
					style := cellStyle(char, partial[index])
					if changed[index] {
						style += " reverse"
					}
					rowLine = append(rowLine, segment{text: string(char), style: style})
				}
				lines = append(lines, clipLine(rowLine, width))
			}
			continue
		}
		lines = append(lines, line{{text: "Hosts", style: "description"}})
		lines = append(lines, hostsBlockLines(block, chars, changed, partial, width)...)
	}
	return cropBody(lines, height, width)
}

func cellStyle(cell byte, partial bool) string {
	if cell == '!' {
		if partial {
			return "warning"
		}
		return "success"
	}
	switch cell {
	case 'R', 'T':
		return "info"
	case 'U', 'E', '?':
		return "error"
	case 'x':
		return "warning"
	case '.':
		return "dim"
	default:
		return "dim"
	}
}

func gridCellSize(blocks []liveBlock, width, height int) int {
	for _, size := range []int{64, 32, 16} {
		if gridLinesCount(blocks, size, width) > uint64(max(height, 1)) {
			continue
		}
		labels := gridLabelWidth(blocks, size)
		if labels+1+size+size/8-1 <= width {
			return size
		}
	}
	return 0
}

func gridLinesCount(blocks []liveBlock, size, width int) uint64 {
	var count uint64
	for _, block := range blocks {
		if block.prefix.IsValid() {
			count += 1 + ceilDiv(block.count, uint64(size))
		} else if len(block.names) > 0 {
			count += 1 + uint64(hostsWrappedLineCount(block, width))
		}
	}
	if len(blocks) > 1 {
		count += uint64(len(blocks) - 1)
	}
	return count
}

func hostsWrappedLineCount(block liveBlock, width int) int {
	const indent = 6
	lines, used := 0, indent
	for _, host := range block.names {
		name := cropCells(terminalText(host.text), max(1, width-indent-2))
		pairWidth := displayWidth(name) + 2
		if used > indent && used+3+pairWidth > width {
			lines++
			used = indent
		}
		if used > indent {
			used += 3
		}
		used += pairWidth
	}
	if used > indent {
		lines++
	}
	return lines
}

func gridLabelWidth(blocks []liveBlock, size int) int {
	width := 0
	for _, block := range blocks {
		if block.prefix.IsValid() {
			rows := ceilDiv(block.count, uint64(size))
			if rows <= 10000 {
				for row := uint64(0); row < rows; row++ {
					width = max(width, displayWidth(gridRowLabel(block.prefix, row*uint64(size))))
				}
			} else if block.prefix.Addr().Is4() {
				width = max(width, len("  255.255.255.255"))
			} else {
				width = max(width, len("  ")+39)
			}
		} else {
			width = max(width, displayWidth("Hosts"))
		}
	}
	return width
}

func gridRowLabel(prefix netip.Prefix, offset uint64) string {
	address := addressAt(prefix, offset)
	if address.Is4() {
		value := address.As4()
		if prefix.Bits() >= 24 {
			return fmt.Sprintf("  .%d", value[3])
		}
		if prefix.Bits() >= 16 {
			return fmt.Sprintf("  .%d.%d", value[2], value[3])
		}
	}
	return "  " + address.String()
}

func gridHeader(block liveBlock, rowCells uint64, labelWidth int) line {
	result := line{{text: block.title, style: "description"}}
	column := displayWidth(block.title)
	for group := uint64(0); group < ceilDiv(rowCells, 8); group++ {
		at := labelWidth + 1 + int(group*9)
		if at <= displayWidth(block.title) {
			continue
		}
		offset := fmt.Sprint(group * 8)
		result = append(result, segment{text: strings.Repeat(" ", max(1, at-column)), style: "text"}, segment{text: offset, style: "header"})
		column = at + displayWidth(offset)
	}
	return result
}

func hostsBlockLines(block liveBlock, chars []byte, changed, partial map[int]bool, width int) []line {
	var result []line
	indent := 6
	current := line{{text: "      "}}
	used := indent
	for _, host := range block.names {
		name := terminalText(host.text)
		name = cropCells(name, max(1, width-indent-2))
		pairWidth := displayWidth(name) + 2
		if used > indent && used+3+pairWidth > width {
			result = append(result, current)
			current = line{{text: strings.Repeat(" ", indent)}}
			used = indent
		}
		if used > indent {
			current = append(current, segment{text: "   "})
			used += 3
		}
		current = append(current, segment{text: name, style: "description"}, segment{text: " "})
		index := host.index
		style := cellStyle(chars[index], partial[index])
		if changed[index] {
			style += " reverse"
		}
		current = append(current, segment{text: string(chars[index]), style: style})
		used += pairWidth
	}
	if len(current) > 1 {
		result = append(result, clipLine(current, width))
	}
	return result
}

func compactGrid(blocks []liveBlock, chars []byte, room, width int) []line {
	var lines []line
	for _, block := range blocks {
		counts := make(map[byte]int)
		for _, index := range block.members {
			if index < len(chars) && chars[index] != monitor.Unprobed {
				counts[chars[index]]++
			}
		}
		line := line{{text: block.title, style: "description"}, {text: ": "}}
		first := true
		for _, cell := range []byte("!RTxUE?.") {
			if counts[cell] == 0 {
				continue
			}
			if !first {
				line = append(line, segment{text: " · "})
			}
			line = append(line, segment{text: groupedCount(counts[cell]) + " "}, segment{text: string(cell), style: cellStyle(cell, false)})
			first = false
		}
		lines = append(lines, line)
	}
	return cropBody(lines, room, width)
}

func liveTable(update monitor.LiveUpdate, width, room int) []line {
	rows := update.Data.Rows
	if len(rows) == 0 {
		return []line{clipLine(line{{text: "No host seen yet.", style: "dim"}}, width)}
	}
	maxHost := 8
	for _, row := range rows {
		maxHost = max(maxHost, displayWidth(terminalText(row.Host)))
	}
	hostWidth := min(maxHost, max(width-53, 8))
	hostHeader := cropCells("Host", hostWidth)
	head := line{
		{text: hostHeader + strings.Repeat(" ", max(0, hostWidth-displayWidth(hostHeader))) + "  ", style: "header"},
		{text: "State  ", style: "header"},
		{text: "ICMP" + strings.Repeat(" ", 12) + "  ", style: "header"},
		{text: "Last seen" + strings.Repeat(" ", 16) + "  Seen", style: "header"},
	}
	lines := []line{clipLine(head, width)}
	if room < 2 {
		return []line{clipLine(line{{text: fmt.Sprintf("(+%d more)", len(rows)), style: "dim"}}, width)}
	}
	limit := max(room-1, 1)
	if len(rows) > room-1 {
		limit = max(room-2, 0)
	}
	partial := make(map[int]bool, len(update.Data.Partial))
	for _, index := range update.Data.Partial {
		partial[index] = true
	}
	for i, row := range rows {
		if i >= limit {
			break
		}
		last := ""
		if !row.LastSeen.IsZero() {
			last = formatTimestamp(row.LastSeen)
		}
		host := cropCells(terminalText(row.Host), hostWidth)
		latest := terminalText(row.Latest)
		seen := fmt.Sprintf("%d of %d", row.SeenRounds, row.Rounds)
		style := cellStyle(row.Cell, partial[row.Index])
		lines = append(lines, clipLine(line{
			{text: host + strings.Repeat(" ", max(0, hostWidth-displayWidth(host))), style: "description"},
			{text: "  "}, {text: string(row.Cell), style: style}, {text: "      "},
			{text: latest + strings.Repeat(" ", max(0, 16-displayWidth(latest))), style: style}, {text: "  "},
			{text: last + strings.Repeat(" ", max(0, 25-displayWidth(last))), style: style}, {text: "  " + seen, style: style},
		}, width))
	}
	if len(rows) > limit {
		lines = append(lines, clipLine(line{{text: fmt.Sprintf("(+%d more)", len(rows)-limit), style: "dim"}}, width))
	}
	return lines
}

func clipLine(value line, width int) line {
	var out line
	used := 0
	for _, part := range value {
		text := terminalText(part.text)
		remaining := width - used
		if remaining <= 0 {
			break
		}
		if displayWidth(text) > remaining {
			out = append(out, segment{text: cropCells(text, remaining), style: part.style})
			return out
		}
		out = append(out, segment{text: text, style: part.style})
		used += displayWidth(text)
	}
	return out
}

func cropBody(lines []line, room, width int) []line {
	if room < 1 {
		return nil
	}
	if len(lines) <= room {
		for i := range lines {
			lines[i] = clipLine(lines[i], width)
		}
		return lines
	}
	if room == 1 {
		return []line{clipLine(line{{text: fmt.Sprintf("(+%d more)", len(lines)), style: "dim"}}, width)}
	}
	kept := append([]line(nil), lines[:room-1]...)
	kept = append(kept, clipLine(line{{text: fmt.Sprintf("(+%d more)", len(lines)-(room-1)), style: "dim"}}, width))
	for i := range kept {
		kept[i] = clipLine(kept[i], width)
	}
	return kept
}

func cropFrame(lines []line, width, height int) []line {
	for i := range lines {
		lines[i] = clipLine(lines[i], width)
	}
	if len(lines) <= height {
		return lines
	}
	if height == 1 {
		return lines[:1]
	}
	footer := lines[len(lines)-1]
	if height == 2 {
		return []line{lines[0], footer}
	}
	kept := append([]line(nil), lines[:height-2]...)
	kept = append(kept, clipLine(line{{text: fmt.Sprintf("(+%d more)", len(lines)-(height-2)-1), style: "dim"}}, width), footer)
	return kept
}

func ceilDiv(value, by uint64) uint64 {
	if value == 0 {
		return 0
	}
	return 1 + (value-1)/by
}

func groupedCount(value int) string {
	text := strconv.Itoa(value)
	if len(text) <= 3 {
		return text
	}
	start := len(text) % 3
	if start == 0 {
		start = 3
	}
	var result strings.Builder
	result.WriteString(text[:start])
	for i := start; i < len(text); i += 3 {
		result.WriteByte(',')
		result.WriteString(text[i : i+3])
	}
	return result.String()
}

func liveClock(value, now time.Time) string {
	value, now = value.UTC(), now.UTC()
	if !now.IsZero() && !sameUTCDay(value, now) {
		return value.Format("2006-01-02 15:04:05")
	}
	return value.Format("15:04:05")
}
