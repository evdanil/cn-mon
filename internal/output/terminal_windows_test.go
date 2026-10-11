// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package output

import (
	"bytes"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsInputReadModeKeepsProcessedCtrlCEnabled(t *testing.T) {
	const unrelatedMode = uint32(0x8000)
	original := unrelatedMode | windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT
	got := windowsInputReadMode(original)
	if got&windows.ENABLE_LINE_INPUT != 0 || got&windows.ENABLE_ECHO_INPUT != 0 {
		t.Fatalf("line/echo input remained enabled: %#x", got)
	}
	if got&windows.ENABLE_PROCESSED_INPUT == 0 {
		t.Fatalf("processed Ctrl+C input is disabled: %#x", got)
	}
	if got&unrelatedMode == 0 {
		t.Fatalf("unrelated console mode was lost: %#x", got)
	}
}

func TestWindowsOutputModeEnablesSafeVTWritesAndPreservesOtherFlags(t *testing.T) {
	const unrelatedMode = uint32(0x8000)
	original := unrelatedMode | windows.ENABLE_WRAP_AT_EOL_OUTPUT
	got := windowsOutputMode(original)
	for _, bit := range []uint32{windows.ENABLE_PROCESSED_OUTPUT, windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING, windows.DISABLE_NEWLINE_AUTO_RETURN, windows.ENABLE_WRAP_AT_EOL_OUTPUT, unrelatedMode} {
		if got&bit == 0 {
			t.Errorf("required/original output mode bit %#x missing from %#x", bit, got)
		}
	}
}

func TestWindowsScreenOwnedPermanentLineUsesCRLFOnlyWhenModeWasAcquired(t *testing.T) {
	if got := (&Screen{platform: &terminalPlatform{outputChanged: true}}).platformLineEnding(); got != "\r\n" {
		t.Fatalf("acquired output mode line ending = %q, want CRLF", got)
	}
	if got := (&Screen{platform: &terminalPlatform{}}).platformLineEnding(); got != "\n" {
		t.Fatalf("unacquired/redirected line ending = %q, want LF", got)
	}
	var output bytes.Buffer
	if err := writeScreenStyledLineTo(&output, "\r\n", []line{{{text: "diagnostic", style: "text"}}}, "default", true); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "diagnostic\r\n"; got != want {
		t.Fatalf("screen-owned permanent line = %q, want %q", got, want)
	}
}
