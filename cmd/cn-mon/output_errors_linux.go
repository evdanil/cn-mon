//go:build linux

package main

import (
	"errors"
	"io"
	"syscall"
)

func isClosedOutputPipe(err error, _ io.Writer) bool {
	return errors.Is(err, syscall.EPIPE)
}
