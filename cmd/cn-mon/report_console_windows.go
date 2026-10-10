//go:build windows

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

const enableVirtualTerminalProcessing = 0x0004

func withReportConsoleVT(writer io.Writer, enabled bool, addEmergencyRestore func(func()), write func() error) error {
	file, ok := writer.(*os.File)
	if !enabled || !ok {
		return write()
	}
	handle := windows.Handle(file.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil || original&enableVirtualTerminalProcessing != 0 {
		return write()
	}
	restore := func() { _ = windows.SetConsoleMode(handle, original) }
	if addEmergencyRestore != nil {
		addEmergencyRestore(restore)
	}
	if err := windows.SetConsoleMode(handle, original|enableVirtualTerminalProcessing); err != nil {
		return write()
	}
	defer restore()
	return write()
}
