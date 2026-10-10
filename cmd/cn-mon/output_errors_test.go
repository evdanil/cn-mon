package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestReportWriterFailureOverridesOrdinaryExit(t *testing.T) {
	var stderr bytes.Buffer
	code := reportExitCode(2, errors.New("disk full"), &bytes.Buffer{}, &stderr)
	if code != 3 {
		t.Fatalf("non-pipe report error exit = %d, want 3", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("write report")) {
		t.Fatalf("missing report diagnostic: %q", stderr.String())
	}
}

func TestGenericClosedWriterErrorIsNotMistakenForClosedStdoutPipe(t *testing.T) {
	var stderr bytes.Buffer
	code := reportExitCode(0, io.ErrClosedPipe, &bytes.Buffer{}, &stderr)
	if code != 3 {
		t.Fatalf("generic io.ErrClosedPipe exit = %d, want 3", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("generic closed writer error should be reported")
	}
}
