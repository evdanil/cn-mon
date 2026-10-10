//go:build linux

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/evdanil/gotest/internal/target"
)

const linuxCAPNetRaw = uint64(1 << 13)

// TestNativeCommandLinuxProbePermissionRole proves that the built command
// distinguishes raw-socket permission from a healthy probe while preserving
// TCP and completed history. The native workflow runs this test once with and
// once without CAP_NET_RAW.
func TestNativeCommandLinuxProbePermissionRole(t *testing.T) {
	requireNativeCommand(t)
	role := os.Getenv("CNMON_NATIVE_COMMAND_EXPECT_PROBE")
	if role != "granted" && role != "denied" {
		t.Fatalf("CNMON_NATIVE_COMMAND_EXPECT_PROBE must be granted or denied, got %q", role)
	}
	uid, euid := os.Getuid(), os.Geteuid()
	effective, permitted, ambient := linuxRawCapabilityFacts(t)
	if role == "granted" {
		if !effective || !permitted {
			t.Fatalf("granted role lacks CAP_NET_RAW: uid=%d euid=%d effective=%t permitted=%t ambient=%t", uid, euid, effective, permitted, ambient)
		}
	} else if euid == 0 || effective || permitted || ambient {
		t.Fatalf("denied role is not an unprivileged process without CAP_NET_RAW: uid=%d euid=%d effective=%t permitted=%t ambient=%t", uid, euid, effective, permitted, ambient)
	}

	binary := buildNativeCommand(t)
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat built command: %v", err)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Fatalf("built command has setuid/setgid mode bits: %v", info.Mode())
	}
	root := t.TempDir()
	database := filepath.Join(root, "permission-role.db")
	logPath := filepath.Join(root, "permission-role.log")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	request, rejected, err := target.Build([]string{"127.0.0.1"}, []int{port})
	if err != nil || len(rejected) != 0 {
		t.Fatalf("build request: request=%+v rejected=%+v err=%v", request, rejected, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary,
		"--format", "json", "--history-file", database, "--log-file", logPath,
		"--tcp", strconv.Itoa(port), "127.0.0.1",
	)
	cmd.Env = nativeCommandEnv(t, root, database, logPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			t.Fatalf("start native command: %v", runErr)
		}
		code = exit.ExitCode()
	}

	// The dynamic TCP result field is decoded separately because JSON names
	// include the selected ephemeral port.
	var rawReport struct {
		Latest []map[string]json.RawMessage `json:"latest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rawReport); err != nil {
		t.Fatalf("decode native command JSON: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if len(rawReport.Latest) != 1 {
		t.Fatalf("latest rows=%d, want one loopback host; exit=%d stdout=%q stderr=%q", len(rawReport.Latest), code, stdout.String(), stderr.String())
	}
	var host, icmpState, tcpState string
	for field, targetValue := range map[string]*string{
		"host": &host, "result": &icmpState, fmt.Sprintf("tcp_%d", port): &tcpState,
	} {
		if err := json.Unmarshal(rawReport.Latest[0][field], targetValue); err != nil {
			t.Fatalf("decode latest.%s: %v; stdout=%q", field, err, stdout.String())
		}
	}
	if host != "127.0.0.1" || tcpState != "open" {
		t.Fatalf("latest host/TCP = %q/%q, want 127.0.0.1/open; stderr=%q", host, tcpState, stderr.String())
	}
	wantState, wantCode := "OK", 0
	if role == "denied" {
		wantState, wantCode = "ERROR", 3
	}
	if icmpState != wantState || code != wantCode {
		t.Fatalf("permission role %s produced ICMP=%q exit=%d; want %q/exit %d; stderr=%q", role, icmpState, code, wantState, wantCode, stderr.String())
	}
	if role == "denied" {
		assertLinuxPermissionDiagnostic(t, stderr.String())
	}

	reader, err := history.OpenReader(context.Background(), database)
	if err != nil || reader == nil {
		t.Fatalf("open command history: reader=%v err=%v", reader, err)
	}
	defer reader.Close()
	err = reader.View(context.Background(), func(snapshot *history.Snapshot) error {
		info, err := snapshot.Request(request.Key)
		if err != nil {
			return err
		}
		if info == nil || info.Rounds != 1 || info.NewestRun == nil || info.NewestRun.Status != "finished" {
			return fmt.Errorf("persisted request/run = %+v, want one finished round", info)
		}
		page, err := snapshot.Rounds(info.ID, history.RoundFilter{Limit: 10})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || len(page.Items[0].Rows) != 1 {
			return fmt.Errorf("persisted rounds = %+v, want one host row", page.Items)
		}
		row := page.Items[0].Rows[0]
		if row.Host != host || row.TCP[port] != "open" || row.Result != icmpState {
			return fmt.Errorf("persisted host row = %+v, want ICMP=%q TCP=open", row, icmpState)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read completed command history: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("configured log file missing: %v", err)
	}
	t.Logf("CNMON_NATIVE_COMMAND_LINUX_PERMISSION uid=%d euid=%d cap_net_raw_eff=%t cap_net_raw_prm=%t cap_net_raw_amb=%t role=%s icmp=%s exit=%d tcp=open history=finished-round", uid, euid, effective, permitted, ambient, role, icmpState, code)
}

func assertLinuxPermissionDiagnostic(t *testing.T, stderr string) {
	t.Helper()
	conn, cause := net.ListenPacket("ip4:icmp", "127.0.0.1")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("denied role unexpectedly opened a raw ICMP socket while determining the expected OS cause")
	}
	if cause == nil {
		t.Fatal("denied role's raw ICMP socket open returned neither a connection nor an error")
	}
	for {
		unwrapper, ok := cause.(interface{ Unwrap() error })
		if !ok || unwrapper.Unwrap() == nil {
			break
		}
		cause = unwrapper.Unwrap()
	}
	causeText := strings.TrimSpace(cause.Error())
	if causeText == "" {
		t.Fatal("raw ICMP socket denial had an empty underlying cause")
	}
	var matching []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "127.0.0.1") && strings.Contains(line, "ICMP") {
			matching = append(matching, strings.TrimSuffix(line, "\r"))
		}
	}
	if len(matching) != 1 {
		t.Fatalf("stderr has %d host/ICMP diagnostics, want exactly one: %q", len(matching), stderr)
	}
	diagnostic := matching[0]
	if len([]byte(diagnostic)) > 4096 {
		t.Fatalf("permission diagnostic is %d bytes, over the 4096-byte limit", len([]byte(diagnostic)))
	}
	if strings.ContainsAny(diagnostic, "\r\n") {
		t.Fatalf("permission diagnostic is not one line: %q", diagnostic)
	}
	if !strings.Contains(diagnostic, causeText) {
		t.Fatalf("permission diagnostic omits the observed OS cause %q: %q", causeText, diagnostic)
	}
	if !strings.Contains(diagnostic, "CAP_NET_RAW") || !strings.Contains(diagnostic, "authorized elevated process") {
		t.Fatalf("permission diagnostic omits the Linux remedy: %q", diagnostic)
	}
}

func linuxRawCapabilityFacts(t *testing.T) (effective, permitted, ambient bool) {
	t.Helper()
	file, err := os.Open("/proc/self/status")
	if err != nil {
		t.Fatalf("open /proc/self/status for capability facts: %v", err)
	}
	defer file.Close()
	values := make(map[string]uint64, 3)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, raw, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		if name != "CapEff" && name != "CapPrm" && name != "CapAmb" {
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(raw), 16, 64)
		if err != nil {
			t.Fatalf("parse %s capability mask: %v", name, err)
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read /proc/self/status: %v", err)
	}
	for _, key := range []string{"CapEff", "CapPrm", "CapAmb"} {
		if _, ok := values[key]; !ok {
			t.Fatalf("/proc/self/status omitted %s", key)
		}
	}
	return values["CapEff"]&linuxCAPNetRaw != 0, values["CapPrm"]&linuxCAPNetRaw != 0, values["CapAmb"]&linuxCAPNetRaw != 0
}
