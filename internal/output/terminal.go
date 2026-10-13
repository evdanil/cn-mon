// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/evdanil/cn-mon/internal/monitor"
)

func writeFrameDiff(w io.Writer, previous, next [][]byte, rowWidths []int, terminalWidth int) error {
	var frame bytes.Buffer
	if len(previous) == len(next) && len(previous) > 0 {
		for index, row := range next {
			if bytes.Equal(previous[index], row) {
				continue
			}
			if index < len(next)-1 {
				fmt.Fprintf(&frame, "\x1b[%dA", len(next)-1-index)
			}
			frame.WriteByte('\r')
			frame.Write(row)
			frame.WriteString("\x1b[0m")
			if rowWidths[index] < terminalWidth {
				frame.WriteString("\x1b[K")
			}
			if index < len(next)-1 {
				frame.WriteByte('\r')
				fmt.Fprintf(&frame, "\x1b[%dB", len(next)-1-index)
			}
			frame.WriteByte('\r')
		}
	} else {
		frame.WriteByte('\r')
		for index, row := range next {
			if index > 0 {
				frame.WriteString("\r\n")
			}
			frame.Write(row)
		}
		frame.WriteByte('\r')
	}
	if frame.Len() == 0 {
		return nil
	}
	data := frame.Bytes()
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

type KeyHandlers struct {
	Quit      func()
	Interrupt func()
}

type presentedFrame struct {
	width, height int
	table         bool
	layout        *liveLayout
	rows          [][]byte
	generation    uint64
}

const (
	screenEligibilityUnknown uint32 = iota
	screenEligibilityDisabled
	screenEligibilityEnabled
)

// Screen is a thin terminal adapter. It serializes frames and permanent notes;
// it never owns or mutates monitor state.
type Screen struct {
	in, out     *os.File
	theme       string
	noColor     bool
	keys        KeyHandlers
	mu          sync.Mutex
	done        chan struct{}
	keyDone     chan struct{}
	closed      bool
	active      bool
	closedFlag  atomic.Bool
	activeFlag  atomic.Bool
	failed      atomic.Bool
	table       atomic.Bool
	eligibility atomic.Uint32
	frameLost   atomic.Bool
	last        *monitor.LiveUpdate
	layout      *liveLayout
	frameRows   int
	notes       []line
	frame       *presentedFrame
	frameDirty  atomic.Uint64

	platform any
}

func OpenScreen(in, errOut *os.File, theme string, noColor bool, keys KeyHandlers) *Screen {
	s := &Screen{
		in: in, out: errOut, theme: theme, noColor: noColor, keys: keys,
		done: make(chan struct{}), keyDone: make(chan struct{}),
	}
	s.initPlatform()
	if s.platformInputEnabled() {
		go func() {
			defer close(s.keyDone)
			s.readPlatformKeys()
		}()
	} else {
		close(s.keyDone)
	}
	return s
}

func (s *Screen) Enabled() bool {
	return s != nil && !s.closedFlag.Load() && !s.failed.Load() && s.observePlatformEligibility()
}

func (s *Screen) CanWriteNote() bool {
	return s != nil && s.out != nil && !s.closedFlag.Load()
}

func (s *Screen) observePlatformEligibility() bool {
	enabled := s.platformEnabled()
	state := screenEligibilityDisabled
	if enabled {
		state = screenEligibilityEnabled
	}
	previous := s.eligibility.Swap(state)
	if previous != state {
		s.invalidateFrame()
		if !enabled && s.activeFlag.Load() {
			s.frameLost.Store(true)
		}
		if enabled && previous == screenEligibilityDisabled {
			s.frameLost.Store(true)
		}
	}
	return enabled
}

func (s *Screen) abandonFrameLocked() {
	if s.activeFlag.Load() {
		s.platformShowCursor()
	}
	s.active = false
	s.activeFlag.Store(false)
	s.frameRows = 0
	s.frame = nil
	s.invalidateFrame()
}

func (s *Screen) resumeFrameOwnershipLocked() {
	if s.frameLost.Swap(false) {
		s.abandonFrameLocked()
	}
}

func (s *Screen) Draw(update monitor.LiveUpdate) {
	if s == nil || s.out == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed.Load() {
		return
	}
	if !s.observePlatformEligibility() {
		if s.active && s.platformLost() {
			s.failed.Store(true)
			s.active = false
			s.activeFlag.Store(false)
			s.frameRows = 0
			s.frame = nil
			s.invalidateFrame()
		} else if s.active {
			s.abandonFrameLocked()
		}
		return
	}
	s.resumeFrameOwnershipLocked()
	copyUpdate := cloneLiveUpdate(update)
	s.last = &copyUpdate
	if err := s.drawLocked(copyUpdate); err != nil {
		s.failed.Store(true)
		s.frame = nil
		s.frameRows = 0
		s.invalidateFrame()
	}
}

func (s *Screen) Change(change monitor.Change) {
	if s == nil || s.out == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	enabled := s.observePlatformEligibility()
	if s.active && !enabled {
		if s.platformLost() {
			s.failed.Store(true)
			s.active = false
			s.activeFlag.Store(false)
			s.frameRows = 0
			s.frame = nil
			s.invalidateFrame()
		} else {
			s.abandonFrameLocked()
		}
		return
	}
	if !enabled {
		s.abandonFrameLocked()
		return
	}
	s.resumeFrameOwnershipLocked()
	changeLineValue := changeLine(change)
	if s.failed.Load() {
		return
	}
	s.invalidateFrame()
	if err := s.removeFrameLocked(); err != nil {
		s.failed.Store(true)
		s.frame = nil
		s.frameRows = 0
		s.invalidateFrame()
		return
	}
	if err := s.writeScreenStyledLine([]line{changeLineValue}); err != nil {
		s.failed.Store(true)
		s.frame = nil
		s.frameRows = 0
		s.invalidateFrame()
		return
	}
	s.notes = appendBoundedLines(s.notes, changeLineValue)
	if s.last != nil {
		if err := s.drawLocked(*s.last); err != nil {
			s.failed.Store(true)
			s.frame = nil
			s.frameRows = 0
			s.invalidateFrame()
		}
	}
}

func (s *Screen) Note(message string) {
	if s == nil || s.out == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	enabled := s.observePlatformEligibility()
	if !DiagnosticWriteAllowed(s.out) {
		return
	}
	if s.active && !enabled {
		if s.platformLost() {
			s.failed.Store(true)
			s.active = false
			s.activeFlag.Store(false)
			s.frameRows = 0
			s.frame = nil
			s.invalidateFrame()
		} else {
			s.abandonFrameLocked()
		}
	}
	if enabled {
		s.resumeFrameOwnershipLocked()
	}
	lineText := terminalText(strings.TrimSuffix(message, "\n"))
	if enabled && !s.failed.Load() {
		s.invalidateFrame()
		if err := s.removeFrameLocked(); err != nil {
			s.failed.Store(true)
			s.frame = nil
			s.frameRows = 0
			s.invalidateFrame()
			return
		}
		if _, err := io.WriteString(s.out, lineText+s.platformLineEnding()); err != nil {
			s.failed.Store(true)
			s.frame = nil
			s.frameRows = 0
			s.invalidateFrame()
			return
		}
		s.notes = appendBoundedLines(s.notes, line{{text: lineText, style: "text"}})
		if s.last != nil {
			if err := s.drawLocked(*s.last); err != nil {
				s.failed.Store(true)
				s.frame = nil
				s.frameRows = 0
				s.invalidateFrame()
			}
		}
		return
	}
	s.invalidateFrame()
	if s.active {
		s.abandonFrameLocked()
	}
	if _, err := io.WriteString(s.out, lineText+s.platformLineEnding()); err != nil {
		s.failed.Store(true)
		s.frame = nil
		s.frameRows = 0
		s.invalidateFrame()
	}
}

func (s *Screen) writeScreenStyledLine(lines []line) error {
	return writeScreenStyledLineTo(s.out, s.platformLineEnding(), lines, s.theme, !s.noColor)
}

func writeScreenStyledLineTo(w io.Writer, ending string, lines []line, theme string, color bool) error {
	if ending == "\n" {
		return writeStyled(w, lines, theme, true, color)
	}
	var encoded bytes.Buffer
	if err := writeStyled(&encoded, lines, theme, true, color); err != nil {
		return err
	}
	data := bytes.ReplaceAll(encoded.Bytes(), []byte("\n"), []byte("\r\n"))
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (s *Screen) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closedFlag.Store(true)
	s.frame = nil
	s.frameRows = 0
	s.invalidateFrame()
	close(s.done)
	if s.active && s.out != nil {
		s.platformShowCursor()
		s.active = false
		s.activeFlag.Store(false)
		s.frameRows = 0
	}
	s.mu.Unlock()
	<-s.keyDone
	s.platformClose()
}

// EmergencyRestore is deliberately independent of the draw/note mutex so a
// hard-exit control path can restore terminal state while output is blocked.
func (s *Screen) EmergencyRestore() {
	if s != nil {
		s.platformEmergency()
	}
}

func (s *Screen) drawLocked(update monitor.LiveUpdate) error {
	width, height, err := s.platformSize()
	if err != nil {
		return err
	}
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	if s.layout == nil || !s.layout.matches(update.Request) {
		s.layout = newLiveLayout(update.Request)
	}
	table := s.table.Load()
	lines := s.layout.frame(update, width, height, table)
	rowWidths := liveLineWidths(lines)
	var encoded bytes.Buffer
	if err := writeStyledFrame(&encoded, lines, s.theme, true, !s.noColor); err != nil {
		return err
	}
	rows := bytes.Split(encoded.Bytes(), []byte("\n"))
	generation := s.frameDirty.Load()
	cacheValid := s.active && s.frame != nil && s.frame.generation == generation &&
		s.frame.width == width && s.frame.height == height && s.frame.table == table &&
		s.frame.layout == s.layout && len(s.frame.rows) == len(rows)
	if !s.active {
		if _, err := s.out.WriteString("\x1b[?25l"); err != nil {
			return err
		}
		s.active = true
		s.activeFlag.Store(true)
	}
	if cacheValid {
		if equalFrameRows(s.frame.rows, rows) {
			return nil
		}
		return s.writeAndRememberFrame(s.out, s.frame.rows, rows, rowWidths, width, height, table, s.layout, generation)
	}
	if err := s.removeFrameLocked(); err != nil {
		return err
	}
	return s.writeAndRememberFrame(s.out, nil, rows, rowWidths, width, height, table, s.layout, generation)
}

func (s *Screen) invalidateFrame() { s.frameDirty.Add(1) }

func (s *Screen) writeAndRememberFrame(w io.Writer, previous, rows [][]byte, rowWidths []int, width, height int, table bool, layout *liveLayout, generation uint64) error {
	if err := writeFrameDiff(w, previous, rows, rowWidths, width); err != nil {
		return err
	}
	s.frameRows = len(rows)
	s.frame = &presentedFrame{width: width, height: height, table: table, layout: layout, rows: cloneFrameRows(rows), generation: generation}
	return nil
}

func liveLineWidths(lines []line) []int {
	widths := make([]int, len(lines))
	for rowIndex, current := range lines {
		for _, part := range current {
			widths[rowIndex] += displayWidth(terminalText(part.text))
		}
	}
	return widths
}

func equalFrameRows(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if !bytes.Equal(a[index], b[index]) {
			return false
		}
	}
	return true
}

func cloneFrameRows(rows [][]byte) [][]byte {
	copyRows := make([][]byte, len(rows))
	for index := range rows {
		copyRows[index] = append([]byte(nil), rows[index]...)
	}
	return copyRows
}

func (s *Screen) removeFrameLocked() error {
	if !s.active || s.frameRows <= 0 {
		return nil
	}
	if s.frameRows > 1 {
		if _, err := fmt.Fprintf(s.out, "\x1b[%dA\r\x1b[%dM", s.frameRows-1, s.frameRows); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(s.out, "\r\x1b[%dM", s.frameRows); err != nil {
		return err
	}
	s.frameRows = 0
	return nil
}

func appendBoundedLines(existing []line, value line) []line {
	const maxNotes = 3
	copyValue := append(line(nil), value...)
	existing = append(existing, copyValue)
	if len(existing) > maxNotes {
		existing = append([]line(nil), existing[len(existing)-maxNotes:]...)
	}
	return existing
}

func cloneLiveUpdate(update monitor.LiveUpdate) monitor.LiveUpdate {
	update.Request.Tokens = append([]string(nil), update.Request.Tokens...)
	update.Request.Hosts = append([]string(nil), update.Request.Hosts...)
	update.Request.Ports = append([]int(nil), update.Request.Ports...)
	update.Data.Cells = append([]byte(nil), update.Data.Cells...)
	update.Data.Changed = append([]int(nil), update.Data.Changed...)
	update.Data.Partial = append([]int(nil), update.Data.Partial...)
	update.Data.Rows = append([]monitor.LiveRow(nil), update.Data.Rows...)
	if update.Until != nil {
		value := *update.Until
		update.Until = &value
	}
	if update.Interval != nil {
		value := *update.Interval
		update.Interval = &value
	}
	if update.Source != nil {
		value := *update.Source
		update.Source = &value
	}
	if update.NextIn != nil {
		value := *update.NextIn
		update.NextIn = &value
	}
	return update
}

func (s *Screen) dispatchKey(key rune) {
	switch key {
	case 'q', 'Q':
		if s.keys.Quit != nil {
			s.keys.Quit()
		}
	case '\x03':
		if s.keys.Interrupt != nil {
			s.keys.Interrupt()
		}
	case 'v', 'V':
		s.table.Store(!s.table.Load())
	}
}
