// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package output

import (
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
