// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

func displayWidth(text string) int { return uniseg.StringWidth(text) }

func cropCells(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	var out strings.Builder
	used := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		cluster := g.Str()
		clusterWidth := g.Width()
		if used+clusterWidth > width-1 {
			break
		}
		out.WriteString(cluster)
		used += clusterWidth
	}
	out.WriteRune('…')
	return out.String()
}

func wrapCells(text string, width int) []string {
	if width < 1 {
		return []string{""}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	var lines []string
	var line strings.Builder
	used := 0
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		cluster, clusterWidth := g.Str(), g.Width()
		if clusterWidth > width {
			if line.Len() > 0 {
				lines = append(lines, line.String())
				line.Reset()
				used = 0
			}
			lines = append(lines, cropCells(cluster, width))
			continue
		}
		if used+clusterWidth > width && line.Len() > 0 {
			lines = append(lines, line.String())
			line.Reset()
			used = 0
		}
		line.WriteString(cluster)
		used += clusterWidth
	}
	if line.Len() > 0 || len(lines) == 0 {
		lines = append(lines, line.String())
	}
	return lines
}

func terminalText(text string) string {
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	var out strings.Builder
	for _, r := range text {
		switch {
		case r == utf8.RuneError, r < 0x20, r >= 0x7f && r <= 0x9f:
			out.WriteRune('�')
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
