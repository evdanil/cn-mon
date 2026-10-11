// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"fmt"
	"strings"
)

type rgb struct{ r, g, b uint8 }

var palettes = map[string]map[string]rgb{
	"default":    {"title": {255, 105, 180}, "header": {0, 206, 209}, "sn": {50, 205, 50}, "pid": {255, 215, 0}, "date": {64, 224, 208}, "code": {60, 179, 113}, "license_full": {0, 255, 255}, "reserved": {221, 160, 221}, "auth_type": {46, 139, 87}, "start_date": {95, 158, 160}, "term_count": {186, 85, 211}, "tag": {218, 165, 32}, "feature": {72, 209, 204}, "success": {46, 139, 87}, "warning": {255, 165, 0}, "error": {178, 34, 34}, "info": {70, 130, 180}, "description": {240, 230, 140}},
	"monochrome": {"title": {255, 255, 255}, "header": {224, 224, 224}, "sn": {192, 192, 192}, "pid": {220, 220, 220}, "date": {232, 232, 232}, "code": {217, 217, 217}, "license_full": {235, 235, 235}, "reserved": {219, 219, 219}, "auth_type": {222, 222, 222}, "start_date": {240, 240, 240}, "term_count": {230, 230, 230}, "tag": {204, 204, 204}, "feature": {250, 250, 250}, "success": {234, 234, 234}, "warning": {223, 223, 223}, "error": {198, 198, 198}, "info": {237, 237, 237}, "description": {200, 200, 200}},
	"pastel":     {"title": {135, 206, 250}, "header": {152, 251, 152}, "sn": {144, 238, 144}, "pid": {240, 230, 140}, "date": {255, 182, 193}, "code": {152, 251, 152}, "license_full": {224, 255, 255}, "reserved": {216, 191, 216}, "auth_type": {144, 238, 144}, "start_date": {175, 238, 238}, "term_count": {221, 160, 221}, "tag": {240, 230, 140}, "feature": {224, 255, 255}, "success": {144, 238, 144}, "warning": {250, 250, 210}, "error": {255, 160, 122}, "info": {230, 230, 250}, "description": {250, 250, 210}},
	"dark":       {"title": {224, 255, 255}, "header": {0, 206, 209}, "sn": {50, 205, 50}, "pid": {218, 165, 32}, "date": {64, 224, 208}, "code": {46, 139, 87}, "license_full": {0, 139, 139}, "reserved": {138, 43, 226}, "auth_type": {0, 100, 0}, "start_date": {0, 128, 128}, "term_count": {153, 50, 204}, "tag": {184, 134, 11}, "feature": {32, 178, 170}, "success": {60, 179, 113}, "warning": {255, 140, 0}, "error": {220, 20, 60}, "info": {70, 130, 180}, "description": {205, 133, 63}},
}

var tableRoles = []string{"header", "sn", "pid", "date", "code", "license_full", "reserved", "auth_type", "start_date", "term_count", "tag", "feature"}

func styleSequence(style string, theme string, color bool) string {
	if strings.ContainsRune(style, ' ') {
		var sequence strings.Builder
		for _, part := range strings.Fields(style) {
			sequence.WriteString(styleSequence(part, theme, color))
		}
		return sequence.String()
	}
	if style == "" || style == "text" {
		return ""
	}
	if style == "reverse" {
		return "\x1b[7m"
	}
	if style == "dim" {
		return "\x1b[2m"
	}
	if style == "bold" {
		return "\x1b[1m"
	}
	if theme == "" {
		theme = "default"
	}
	palette, ok := palettes[theme]
	if !ok {
		palette = palettes["default"]
	}
	if style == "title" {
		return styleSequence("titleColor", theme, color) + "\x1b[1m"
	}
	if !color {
		if style == "header" || style == "titleColor" {
			return "\x1b[1m"
		}
		return ""
	}
	if style == "titleColor" {
		style = "title"
	}
	value, ok := palette[style]
	if !ok {
		return ""
	}
	sequence := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", value.r, value.g, value.b)
	if style == "header" {
		sequence += "\x1b[1m"
	}
	return sequence
}
