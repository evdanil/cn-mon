//go:build linux

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func collectNativeLinuxPTY(master *os.File, output *nativePTYOutput, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	buffer := make([]byte, 2048)
	for {
		select {
		case <-stop:
			return
		default:
		}
		fd := master.Fd()
		if fd == ^uintptr(0) || int32(fd) < 0 {
			return
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); errors.Is(err, unix.EINTR) {
			continue
		} else if err != nil {
			return
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			if fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
				return
			}
			continue
		}
		n, err := unix.Read(int(master.Fd()), buffer)
		if n > 0 {
			output.append(buffer[:n])
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) {
			return
		}
		if n == 0 && err == nil {
			return
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR) != 0 {
			// A readable hangup may still have more queued bytes than one read
			// can consume. Poll again before honoring it.
			continue
		}
	}
}
