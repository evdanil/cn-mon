// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package output

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type terminalPlatform struct {
	inFD         int
	outFD        int
	inputTTY     bool
	outputTTY    bool
	changed      bool
	originalMode atomic.Pointer[unix.Termios]
	inputErr     error
	mu           sync.Mutex
	suspend      chan os.Signal
	resume       chan os.Signal
	parser       linuxKeyParser
}

type linuxKeyParser struct {
	escaped    bool
	sequence   bool
	escapedAt  time.Time
	sequenceAt time.Time
}

func (p *linuxKeyParser) feed(value byte, now time.Time) (rune, bool) {
	if p.escaped && now.Sub(p.escapedAt) > 50*time.Millisecond {
		p.escaped = false
	}
	if p.sequence && now.Sub(p.sequenceAt) > 50*time.Millisecond {
		p.sequence = false
	}
	if p.sequence {
		if value >= 0x40 && value <= 0x7e {
			p.sequence = false
		} else {
			p.sequenceAt = now
		}
		return 0, false
	}
	if p.escaped {
		p.escaped = false
		if value == '[' || value == 'O' {
			p.sequence = true
			p.sequenceAt = now
		}
		return 0, false
	}
	if value == 0x1b {
		p.escaped = true
		p.escapedAt = now
		return 0, false
	}
	return rune(value), true
}

func (s *Screen) initPlatform() {
	p := &terminalPlatform{inFD: -1, outFD: -1}
	if s.in != nil {
		p.inFD = int(s.in.Fd())
		p.inputTTY = term.IsTerminal(p.inFD)
	}
	if s.out != nil {
		p.outFD = int(s.out.Fd())
		p.outputTTY = term.IsTerminal(p.outFD)
	}
	if p.inputTTY {
		p.suspend = make(chan os.Signal, 1)
		p.resume = make(chan os.Signal, 1)
		signal.Notify(p.suspend, syscall.SIGTSTP)
		signal.Notify(p.resume, syscall.SIGCONT)
	}
	s.platform = p
	if p.inputTTY && linuxForeground(p.inFD) {
		p.ensureInputMode()
	}
}

func (s *Screen) platformInputEnabled() bool {
	p := s.platform.(*terminalPlatform)
	return p.inputTTY
}

func (s *Screen) platformEnabled() bool {
	p := s.platform.(*terminalPlatform)
	if !p.outputTTY {
		return false
	}
	foreground := linuxForeground(p.outFD)
	if foreground {
		p.ensureInputMode()
	}
	return foreground
}

func (s *Screen) platformLost() bool {
	p := s.platform.(*terminalPlatform)
	return p.outFD < 0 || !term.IsTerminal(p.outFD)
}

func (s *Screen) platformSize() (int, int, error) {
	p := s.platform.(*terminalPlatform)
	if p.outFD < 0 || !p.outputTTY {
		return 0, 0, errors.New("terminal output unavailable")
	}
	return term.GetSize(p.outFD)
}

func (s *Screen) readPlatformKeys() {
	p := s.platform.(*terminalPlatform)
	buffer := make([]byte, 32)
	for {
		if s.handlePlatformSignals(p) {
			continue
		}
		select {
		case <-s.done:
			return
		default:
		}
		if p.inputTTY && !linuxForeground(p.inFD) {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if p.inputTTY {
			if err := p.ensureInputMode(); err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
		}
		fds := []unix.PollFd{{Fd: int32(p.inFD), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 100)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		if p.inputTTY && !linuxForeground(p.inFD) {
			continue
		}
		n, readErr := unix.Read(p.inFD, buffer)
		if readErr != nil {
			if errors.Is(readErr, unix.EINTR) || errors.Is(readErr, unix.EAGAIN) {
				continue
			}
			return
		}
		for _, key := range buffer[:n] {
			if value, ok := p.parser.feed(key, time.Now()); ok {
				s.dispatchKey(value)
			}
		}
	}
}

func (s *Screen) platformClose() {
	p := s.platform.(*terminalPlatform)
	p.mu.Lock()
	if original := p.originalMode.Load(); p.changed && original != nil && p.inFD >= 0 && linuxForeground(p.inFD) {
		_ = unix.IoctlSetTermios(p.inFD, unix.TCSETS, original)
		p.changed = false
	}
	p.mu.Unlock()
	if p.suspend != nil {
		signal.Stop(p.suspend)
		signal.Stop(p.resume)
	}
}

func (s *Screen) platformEmergency() {
	p := s.platform.(*terminalPlatform)
	foreground := linuxForeground(p.inFD)
	if original := p.originalMode.Load(); original != nil && p.inFD >= 0 && foreground {
		_ = unix.IoctlSetTermios(p.inFD, unix.TCSETS, original)
	}
	if s.activeFlag.Load() {
		s.platformShowCursor()
	}
}

func (s *Screen) platformShowCursor() {
	p := s.platform.(*terminalPlatform)
	linuxWriteCursor(p.outFD, "\x1b[?25h")
}

func linuxForeground(fd int) bool {
	if fd < 0 {
		return false
	}
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil && foreground == unix.Getpgrp()
}

func (p *terminalPlatform) ensureInputMode() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.changed && p.inputTTY && linuxForeground(p.inFD) {
		if p.originalMode.Load() == nil {
			original, err := unix.IoctlGetTermios(p.inFD, unix.TCGETS)
			if err != nil {
				p.inputErr = err
				return err
			}
			p.originalMode.CompareAndSwap(nil, original)
		}
		p.inputErr = p.applyInputModeLocked()
	}
	return p.inputErr
}

func (p *terminalPlatform) applyInputModeLocked() error {
	original := p.originalMode.Load()
	if original == nil {
		return errors.New("terminal input mode has no foreground snapshot")
	}
	changed := *original
	changed.Lflag &^= unix.ICANON | unix.ECHO
	changed.Cc[unix.VMIN] = 0
	changed.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(p.inFD, unix.TCSETS, &changed); err != nil {
		return err
	}
	p.changed = true
	return nil
}

func (s *Screen) handlePlatformSignals(p *terminalPlatform) bool {
	if p.suspend == nil {
		return false
	}
	select {
	case <-p.suspend:
		p.mu.Lock()
		if original := p.originalMode.Load(); p.changed && original != nil && linuxForeground(p.inFD) {
			_ = unix.IoctlSetTermios(p.inFD, unix.TCSETS, original)
			p.changed = false
		}
		p.mu.Unlock()
		if s.activeFlag.Load() {
			linuxWriteCursor(p.outFD, "\x1b[?25h")
		}
		signal.Stop(p.suspend)
		signal.Reset(syscall.SIGTSTP)
		_ = unix.Kill(-unix.Getpgrp(), unix.SIGTSTP)
		signal.Notify(p.suspend, syscall.SIGTSTP)
		if linuxForeground(p.inFD) {
			p.ensureInputMode()
			if s.activeFlag.Load() {
				linuxWriteCursor(p.outFD, "\x1b[?25l")
			}
		}
		return true
	case <-p.resume:
		if linuxForeground(p.inFD) {
			p.ensureInputMode()
			if s.activeFlag.Load() {
				linuxWriteCursor(p.outFD, "\x1b[?25l")
			}
		}
		return true
	default:
		return false
	}
}

func linuxWriteCursor(terminalFD int, sequence string) {
	if !linuxCursorWriteAllowed(terminalFD, sequence) {
		return
	}
	fd, err := unix.Open("/dev/tty", unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		_, _ = unix.Write(fd, []byte(sequence))
		_ = unix.Close(fd)
	}
}

func linuxCursorWriteAllowed(fd int, sequence string) bool {
	if fd < 0 || !term.IsTerminal(fd) {
		return false
	}
	foreground := linuxForeground(fd)
	mode, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	return linuxCursorWritePermitted(foreground, err == nil && mode.Lflag&unix.TOSTOP == 0, sequence)
}

func linuxCursorWritePermitted(foreground, backgroundWritesAllowed bool, sequence string) bool {
	if foreground {
		return true
	}
	return sequence == "\x1b[?25h" && backgroundWritesAllowed
}
