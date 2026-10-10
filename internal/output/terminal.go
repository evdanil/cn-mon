// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/evdanil/gotest/internal/monitor"
)

type KeyHandlers struct {
	Quit      func()
	Interrupt func()
}

// Screen is a thin terminal adapter. It serializes frames and permanent notes;
// it never owns or mutates monitor state.
type Screen struct {
	in, out    *os.File
	theme      string
	noColor    bool
	keys       KeyHandlers
	mu         sync.Mutex
	done       chan struct{}
	keyDone    chan struct{}
	closed     bool
	active     bool
	closedFlag atomic.Bool
	activeFlag atomic.Bool
	failed     atomic.Bool
	table      atomic.Bool
	last       *monitor.LiveUpdate
	layout     *liveLayout
	frameRows  int
	notes      []line

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
	return s != nil && !s.closedFlag.Load() && !s.failed.Load() && s.platformEnabled()
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
	if !s.platformEnabled() {
		if s.active && s.platformLost() {
			s.failed.Store(true)
			s.active = false
			s.activeFlag.Store(false)
			s.frameRows = 0
		}
		return
	}
	copyUpdate := cloneLiveUpdate(update)
	s.last = &copyUpdate
	if err := s.drawLocked(copyUpdate); err != nil {
		s.failed.Store(true)
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
	enabled := s.platformEnabled()
	if s.active && !enabled {
		if s.platformLost() {
			s.failed.Store(true)
			s.active = false
			s.activeFlag.Store(false)
			s.frameRows = 0
		}
		return
	}
	if !enabled {
		return
	}
	changeLineValue := changeLine(change)
	if s.failed.Load() {
		return
	}
	if err := s.removeFrameLocked(); err != nil {
		s.failed.Store(true)
		return
	}
	if err := writeStyled(s.out, []line{changeLineValue}, s.theme, true, !s.noColor); err != nil {
		s.failed.Store(true)
		return
	}
	s.notes = appendBoundedLines(s.notes, changeLineValue)
	if s.last != nil {
		if err := s.drawLocked(*s.last); err != nil {
			s.failed.Store(true)
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
	if !DiagnosticWriteAllowed(s.out) {
		return
	}
	enabled := s.platformEnabled()
	if s.active && !enabled {
		if s.platformLost() {
			s.failed.Store(true)
			s.active = false
			s.activeFlag.Store(false)
			s.frameRows = 0
		}
	}
	lineText := terminalText(strings.TrimSuffix(message, "\n"))
	if enabled && !s.failed.Load() {
		if err := s.removeFrameLocked(); err != nil {
			s.failed.Store(true)
			return
		}
		if _, err := fmt.Fprintln(s.out, lineText); err != nil {
			s.failed.Store(true)
			return
		}
		s.notes = appendBoundedLines(s.notes, line{{text: lineText, style: "text"}})
		if s.last != nil {
			if err := s.drawLocked(*s.last); err != nil {
				s.failed.Store(true)
			}
		}
		return
	}
	if _, err := fmt.Fprintln(s.out, lineText); err != nil {
		s.failed.Store(true)
	}
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
	lines := s.layout.frame(update, width, height, s.table.Load())
	if !s.active {
		if _, err := s.out.WriteString("\x1b[?25l"); err != nil {
			return err
		}
		s.active = true
		s.activeFlag.Store(true)
	}
	if err := s.removeFrameLocked(); err != nil {
		return err
	}
	if err := writeStyledFrame(s.out, lines, s.theme, true, !s.noColor); err != nil {
		return err
	}
	s.frameRows = len(lines)
	return nil
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
