//go:build linux

package main

import (
	"bytes"
	"syscall"
	"testing"
)

func TestLinuxEPIPERetainsOrdinaryExitWithoutDiagnostic(t *testing.T) {
	var stderr bytes.Buffer
	code := reportExitCode(2, syscall.EPIPE, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Fatalf("EPIPE exit = %d, want ordinary exit 2", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("EPIPE should be quiet, got diagnostic %q", stderr.String())
	}
}

func TestLinuxInvalidHandleIsNotSilencedAsClosedPipe(t *testing.T) {
	var stderr bytes.Buffer
	code := reportExitCode(0, syscall.EBADF, &bytes.Buffer{}, &stderr)
	if code != 3 || stderr.Len() == 0 {
		t.Fatalf("EBADF result code=%d diagnostic=%q; want report failure/3", code, stderr.String())
	}
}
