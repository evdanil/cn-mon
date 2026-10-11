// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

//go:build linux && native

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evdanil/gotest/internal/history"
)

// TestNativeReleaseLinuxNamespaceCommand runs the exact extracted artifact in
// the same granted namespace phases as the probe component gate. It verifies
// successful IPv4/IPv6 echo on the ordinary echo phase and preserves the
// existing controlled target/router error phases at the command boundary.
func TestNativeReleaseLinuxNamespaceCommand(t *testing.T) {
	requireNativeReleaseQualification(t)
	if os.Getenv("CNMON_NATIVE") != "1" || os.Getenv("CNMON_NATIVE_COMMAND") != "1" {
		t.Fatal("release namespace qualification requires native probe and command gates")
	}
	if os.Geteuid() != 0 {
		t.Fatalf("release echo command must run in the existing granted root role, got euid=%d", os.Geteuid())
	}
	phase := os.Getenv("CNMON_NATIVE_PHASE")
	if phase != "echo" && phase != "target-error" && phase != "intermediate-error" {
		t.Fatalf("release namespace command got unsupported phase %q", phase)
	}
	binary := buildNativeCommand(t)
	if !filepath.IsAbs(binary) {
		t.Fatalf("extracted release command path is not absolute: %q", binary)
	}
	target4 := os.Getenv("CNMON_NATIVE_IPV4_TARGET")
	target6 := os.Getenv("CNMON_NATIVE_IPV6_TARGET")
	if target4 == "" || target6 == "" {
		t.Fatal("native namespace target addresses are required")
	}
	address4, err := net.ResolveIPAddr("ip4", target4)
	if err != nil || address4.IP.To4() == nil {
		t.Fatalf("invalid native IPv4 target %q: %v", target4, err)
	}
	address6, err := net.ResolveIPAddr("ip6", target6)
	if err != nil || address6.IP.To4() != nil || address6.IP.To16() == nil {
		t.Fatalf("invalid native IPv6 target %q: %v", target6, err)
	}

	directory := t.TempDir()
	listener4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on namespace IPv4 loopback: %v", err)
	}
	defer listener4.Close()
	listener6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen on namespace IPv6 loopback: %v", err)
	}
	defer listener6.Close()
	port4 := listener4.Addr().(*net.TCPAddr).Port
	port6 := listener6.Addr().(*net.TCPAddr).Port
	database := filepath.Join(directory, "release namespace history.db")
	logPath := filepath.Join(directory, "release namespace command.log")
	pathDir := filepath.Join(directory, "empty child PATH")
	if err := os.Mkdir(pathDir, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port4) + "," + strconv.Itoa(port6),
		"127.0.0.1", "::1", target4, target6}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = directory
	env := nativeCommandEnv(t, directory, database, logPath)
	env = append(env,
		"CNMON_NATIVE=1",
		"CNMON_NATIVE_PHASE="+phase,
		"CNMON_NATIVE_IPV4_TARGET="+target4,
		"CNMON_NATIVE_IPV6_TARGET="+target6,
		"CNMON_RELEASE_QUALIFY=1",
	)
	command.Env = nativeReleaseEnvWithPath(env, pathDir)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if ctx.Err() != nil {
		t.Fatalf("extracted namespace command timed out: phase=%s err=%v", phase, ctx.Err())
	}
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run extracted namespace command: %v", err)
		}
		code = exit.ExitCode()
	}
	if code != 0 {
		t.Fatalf("extracted namespace command exit=%d phase=%s stdout=%q stderr=%q", code, phase, stdout.String(), stderr.String())
	}
	var report struct {
		Latest []map[string]json.RawMessage `json:"latest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode extracted namespace JSON: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if len(report.Latest) != 4 {
		t.Fatalf("extracted namespace latest rows=%d, want four families/loopback rows; stdout=%q", len(report.Latest), stdout.String())
	}
	states := make(map[string]string, len(report.Latest))
	observations := make(map[string]nativeReleaseObservation, len(report.Latest))
	for _, row := range report.Latest {
		var host, address, state string
		if err := json.Unmarshal(row["host"], &host); err != nil {
			t.Fatalf("decode namespace result host: %v row=%v", err, row)
		}
		if err := json.Unmarshal(row["address"], &address); err != nil {
			t.Fatalf("decode namespace result address: %v row=%v", err, row)
		}
		if err := json.Unmarshal(row["result"], &state); err != nil {
			t.Fatalf("decode namespace ICMP result for %s: %v row=%v", host, err, row)
		}
		states[host] = state
		observed := nativeReleaseObservation{Address: address, Result: state, TCP: make(map[int]string)}
		for _, port := range []int{port4, port6} {
			var tcp string
			if err := json.Unmarshal(row[fmt.Sprintf("tcp_%d", port)], &tcp); err != nil {
				t.Fatalf("decode namespace TCP result for %s port %d: %v row=%v", host, port, err, row)
			}
			observed.TCP[port] = tcp
		}
		observations[host] = observed
	}
	if states["127.0.0.1"] != "OK" && !strings.HasPrefix(states["127.0.0.1"], "OK (") {
		t.Fatalf("extracted IPv4 loopback echo state=%q", states["127.0.0.1"])
	}
	if states["::1"] != "OK" && !strings.HasPrefix(states["::1"], "OK (") {
		t.Fatalf("extracted IPv6 loopback echo state=%q", states["::1"])
	}
	wantRemote := "OK"
	if phase == "target-error" {
		wantRemote = "REJECTED"
	} else if phase == "intermediate-error" {
		wantRemote = "UNREACHABLE"
	}
	for host, family := range map[string]string{address4.IP.String(): "ipv4", address6.IP.String(): "ipv6"} {
		if states[host] != wantRemote {
			t.Fatalf("extracted %s target %s state=%q phase=%s, want %q", family, host, states[host], phase, wantRemote)
		}
	}
	for host, port := range map[string]int{"127.0.0.1": port4, "::1": port6} {
		if tcp := observations[host].TCP[port]; tcp != "open" {
			t.Fatalf("extracted %s TCP state=%q, want open", host, tcp)
		}
	}
	verifyNativeReleaseHistory(t, database, observations)
	if phase == "echo" {
		t.Log("CNMON_NATIVE_RELEASE_NAMESPACE phase=echo role=root ipv4=echo ipv6=echo tcp4=open tcp6=open history=finished path=empty")
	} else {
		t.Logf("CNMON_NATIVE_RELEASE_NAMESPACE phase=%s role=root ipv4=%s ipv6=%s tcp4=open tcp6=open history=finished path=empty", phase, states[address4.IP.String()], states[address6.IP.String()])
	}
}

type nativeReleaseObservation struct {
	Address string
	Result  string
	TCP     map[int]string
}

func verifyNativeReleaseHistory(t *testing.T, path string, expected map[string]nativeReleaseObservation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := history.OpenReader(ctx, path)
	if err != nil {
		t.Fatalf("open extracted command history: %v", err)
	}
	defer store.Close()
	err = store.View(ctx, func(snapshot *history.Snapshot) error {
		page, err := snapshot.Requests(nil, 10)
		if err != nil {
			return err
		}
		if len(page.Items) != 1 {
			return fmt.Errorf("stored requests=%d, want one", len(page.Items))
		}
		request, err := snapshot.Request(page.Items[0].Key)
		if err != nil {
			return err
		}
		if request == nil {
			return fmt.Errorf("stored request lookup returned nil")
		}
		if len(request.Hosts) != len(expected) {
			return fmt.Errorf("stored request hosts=%v, want %d command hosts", request.Hosts, len(expected))
		}
		requestHosts := make(map[string]bool, len(request.Hosts))
		for _, host := range request.Hosts {
			if _, ok := expected[host]; !ok {
				return fmt.Errorf("stored history has unexpected host %q", host)
			}
			requestHosts[host] = true
		}
		for host := range expected {
			if !requestHosts[host] {
				return fmt.Errorf("stored history omitted requested host %q", host)
			}
		}
		runs, err := snapshot.Runs(request.ID, false, nil, 10)
		if err != nil {
			return err
		}
		if len(runs.Items) != 1 || runs.Items[0].Status != "finished" || runs.Items[0].Rounds < 1 || runs.Items[0].Ended == nil {
			return fmt.Errorf("stored run=%+v, want one finished run with completed round", runs.Items)
		}
		rounds, err := snapshot.Rounds(request.ID, history.RoundFilter{Limit: 10})
		if err != nil {
			return err
		}
		if len(rounds.Items) != 1 || len(rounds.Items[0].Rows) != len(expected) {
			return fmt.Errorf("stored rounds/rows=%d/%d, want one round with %d rows", len(rounds.Items), roundRowCount(rounds.Items), len(expected))
		}
		storedHosts := make(map[string]bool, len(rounds.Items[0].Rows))
		for _, row := range rounds.Items[0].Rows {
			want, ok := expected[row.Host]
			if !ok {
				return fmt.Errorf("stored round has unexpected row host %q", row.Host)
			}
			if storedHosts[row.Host] {
				return fmt.Errorf("stored round repeats host %q", row.Host)
			}
			storedHosts[row.Host] = true
			if row.Address != want.Address || row.Result != want.Result || len(row.TCP) != len(want.TCP) {
				return fmt.Errorf("stored row for %s=%+v, want JSON observation %+v", row.Host, row, want)
			}
			for port, state := range want.TCP {
				if row.TCP[port] != state {
					return fmt.Errorf("stored row %s TCP %d=%q, JSON reports %q", row.Host, port, row.TCP[port], state)
				}
			}
		}
		for host := range expected {
			if !storedHosts[host] {
				return fmt.Errorf("stored round omitted host %q", host)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read extracted command history: %v", err)
	}
}

func roundRowCount(rounds []history.StoredRound) int {
	if len(rounds) == 0 {
		return 0
	}
	return len(rounds[0].Rows)
}
