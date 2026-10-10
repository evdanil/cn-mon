//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"syscall"
)

func isClosedOutputPipe(err error, writer io.Writer) bool {
	var code syscall.Errno
	if !errors.As(err, &code) {
		return false
	}
	switch uint32(code) {
	case 109, 232: // ERROR_BROKEN_PIPE, ERROR_NO_DATA
		return true
	case 6, 87: // ERROR_INVALID_HANDLE / ERROR_INVALID_PARAMETER may come from a closed stdout pipe.
		file, ok := writer.(*os.File)
		if !ok || file != os.Stdout {
			return false
		}
		info, statErr := file.Stat()
		return statErr == nil && info.Mode()&os.ModeNamedPipe != 0
	default:
		return false
	}
}
