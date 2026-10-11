//go:build !windows

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import "io"

func withReportConsoleVT(_ io.Writer, _ bool, _ func(func()), write func() error) error {
	return write()
}
