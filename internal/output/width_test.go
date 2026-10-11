// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTerminalTextReplacesControlsAndKeepsUnicode(t *testing.T) {
	got := terminalText("雪\x00\x1b[31m\x7f\u0085x")
	if strings.ContainsAny(got, "\x00\x1b\x7f\u0085") || !strings.Contains(got, "雪") || !strings.Contains(got, "�") {
		t.Fatalf("terminalText = %q", got)
	}
}

func TestCropCellsNeverSplitsGraphemeOrExceedsWidth(t *testing.T) {
	for _, input := range []string{"a雪b", "e\u0301xy", "👩‍💻xy"} {
		got := cropCells(input, 2)
		if displayWidth(got) > 2 || !strings.HasSuffix(got, "…") {
			t.Errorf("cropCells(%q) = %q width %d", input, got, displayWidth(got))
		}
		if strings.HasPrefix(got, "e") && !strings.HasPrefix(got, "e\u0301") {
			t.Errorf("crop split combining grapheme: %q", got)
		}
	}
}

func TestStyledWriterOnlyDecoratesWhenRequested(t *testing.T) {
	lines := []line{{{text: "safe", style: "header"}, {text: "\x1braw"}, {text: "!", style: "warning reverse"}, {text: ".", style: "dim reverse"}}}
	var redirected, terminal bytes.Buffer
	if err := writeStyled(&redirected, lines, "default", false, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redirected.String(), "\x1b") {
		t.Fatalf("redirected text contains ANSI: %q", redirected.String())
	}
	if err := writeStyled(&terminal, lines, "pastel", true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.String(), "\x1b[1m") || !strings.Contains(terminal.String(), "\x1b[7m") || !strings.Contains(terminal.String(), "\x1b[2m") || strings.Contains(terminal.String(), "38;2") {
		t.Fatalf("no-color terminal style = %q", terminal.String())
	}
	var colored bytes.Buffer
	if err := writeStyled(&colored, lines, "pastel", true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(colored.String(), "38;2;250;250;210m") || !strings.Contains(colored.String(), "\x1b[7m") {
		t.Fatalf("colored composite style = %q", colored.String())
	}
}

func TestColumnPaletteRestartsPerSectionAndUsesSourceRoles(t *testing.T) {
	want := []string{"header", "sn", "pid", "date", "code", "license_full", "reserved", "auth_type", "start_date", "term_count", "tag", "feature"}
	if len(tableRoles) < len(want) {
		t.Fatalf("only %d source column roles", len(tableRoles))
	}
	for i := range want {
		if got := tableRole(i); got != want[i] {
			t.Errorf("column %d role = %q, want %q", i, got, want[i])
		}
	}
}

func TestThemePaletteMatchesPinnedSourceValues(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "theme-source-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Roles  []string
		Themes map[string]struct {
			Title string
			Roles []string
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Themes) != 4 || len(fixture.Roles) != len(tableRoles) {
		t.Fatalf("theme source fixture has %d themes and %d roles", len(fixture.Themes), len(fixture.Roles))
	}
	for name, theme := range fixture.Themes {
		palette, ok := palettes[name]
		if !ok || len(theme.Roles) != len(tableRoles) {
			t.Fatalf("missing Go palette %q or source roles", name)
		}
		for i, role := range fixture.Roles {
			if role != tableRoles[i] {
				t.Fatalf("source role %d = %q, Go role = %q", i, role, tableRoles[i])
			}
			if got, want := paletteRGB(palette[role]), parseSourceRGB(theme.Roles[i]); got != want {
				t.Errorf("%s %s = %#v, source %q", name, role, got, theme.Roles[i])
			}
		}
		if got, want := paletteRGB(palette["title"]), parseSourceRGB(theme.Title); got != want {
			t.Errorf("%s title = %#v, source %q", name, got, theme.Title)
		}
	}
}

func parseSourceRGB(style string) rgb {
	fields := strings.Fields(style)
	color := strings.TrimPrefix(fields[len(fields)-1], "#")
	value, err := strconv.ParseUint(color, 16, 32)
	if err != nil {
		panic(err)
	}
	return rgb{r: uint8(value >> 16), g: uint8(value >> 8), b: uint8(value)}
}

func paletteRGB(value rgb) rgb { return value }
