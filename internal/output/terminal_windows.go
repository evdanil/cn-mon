// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package output

import (
	"errors"
	"io"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

const (
	windowsEnableProcessedOutput    = 0x0001
	windowsEnableVT                 = 0x0004
	windowsDisableNewlineAutoReturn = 0x0008
)

const (
	windowsKeyEvent       = 0x0001
	windowsAltPressed     = 0x0001 | 0x0002
	windowsReadPollMillis = 100
)

type windowsKeyEventRecord struct {
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	UnicodeChar     uint16
	ControlKeyState uint32
}

type windowsInputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

func (r *windowsInputRecord) keyEvent() windowsKeyEventRecord {
	return *(*windowsKeyEventRecord)(unsafe.Pointer(&r.Event[0]))
}

var (
	windowsKernel32      = windows.NewLazySystemDLL("kernel32.dll")
	procPeekConsoleInput = windowsKernel32.NewProc("PeekConsoleInputW")
	procReadConsoleInput = windowsKernel32.NewProc("ReadConsoleInputW")
	procGetCursorInfo    = windowsKernel32.NewProc("GetConsoleCursorInfo")
	procSetCursorInfo    = windowsKernel32.NewProc("SetConsoleCursorInfo")
)

type windowsCursorInfo struct {
	Size    uint32
	Visible int32
}

type terminalPlatform struct {
	inHandle        windows.Handle
	outHandle       windows.Handle
	inputTTY        bool
	outputTTY       bool
	inputChanged    bool
	outputChanged   bool
	inputModeSaved  bool
	outputModeSaved bool
	inputMode       uint32
	outputMode      uint32
	originalCursor  *windowsCursorInfo
}

func (s *Screen) initPlatform() {
	p := &terminalPlatform{inHandle: windows.InvalidHandle, outHandle: windows.InvalidHandle}
	if s.in != nil {
		p.inHandle = windows.Handle(s.in.Fd())
		p.inputTTY = term.IsTerminal(int(p.inHandle))
	}
	if s.out != nil {
		p.outHandle = windows.Handle(s.out.Fd())
		p.outputTTY = term.IsTerminal(int(p.outHandle))
	}
	// Capture all cursor metadata before changing either console mode. If the
	// cursor cannot be queried, output drawing stays disabled so the VT hide
	// sequence cannot leave the user's cursor in an unrestorable state.
	if p.outputTTY {
		if cursor, err := windowsGetConsoleCursorInfo(p.outHandle); err == nil {
			p.originalCursor = &cursor
		}
	}
	if p.inputTTY {
		if err := windows.GetConsoleMode(p.inHandle, &p.inputMode); err == nil {
			p.inputModeSaved = true
			mode := windowsInputReadMode(p.inputMode)
			if err = windows.SetConsoleMode(p.inHandle, mode); err == nil {
				p.inputChanged = true
			}
		}
	}
	if p.outputTTY && p.originalCursor != nil {
		if err := windows.GetConsoleMode(p.outHandle, &p.outputMode); err == nil {
			p.outputModeSaved = true
			if err = windows.SetConsoleMode(p.outHandle, windowsOutputMode(p.outputMode)); err == nil {
				p.outputChanged = true
			} else {
				p.outputTTY = false
			}
		}
	}
	s.platform = p
}

func windowsInputReadMode(original uint32) uint32 {
	return original&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) | windows.ENABLE_PROCESSED_INPUT
}

func windowsOutputMode(original uint32) uint32 {
	return original | windowsEnableProcessedOutput | windowsEnableVT | windowsDisableNewlineAutoReturn
}

func (s *Screen) platformInputEnabled() bool {
	p := s.platform.(*terminalPlatform)
	return p.inputTTY && p.inputChanged
}

func (s *Screen) platformEnabled() bool {
	p := s.platform.(*terminalPlatform)
	return p.outputTTY && p.outputChanged && p.originalCursor != nil
}

func (s *Screen) platformLineEnding() string {
	if s.platform.(*terminalPlatform).outputChanged {
		return "\r\n"
	}
	return "\n"
}

func (s *Screen) platformLost() bool {
	p := s.platform.(*terminalPlatform)
	var mode uint32
	return p.outHandle == windows.InvalidHandle || windows.GetConsoleMode(p.outHandle, &mode) != nil
}

func (s *Screen) platformSize() (int, int, error) {
	p := s.platform.(*terminalPlatform)
	if !p.outputTTY {
		return 0, 0, errors.New("terminal output unavailable")
	}
	return term.GetSize(int(p.outHandle))
}

func (s *Screen) readPlatformKeys() {
	p := s.platform.(*terminalPlatform)
	for {
		select {
		case <-s.done:
			return
		default:
		}
		state, err := windows.WaitForSingleObject(p.inHandle, windowsReadPollMillis)
		if err != nil {
			return
		}
		if state != windows.WAIT_OBJECT_0 {
			continue
		}
		records := make([]windowsInputRecord, 32)
		count, err := windowsConsoleRecords(procPeekConsoleInput, p.inHandle, records)
		if err != nil {
			return
		}
		if count == 0 {
			continue
		}
		count, err = windowsConsoleRecords(procReadConsoleInput, p.inHandle, records[:count])
		if err != nil {
			return
		}
		for index := uint32(0); index < count; index++ {
			record := &records[index]
			if record.EventType != windowsKeyEvent {
				continue
			}
			key := record.keyEvent()
			if key.KeyDown == 0 || key.UnicodeChar == 0 || key.ControlKeyState&windowsAltPressed != 0 {
				continue
			}
			s.dispatchKey(rune(key.UnicodeChar))
		}
	}
}

func windowsConsoleRecords(proc *windows.LazyProc, handle windows.Handle, records []windowsInputRecord) (uint32, error) {
	if len(records) == 0 {
		return 0, nil
	}
	var count uint32
	result, _, callErr := proc.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(len(records)),
		uintptr(unsafe.Pointer(&count)),
	)
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return 0, callErr
	}
	return count, nil
}

func (s *Screen) platformClose() {
	p := s.platform.(*terminalPlatform)
	if p.inputChanged {
		_ = windows.SetConsoleMode(p.inHandle, p.inputMode)
		p.inputChanged = false
	}
	if p.outputChanged {
		_ = windows.SetConsoleMode(p.outHandle, p.outputMode)
		p.outputChanged = false
	}
}

func (s *Screen) platformEmergency() {
	p := s.platform.(*terminalPlatform)
	if p.inputModeSaved {
		_ = windows.SetConsoleMode(p.inHandle, p.inputMode)
	}
	if p.outputModeSaved {
		_ = windows.SetConsoleMode(p.outHandle, p.outputMode)
	}
	s.platformShowCursor()
}

func (s *Screen) platformShowCursor() {
	p := s.platform.(*terminalPlatform)
	original := p.originalCursor
	if original == nil || p.outHandle == windows.InvalidHandle {
		return
	}
	cursor := *original
	_, _, _ = procSetCursorInfo.Call(uintptr(p.outHandle), uintptr(unsafe.Pointer(&cursor)))
}

func windowsGetConsoleCursorInfo(handle windows.Handle) (windowsCursorInfo, error) {
	var cursor windowsCursorInfo
	result, _, callErr := procGetCursorInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&cursor)))
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return windowsCursorInfo{}, callErr
	}
	return cursor, nil
}

// Windows console writes do not use Unix terminal job-control semantics.
func DiagnosticWriteAllowed(io.Writer) bool { return true }
