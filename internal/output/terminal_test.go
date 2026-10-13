// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/probe"
	"github.com/evdanil/cn-mon/internal/target"
)

func TestDispatchKeyDoesNotWaitForScreenFrameLock(t *testing.T) {
	var quits atomic.Int32
	s := &Screen{keys: KeyHandlers{Quit: func() { quits.Add(1) }}}
	s.mu.Lock() // Models a frame write blocked in the draw owner.
	dispatched := make(chan struct{})
	go func() {
		s.dispatchKey('v')
		s.dispatchKey('q')
		close(dispatched)
	}()
	select {
	case <-dispatched:
	case <-time.After(100 * time.Millisecond):
		s.mu.Unlock()
		t.Fatal("key dispatch waited for the frame lock")
	}
	if !s.table.Load() {
		s.mu.Unlock()
		t.Fatal("view preference was not toggled")
	}
	if quits.Load() != 1 {
		s.mu.Unlock()
		t.Fatalf("quit callback count = %d, want 1", quits.Load())
	}
	s.mu.Unlock()
}

func TestScreenPermanentlyDisablesAfterOutputFailure(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	s := OpenScreen(nil, writer, "pastel", true, KeyHandlers{})
	defer s.Close()
	s.Note("write should fail")
	if !s.failed.Load() {
		t.Fatal("screen did not disable itself after a failed terminal write")
	}
	s.Note("later writes must be suppressed")
	if !s.failed.Load() {
		t.Fatal("failed state was cleared")
	}
}

func TestScreenChangeDoesNotWriteToRedirectedOutput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := OpenScreen(nil, writer, "pastel", true, KeyHandlers{})
	s.Change(monitor.Change{Host: "192.0.2.1", Seen: true, Observation: 'R'})
	s.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("redirected output got live Change text %q", data)
	}
}

func TestScreenNoteSurvivesDisabledDrawing(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := OpenScreen(nil, writer, "pastel", true, KeyHandlers{})
	s.failed.Store(true) // Models a prior terminal size/write failure.
	s.Note("required diagnostic")
	s.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "required diagnostic") {
		t.Fatalf("diagnostic was lost after drawing was disabled: %q", data)
	}
}

func TestRemoveFrameDoesNotMoveUpFromFinalRow(t *testing.T) {
	for _, tc := range []struct {
		rows int
		want string
	}{
		{rows: 1, want: "\r\x1b[1M"},
		{rows: 3, want: "\x1b[2A\r\x1b[3M"},
	} {
		t.Run(fmt.Sprintf("rows_%d", tc.rows), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			s := &Screen{out: writer, active: true, frameRows: tc.rows}
			if err := s.removeFrameLocked(); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Fatalf("remove frame sequence = %q, want %q", data, tc.want)
			}
		})
	}
}

type committedHeaderProber struct{}

func (committedHeaderProber) Resolve(_ context.Context, hosts []string) ([]probe.Resolved, error) {
	return []probe.Resolved{{Index: 0, Host: hosts[0], Address: netip.MustParseAddr(hosts[0])}}, nil
}

func (committedHeaderProber) ProbeBatch(_ context.Context, targets []probe.Resolved, _ []int) ([]probe.Result, error) {
	return []probe.Result{{Target: targets[0], ICMP: probe.ICMPResult{State: "OK", Sent: 2, Received: 2}}}, nil
}

func TestCommittedDrawDoesNotAdvertiseNextInFlightRound(t *testing.T) {
	request, rejected, err := target.Build([]string{"192.0.2.1"}, nil)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request: rejected=%v err=%v", rejected, err)
	}
	_, err = monitor.Run(context.Background(), monitor.RunPlan{
		Request: request, OwnRunOnly: true, HistoryFile: "unused", HistoryDays: big.NewInt(30), RunID: "committed-header",
	}, monitor.RunDeps{
		Prober:      committedHeaderProber{},
		OpenHistory: func(context.Context, string) (monitor.History, error) { return nil, nil },
		Live: &monitor.LiveHooks{Draw: func(update monitor.LiveUpdate) {
			if update.CurrentRounds != 1 || update.Data.Stats.Rounds != 1 || update.Phase == monitor.LiveEnded {
				return
			}
			if header := liveHeader(update, 200); strings.Contains(header, "round 2") {
				t.Errorf("committed round 1 displayed as a later in-flight round: %q phase=%v", header, update.Phase)
			}
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
