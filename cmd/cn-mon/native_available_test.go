// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build native

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/target"
)

const nativeNoNeverSeenLine = "cn-mon: no never-seen IPs: every host of this target line replied at least once"

// TestNativeCommandAvailable runs the built command through the three
// --available cases of M13 (cn-tool PR #178, merge
// a739948c69ada9bab27d25058c1d695ca06d5aba): a target run whose own seen round
// leaves the never-seen list empty, a --show of a seeded request that holds a
// never-replying subnet, and a refusal that must do no I/O. Only loopback and
// documentation addresses are used, and the latter are never probed.
func TestNativeCommandAvailable(t *testing.T) {
	requireNativeCommand(t)
	binary := buildNativeCommand(t)
	root := t.TempDir()
	database, logPath := filepath.Join(root, "history.db"), filepath.Join(root, "available.log")

	// (a) Run: TCP to a loopback listener makes the host seen whatever the ICMP
	// role is, so the section is present and empty, the no-never-seen line is
	// said, and the round is recorded.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	runRequest, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build loopback request: %+v %+v %v", runRequest, rejected, err)
	}
	code, stdout, stderr := runNativeRerunProcess(t, binary, root, database, logPath,
		"127.0.0.1", "--tcp", fmt.Sprint(port), "--available", "--format", "json")
	report := decodeNativeRerunReport(t, stdout, port)
	// Exit 0 with OK, or the role's ICMP-denied exit 3 with an ERROR result.
	verifyNativeRerunProbeOutcome(t, code, report.Latest[0].Result, string(stderr))
	if report.Latest[0].TCP != "open" {
		t.Fatalf("run TCP=%q, want open; stderr=%q", report.Latest[0].TCP, stderr)
	}
	t.Logf("run role outcome: icmp=%q exit=%d", report.Latest[0].Result, code)
	if rows, present := decodeNativeUnseen(t, stdout); !present || len(rows) != 0 {
		t.Fatalf("run unseen = %+v (present %t), want the key present and empty:\n%s", rows, present, stdout)
	}
	if strings.Count(string(stderr), nativeNoNeverSeenLine) != 1 {
		t.Fatalf("run stderr = %q, want the no-never-seen line exactly once", stderr)
	}
	if got := nativeStoredRounds(t, database, runRequest.Key); got != 1 {
		t.Fatalf("the run recorded %d rounds, want 1", got)
	}

	// (b) Show: a seeded request with a never-replying documentation subnet and
	// a replying loopback literal. Nothing in this case is probed.
	showRequest, rejected, err := target.Build([]string{"192.0.2.0/30", "127.0.0.1"}, nil)
	if err != nil || len(rejected) != 0 || len(showRequest.Hosts) != 3 {
		t.Fatalf("build seeded request: %+v %+v %v", showRequest, rejected, err)
	}
	seedNativeAvailable(t, database, showRequest)
	showArgs := []string{"--show", showRequest.Key[:8], "--available"}

	code, stdout, stderr = runNativeRerunProcess(t, binary, root, database, logPath, append(showArgs, "--format", "csv")...)
	if code != 0 {
		t.Fatalf("--show --available csv exit=%d stderr=%q", code, stderr)
	}
	records, err := csv.NewReader(bytes.NewReader(stdout)).ReadAll()
	if err != nil || len(records) == 0 {
		t.Fatalf("show CSV is not parseable: %v\n%s", err, stdout)
	}
	var csvRows []map[string]string
	for _, record := range records[1:] {
		if record[0] != "unseen" {
			continue
		}
		row := map[string]string{}
		for i, column := range records[0][1:] {
			row[column] = record[i+1]
		}
		csvRows = append(csvRows, row)
	}
	if len(csvRows) != 2 {
		t.Fatalf("CSV unseen rows = %v, want the two documentation hosts:\n%s", csvRows, stdout)
	}
	for i, want := range []map[string]string{
		{"Host": "192.0.2.1", "Status": "Unseen", "Seen": "0 of 2 rounds", "Failed rounds": "0", "Note": monitor.SilentSubnetNote},
		{"Host": "192.0.2.2", "Status": "Unseen", "Seen": "0 of 1 rounds", "Failed rounds": "1", "Note": monitor.SilentSubnetNote},
	} {
		for column, value := range want {
			if csvRows[i][column] != value {
				t.Fatalf("CSV unseen row %d column %q = %q, want %q\n%s", i, column, csvRows[i][column], value, stdout)
			}
		}
		if csvRows[i]["Since"] == "" {
			t.Fatalf("CSV unseen row %d has an empty Since", i)
		}
	}
	if want := "cn-mon: no IP in 192.0.2.0/30 ever replied; this machine may not be able to reach it\n"; string(stderr) != want {
		t.Fatalf("show stderr = %q, want %q", stderr, want)
	}

	code, stdout, stderr = runNativeRerunProcess(t, binary, root, database, logPath, append(showArgs, "--format", "json")...)
	if code != 0 {
		t.Fatalf("--show --available json exit=%d stderr=%q", code, stderr)
	}
	rows, present := decodeNativeUnseen(t, stdout)
	if !present || len(rows) != 2 || rows[0].Rounds != 2 || rows[1].Rounds != 1 || rows[1].FailedRounds != 1 || rows[0].SeenRounds != 0 {
		t.Fatalf("show JSON unseen = %+v (present %t), want two rows with numeric rounds 2 and 1, failed 0 and 1", rows, present)
	}

	// (c) Refusal: --available never goes with --since, exits 2 with an empty
	// stdout, and does not touch the database or the log.
	before := nativeFileDigest(t, database)
	logBefore := nativeFileDigest(t, logPath)
	code, stdout, stderr = runNativeRerunProcess(t, binary, root, database, logPath, append(showArgs, "--since", "1h")...)
	if code != 2 || len(stdout) != 0 || !strings.Contains(string(stderr), "--available does not go with --since") {
		t.Fatalf("refusal: exit=%d stdout=%q stderr=%q, want exit 2, empty stdout and the --since refusal", code, stdout, stderr)
	}
	if after := nativeFileDigest(t, database); after != before {
		t.Fatalf("the refusal changed the history database")
	}
	if after := nativeFileDigest(t, logPath); after != logBefore {
		t.Fatalf("the refusal appended to the log")
	}

	t.Log("CNMON_NATIVE_COMMAND_AVAILABLE run=unseen-empty show=rows silent=noted refusal=exit2-no-io")
}

type nativeUnseenRow struct {
	Host         string `json:"host"`
	Status       string `json:"status"`
	SeenRounds   int    `json:"seen_rounds"`
	Rounds       int    `json:"rounds"`
	FailedRounds int    `json:"failed_rounds"`
	Since        string `json:"since"`
	Note         string `json:"note"`
}

func decodeNativeUnseen(t *testing.T, encoded []byte) (rows []nativeUnseenRow, present bool) {
	t.Helper()
	var report struct {
		Unseen *[]nativeUnseenRow `json:"unseen"`
	}
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("decode unseen section: %v\n%s", err, encoded)
	}
	if report.Unseen == nil {
		return nil, false
	}
	return *report.Unseen, true
}

// seedNativeAvailable records two finished rounds through the history API:
// 127.0.0.1 answers in both, 192.0.2.1 is silent in both, and 192.0.2.2 is
// silent once and fails once.
func seedNativeAvailable(t *testing.T, database string, request target.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := history.OpenWriter(ctx, database)
	if err != nil {
		t.Fatalf("open seed history: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close seed history: %v", err)
		}
	}()
	first := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	if _, err := store.BeginRun(ctx, history.Request{
		RequestMeta: history.RequestMeta{Key: request.Key, Targets: request.Tokens, TCPPorts: request.Ports}, Hosts: request.Hosts,
	}, history.RunStart{ID: "native-available-seed", Started: first, Host: "native-test", PID: 1, Heartbeat: first}); err != nil {
		t.Fatalf("begin seed run: %v", err)
	}
	for step, second := range []byte{'.', 'E'} {
		started := first.Add(time.Duration(step) * time.Hour)
		observations := make([]byte, len(request.Hosts))
		var rows []history.ResultRow
		for i, host := range request.Hosts {
			switch host {
			case "127.0.0.1":
				observations[i] = '!'
				rows = append(rows, history.ResultRow{Host: host, Address: host, Result: "OK", Time: started, TCP: map[int]string{}})
			case "192.0.2.1":
				observations[i] = '.'
			default:
				observations[i] = second
			}
		}
		if _, err := store.RecordRound(ctx, "native-available-seed", history.RoundInput{Started: started, Observations: string(observations), Rows: rows}); err != nil {
			t.Fatalf("record seed round %q: %v", observations, err)
		}
	}
	if err := store.EndRun(ctx, "native-available-seed", first.Add(2*time.Hour), "finished"); err != nil {
		t.Fatalf("end seed run: %v", err)
	}
}

func nativeStoredRounds(t *testing.T, database, key string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader, err := history.OpenReader(ctx, database)
	if err != nil || reader == nil {
		t.Fatalf("open command-created history: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	var rounds int64
	err = reader.View(ctx, func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(key)
		if err != nil {
			return err
		}
		if info != nil {
			rounds = info.Rounds
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read command-created history: %v", err)
	}
	return rounds
}

func nativeFileDigest(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(data)
}
