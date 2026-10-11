// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestFrameDiffSkipsIdenticalRowsAndPatchesOnlyChangedRow(t *testing.T) {
	previous := [][]byte{[]byte("unchanged top"), []byte("old header"), []byte("unchanged bottom")}
	output := &countingRedrawWriter{}
	if err := writeFrameDiff(output, previous, previous, []int{13, 10, 16}, 40); err != nil {
		t.Fatal(err)
	}
	if output.calls != 0 {
		t.Fatalf("identical frame made %d writes, want no output", output.calls)
	}

	next := [][]byte{[]byte("unchanged top"), []byte("new header"), []byte("unchanged bottom")}
	if err := writeFrameDiff(output, previous, next, []int{13, 10, 16}, 40); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[1A\rnew header\x1b[0m\x1b[K\r\x1b[1B\r"
	if output.Buffer.String() != want || output.calls != 1 {
		t.Fatalf("row diff = %q in %d writes, want %q in one write", output.Buffer.String(), output.calls, want)
	}
}

type countingRedrawWriter struct {
	bytes.Buffer
	calls int
}

func (writer *countingRedrawWriter) Write(data []byte) (int, error) {
	writer.calls++
	return writer.Buffer.Write(data)
}

func TestFrameDiffClearsShortenedStyledRowWithoutNewline(t *testing.T) {
	previous := [][]byte{[]byte("\x1b[1mLong styled header\x1b[0m"), []byte("bottom")}
	next := [][]byte{[]byte("\x1b[1mShort\x1b[0m"), []byte("bottom")}
	var output bytes.Buffer
	if err := writeFrameDiff(&output, previous, next, []int{5, 6}, 30); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[1A\r\x1b[1mShort\x1b[0m\x1b[0m\x1b[K\r\x1b[1B\r"
	if output.String() != want {
		t.Fatalf("shortened styled row diff = %q, want %q", output.String(), want)
	}
}

func TestFrameDiffFallsBackToFullFrameWhenRowCountChanges(t *testing.T) {
	previous := [][]byte{[]byte("one"), []byte("two")}
	next := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	var output bytes.Buffer
	if err := writeFrameDiff(&output, previous, next, []int{3, 3, 5}, 10); err != nil {
		t.Fatal(err)
	}
	want := "\rone\r\ntwo\r\nthree\r"
	if output.String() != want {
		t.Fatalf("row-count fallback = %q, want %q", output.String(), want)
	}
}

func TestFrameDiffAnchorsFullWidthAndBottomRowWithoutNewline(t *testing.T) {
	rows := [][]byte{[]byte("12345"), []byte("abcde")}
	var output bytes.Buffer
	if err := writeFrameDiff(&output, nil, rows, []int{5, 5}, 5); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "\r12345\r\nabcde\r"; got != want {
		t.Fatalf("full-width frame = %q, want %q", got, want)
	}

	output.Reset()
	if err := writeFrameDiff(&output, rows, [][]byte{rows[0], []byte("vwxyz")}, []int{5, 5}, 5); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "\rvwxyz\x1b[0m\r"; got != want {
		t.Fatalf("bottom-row patch = %q, want %q", got, want)
	}
}

func TestScreenPublishesFrameOnlyAfterSuccessfulWrite(t *testing.T) {
	screen := &Screen{frame: &presentedFrame{rows: [][]byte{[]byte("old")}}}
	old := screen.frame
	err := screen.writeAndRememberFrame(shortFrameWriter{}, nil, [][]byte{[]byte("new")}, []int{3}, 20, 2, false, nil, 1)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("write error = %v, want io.ErrShortWrite", err)
	}
	if screen.frame != old {
		t.Fatal("failed write published a partially presented frame")
	}
}

func TestScreenInvalidationDuringWriteRemainsPending(t *testing.T) {
	screen := &Screen{}
	generation := screen.frameDirty.Load()
	writer := blockingFrameWriter{entered: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan error, 1)
	go func() {
		finished <- screen.writeAndRememberFrame(writer, nil, [][]byte{[]byte("row")}, []int{3}, 20, 2, false, nil, generation)
	}()
	<-writer.entered
	screen.invalidateFrame()
	close(writer.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if screen.frame == nil || screen.frame.generation != generation || screen.frameDirty.Load() == generation {
		t.Fatalf("write consumed invalidation: cached=%+v current generation=%d", screen.frame, screen.frameDirty.Load())
	}
}

type shortFrameWriter struct{}

func (shortFrameWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

type blockingFrameWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (writer blockingFrameWriter) Write(data []byte) (int, error) {
	close(writer.entered)
	<-writer.release
	return len(data), nil
}
