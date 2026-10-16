//go:build native && windows

// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nativeMSIXReparsePoint       = 0x00000400
	nativeMSIXMediumIntegrityRID = 0x00002000
	nativeMSIXAdministratorsSID  = "S-1-5-32-544"
	nativeMSIXPublishedVersion   = "cn-mon 0.3.0 (bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed)\n"
)

var nativeMSIXGetPackageFamilyName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetPackageFamilyName")

func TestNativeMSIXInstalledAlias(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" {
		t.Skip("installed MSIX qualification is an explicit hosted Windows gate")
	}

	alias, err := nativeMSIXInstalledAlias()
	if err != nil {
		nativeMSIXReject(t, "alias-selection")
	}
	childPID, err := nativeMSIXAliasHasPackageIdentity(alias)
	if err != nil {
		nativeMSIXReject(t, "package-identity")
	}
	if err := nativeMSIXRequireOrdinaryToken(); err != nil {
		nativeMSIXReject(t, "ordinary-token")
	}
	code, stdout, stderr := nativeMSIXRun(t, alias, "", nil, "--version")
	if code != 0 || len(stderr) != 0 || string(stdout) != nativeMSIXPublishedVersion {
		t.Fatalf("installed alias --version exit=%d stdout=%q stderr=%q, want exact published version", code, stdout, stderr)
	}
	t.Log("CNMON_NATIVE_MSIX_ALIAS package-identity=verified ordinary-token=medium expected-sid=verified version=exact")
	t.Logf("CNMON_NATIVE_MSIX_CHILD pid=%d package-family=verified", childPID)
}

func TestNativeMSIXResolvedCommandIdentity(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" {
		t.Skip("installed MSIX qualification is an explicit hosted Windows gate")
	}
	mode := os.Getenv("CNMON_MSIX_RESOLUTION_MODE")
	if mode != "packaged" && mode != "portable" && mode != "disabled" {
		t.Fatalf("command-resolution mode is not configured")
	}
	for _, spelling := range []string{"cn-mon.exe", "cn-mon"} {
		command := exec.Command(spelling, "127.0.0.1", "--every", "10s", "--format", "json",
			"--history-file", filepath.Join(t.TempDir(), "history.db"), "--log-file", filepath.Join(t.TempDir(), "cn.log"))
		command.Env = os.Environ()
		if err := command.Start(); err != nil {
			if mode == "disabled" {
				continue
			}
			t.Fatalf("resolve %s from process PATH: %v", spelling, err)
		}
		if mode == "disabled" {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("%s resolved even though the app-alias PATH directory was removed", spelling)
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("open %s PATH-resolved process for package identity", spelling)
		}
		family, identityErr := nativeMSIXProcessFamilyName(process)
		_ = windows.CloseHandle(process)
		if err := command.Process.Kill(); err != nil {
			_ = command.Wait()
			t.Fatalf("stop %s PATH-resolved identity probe", spelling)
		}
		_ = command.Wait()
		switch mode {
		case "packaged":
			if identityErr != nil || family != os.Getenv("CNMON_MSIX_EXPECTED_PFN") {
				t.Fatalf("%s from PATH did not resolve to the installed package alias", spelling)
			}
		case "portable":
			if identityErr == nil {
				t.Fatalf("portable PATH conflict for %s unexpectedly acquired package identity", spelling)
			}
		}
	}
	switch mode {
	case "packaged":
		t.Log("CNMON_NATIVE_MSIX_PATH packaged=identity-verified spellings=cn-mon.exe,cn-mon")
	case "portable":
		t.Log("CNMON_NATIVE_MSIX_PATH portable-conflict=selected packaged-identity=absent spellings=cn-mon.exe,cn-mon")
	case "disabled":
		t.Log("CNMON_NATIVE_MSIX_PATH disabled=command-unresolved spellings=cn-mon.exe,cn-mon")
	}
}

func TestNativeMSIXOrdinaryICMPEcho(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" {
		t.Skip("installed MSIX qualification is an explicit hosted Windows gate")
	}
	alias, err := nativeMSIXInstalledAlias()
	if err != nil {
		t.Fatalf("select installed MSIX alias: %v", err)
	}
	if err := nativeMSIXRequireOrdinaryToken(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	database := filepath.Join(root, "history.db")
	logPath := filepath.Join(root, "cn.log")
	code, stdout, stderr := nativeMSIXRun(t, alias, root, nil,
		"127.0.0.1", "::1", "--history-file", database, "--log-file", logPath, "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed ordinary-user IPv4/IPv6 echo exit=%d stderr=%q", code, stderr)
	}
	var report struct {
		Latest []struct {
			Host   string `json:"host"`
			Result string `json:"result"`
		} `json:"latest"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil {
		t.Fatalf("decode installed alias probe report: %v", err)
	}
	if len(report.Latest) != 2 || report.Latest[0].Result != "OK" || report.Latest[1].Result != "OK" {
		t.Fatalf("ordinary-user loopback probes were not both successful: %+v", report.Latest)
	}
	for _, path := range []string{database, logPath} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("installed alias did not write expected external test file: %v", err)
		}
	}
	t.Log("CNMON_NATIVE_MSIX_ORDINARY icmp_ipv4=OK icmp_ipv6=OK history=external log=external profile=preserved")
}

func TestNativeMSIXLoopbackTCP(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" {
		t.Skip("installed MSIX qualification is an explicit hosted Windows gate")
	}
	alias, err := nativeMSIXInstalledAlias()
	if err != nil {
		t.Fatal("select installed alias for TCP qualification")
	}
	if err := nativeMSIXRequireOrdinaryToken(); err != nil {
		t.Fatal("TCP qualification is not running as the ordinary user")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open loopback TCP fixture: %v", err)
	}
	openPort := listener.Addr().(*net.TCPAddr).Port
	closedProbe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = listener.Close()
		t.Fatalf("select refused loopback TCP fixture: %v", err)
	}
	closedPort := closedProbe.Addr().(*net.TCPAddr).Port
	if err := closedProbe.Close(); err != nil {
		_ = listener.Close()
		t.Fatalf("close refused loopback TCP fixture: %v", err)
	}
	defer listener.Close()
	root := t.TempDir()
	database := filepath.Join(root, "history.db")
	logPath := filepath.Join(root, "cn.log")
	code, stdout, stderr := nativeMSIXRun(t, alias, root, nil,
		"127.0.0.1", "--tcp", fmt.Sprintf("%d,%d", openPort, closedPort),
		"--history-file", database, "--log-file", logPath, "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed loopback TCP command exit=%d stderr=%q", code, stderr)
	}
	var report struct {
		Latest []map[string]json.RawMessage `json:"latest"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil || len(report.Latest) != 1 {
		t.Fatal("installed loopback TCP command returned an invalid result")
	}
	var openState, closedState string
	for field, value := range map[string]*string{
		fmt.Sprintf("tcp_%d", openPort):   &openState,
		fmt.Sprintf("tcp_%d", closedPort): &closedState,
	} {
		if err := json.Unmarshal(report.Latest[0][field], value); err != nil {
			t.Fatal("installed loopback TCP result omitted a requested state")
		}
	}
	if openState != "open" || closedState != "closed" {
		t.Fatalf("installed TCP states open=%q closed=%q", openState, closedState)
	}
	t.Log("CNMON_NATIVE_MSIX_ORDINARY tcp_open=open tcp_refused=closed")
}

func TestNativeMSIXFixtureAdjacentConfig(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" || os.Getenv("CNMON_MSIX_FIXTURE_CONFIG") != "1" {
		t.Skip("installed fixture configuration is an explicit hosted Windows gate")
	}
	alias, err := nativeMSIXInstalledAlias()
	if err != nil {
		t.Fatal("select installed fixture alias")
	}
	if err := nativeMSIXRequireOrdinaryToken(); err != nil {
		t.Fatal("fixture configuration is not running as the ordinary user")
	}
	history := os.Getenv("CNMON_MSIX_FIXTURE_HISTORY")
	logPath := os.Getenv("CNMON_MSIX_FIXTURE_LOG")
	if history == "" || logPath == "" {
		t.Fatal("fixture configuration output paths are not configured")
	}
	root := t.TempDir()
	code, stdout, stderr := nativeMSIXRun(t, alias, root, nil, "127.0.0.1", "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed fixture config exit=%d stderr=%q", code, stderr)
	}
	nativeMSIXRequireLoopbackJSON(t, stdout)
	for _, path := range []string{history, logPath} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("adjacent fixture configuration did not create an external file: %v", err)
		}
	}
	t.Log("CNMON_NATIVE_MSIX_CONFIG adjacent-fixture=selected history=external log=external")
}

func TestNativeMSIXRealProfileDefaults(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" || os.Getenv("CNMON_MSIX_PROFILE_DEFAULTS") != "1" {
		t.Skip("real-profile defaults are an explicit hosted Windows gate")
	}
	alias, err := nativeMSIXInstalledAlias()
	if err != nil {
		t.Fatal("select installed profile alias")
	}
	if err := nativeMSIXRequireOrdinaryToken(); err != nil {
		t.Fatal("profile defaults are not running as the ordinary user")
	}
	profile := os.Getenv("USERPROFILE")
	if profile == "" || !filepath.IsAbs(profile) {
		t.Fatal("real user profile is unavailable")
	}
	history := filepath.Join(profile, ".cn-ping-history.db")
	logPath := filepath.Join(profile, "cn.log")
	for _, path := range []string{history, logPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("real-profile default output already exists or cannot be inspected: %v", err)
		}
	}
	code, stdout, stderr := nativeMSIXRun(t, alias, profile, nil, "127.0.0.1", "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed profile-default command exit=%d stderr=%q", code, stderr)
	}
	nativeMSIXRequireLoopbackJSON(t, stdout)
	for _, path := range []string{history, logPath} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("installed command did not write the real-profile default: %v", err)
		}
	}
	t.Log("CNMON_NATIVE_MSIX_PROFILE default-history=real-profile default-log=real-profile")
}

func TestNativeMSIXRealProfileLayers(t *testing.T) {
	if os.Getenv("CNMON_MSIX_TEST") != "1" || os.Getenv("CNMON_MSIX_PROFILE_LAYERS") != "1" {
		t.Skip("real-profile configuration layers are an explicit hosted Windows gate")
	}
	alias, err := nativeMSIXInstalledAlias()
	if err != nil {
		t.Fatal("select installed profile alias")
	}
	if err := nativeMSIXRequireOrdinaryToken(); err != nil {
		t.Fatal("profile configuration is not running as the ordinary user")
	}
	profile := os.Getenv("USERPROFILE")
	if profile == "" || !filepath.IsAbs(profile) {
		t.Fatal("real user profile is unavailable")
	}
	profileConfig := filepath.Join(profile, ".cn")
	profileHistory := os.Getenv("CNMON_MSIX_PROFILE_HISTORY")
	profileLog := os.Getenv("CNMON_MSIX_PROFILE_LOG")
	if profileHistory == "" || profileLog == "" {
		t.Fatal("profile configuration output paths are not configured")
	}
	before, err := os.ReadFile(profileConfig)
	if err != nil {
		t.Fatal("read initialized real-profile configuration")
	}
	root := t.TempDir()
	code, stdout, stderr := nativeMSIXRun(t, alias, root, nil, "127.0.0.1", "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed profile config exit=%d stderr=%q", code, stderr)
	}
	nativeMSIXRequireLoopbackJSON(t, stdout)
	for _, path := range []string{profileHistory, profileLog} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("real-profile configuration did not select an external file: %v", err)
		}
	}

	configPath := filepath.Join(root, "explicit.cn")
	configHistory := filepath.Join(root, "explicit-history.db")
	configLog := filepath.Join(root, "explicit.log")
	config := "[ping]\nhistory_file = explicit-history.db\n[logging]\nlogfile = explicit.log\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal("write explicit configuration fixture")
	}
	code, stdout, stderr = nativeMSIXRun(t, alias, root, nil, "--config", configPath, "127.0.0.1", "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed explicit-config command exit=%d stderr=%q", code, stderr)
	}
	nativeMSIXRequireLoopbackJSON(t, stdout)
	for _, path := range []string{configHistory, configLog} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("explicit configuration did not select a caller-relative file: %v", err)
		}
	}

	localAppData := os.Getenv("LOCALAPPDATA")
	cliHistory := os.Getenv("CNMON_MSIX_CLI_HISTORY")
	cliLog := os.Getenv("CNMON_MSIX_CLI_LOG")
	if localAppData == "" || !filepath.IsAbs(localAppData) || cliHistory == "" || cliLog == "" {
		t.Fatal("CLI override output paths are not configured for the ordinary user's profile")
	}
	expectedCLIHistory := filepath.Join(localAppData, "msix-cli-history.db")
	expectedCILog := filepath.Join(localAppData, "msix-cli.log")
	if !strings.EqualFold(filepath.Clean(cliHistory), filepath.Clean(expectedCLIHistory)) ||
		!strings.EqualFold(filepath.Clean(cliLog), filepath.Clean(expectedCILog)) {
		t.Fatal("CLI override output paths do not match the configured ordinary-user profile paths")
	}
	for _, path := range []string{cliHistory, cliLog} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("CLI override output already exists or cannot be inspected: %v", err)
		}
	}
	code, stdout, stderr = nativeMSIXRun(t, alias, root, nil, "--config", configPath, "--history-file", cliHistory, "--log-file", cliLog, "127.0.0.1", "--format", "json")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("installed CLI-override command exit=%d stderr=%q", code, stderr)
	}
	nativeMSIXRequireLoopbackJSON(t, stdout)
	for _, path := range []string{cliHistory, cliLog} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("CLI override did not select an external file: %v", err)
		}
	}
	after, err := os.ReadFile(profileConfig)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("installed command changed the caller's real-profile configuration")
	}
	t.Log("CNMON_NATIVE_MSIX_CONFIG real-profile=selected explicit-config=selected cli-overrides=selected relative-paths=resolved")
}

func nativeMSIXRequireLoopbackJSON(t *testing.T, stdout []byte) {
	t.Helper()
	var report struct {
		Latest []struct {
			Result string `json:"result"`
		} `json:"latest"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil || len(report.Latest) != 1 || report.Latest[0].Result != "OK" {
		t.Fatal("installed alias did not return one successful loopback result")
	}
}

func nativeMSIXReject(t *testing.T, reason string) {
	t.Helper()
	t.Logf("CNMON_NATIVE_MSIX_REJECTED reason=%s", reason)
	t.Fatalf("installed MSIX prerequisite rejected: %s", reason)
}

func nativeMSIXInstalledAlias() (string, error) {
	configured := os.Getenv("CNMON_NATIVE_COMMAND_BINARY")
	localAppData := os.Getenv("LOCALAPPDATA")
	if configured == "" || localAppData == "" {
		return "", fmt.Errorf("installed alias path is not configured")
	}
	configuredPath, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("resolve installed alias path")
	}
	expectedPath, err := filepath.Abs(filepath.Join(localAppData, "Microsoft", "WindowsApps", "cn-mon.exe"))
	if err != nil || !strings.EqualFold(filepath.Clean(configuredPath), filepath.Clean(expectedPath)) {
		return "", fmt.Errorf("configured command is not the current user's installed alias")
	}
	configuredPathUTF16, err := windows.UTF16PtrFromString(configuredPath)
	if err != nil {
		return "", fmt.Errorf("resolve installed app execution alias")
	}
	attributes, err := windows.GetFileAttributes(configuredPathUTF16)
	if err != nil || attributes&nativeMSIXReparsePoint == 0 {
		return "", fmt.Errorf("configured command is not an installed app execution alias")
	}
	return configuredPath, nil
}

func nativeMSIXAliasHasPackageIdentity(aliasPath string) (uint32, error) {
	expectedFamily := os.Getenv("CNMON_MSIX_EXPECTED_PFN")
	if expectedFamily == "" {
		return 0, fmt.Errorf("expected package family is not configured")
	}
	root, err := os.MkdirTemp("", "cn-mon-msix-identity-")
	if err != nil {
		return 0, fmt.Errorf("create private identity-probe directory")
	}
	defer os.RemoveAll(root)
	command := exec.Command(aliasPath, "127.0.0.1", "--every", "10s", "--format", "json",
		"--history-file", filepath.Join(root, "history.db"), "--log-file", filepath.Join(root, "cn.log"))
	command.Env = os.Environ()
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start installed alias")
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return 0, fmt.Errorf("open launched process for package identity")
	}
	family, identityErr := nativeMSIXProcessFamilyName(processHandle)
	_ = windows.CloseHandle(processHandle)
	if err := command.Process.Kill(); err != nil {
		_ = command.Wait()
		return 0, fmt.Errorf("stop installed alias identity probe")
	}
	_ = command.Wait()
	if identityErr != nil {
		return 0, identityErr
	}
	if family != expectedFamily {
		return 0, fmt.Errorf("launched process has a different package family")
	}
	return uint32(command.Process.Pid), nil
}

func nativeMSIXProcessFamilyName(process windows.Handle) (string, error) {
	var length uint32
	result, _, _ := nativeMSIXGetPackageFamilyName.Call(
		uintptr(process), uintptr(unsafe.Pointer(&length)), 0,
	)
	if result != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || length < 2 {
		return "", fmt.Errorf("launched process has no readable package identity")
	}
	buffer := make([]uint16, length)
	result, _, _ = nativeMSIXGetPackageFamilyName.Call(
		uintptr(process), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0])),
	)
	if result != 0 {
		return "", fmt.Errorf("launched process has no readable package identity")
	}
	return windows.UTF16ToString(buffer), nil
}

func nativeMSIXRequireProcessPackageIdentity(pid uint32) error {
	expectedFamily := os.Getenv("CNMON_MSIX_EXPECTED_PFN")
	if expectedFamily == "" {
		return fmt.Errorf("expected package family is not configured")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("open launched process for package identity")
	}
	defer windows.CloseHandle(process)
	family, err := nativeMSIXProcessFamilyName(process)
	if err != nil {
		return err
	}
	if family != expectedFamily {
		return fmt.Errorf("launched process has a different package family")
	}
	return nil
}

func nativeMSIXRequireOrdinaryToken() error {
	expectedSID := os.Getenv("CNMON_MSIX_EXPECTED_SID")
	if expectedSID == "" {
		return fmt.Errorf("ordinary-user SID is not configured")
	}
	facts, err := currentWindowsTokenFacts()
	if err != nil {
		return fmt.Errorf("query installed-test token: %w", err)
	}
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		return fmt.Errorf("query installed-test token groups: %w", err)
	}
	adminMember, err := nativeMSIXHasAdministratorsMembership(groups.AllGroups())
	if err != nil {
		return err
	}
	if !strings.EqualFold(facts.sid, expectedSID) || facts.elevated || facts.adminEnabled || adminMember {
		return fmt.Errorf("installed test is not running as the expected ordinary user")
	}
	token := windows.GetCurrentProcessToken()
	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size)
	if size < uint32(unsafe.Sizeof(windows.SIDAndAttributes{})) {
		return fmt.Errorf("ordinary-user integrity level is unavailable")
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buffer[0], size, &size); err != nil {
		return fmt.Errorf("query ordinary-user integrity level: %w", err)
	}
	label := (*windows.SIDAndAttributes)(unsafe.Pointer(&buffer[0]))
	if label.Sid == nil || label.Sid.SubAuthorityCount() == 0 {
		return fmt.Errorf("ordinary-user integrity level is unavailable")
	}
	rid := label.Sid.SubAuthority(uint32(label.Sid.SubAuthorityCount() - 1))
	if rid != nativeMSIXMediumIntegrityRID {
		return fmt.Errorf("installed test does not have medium integrity")
	}
	return nil
}

func nativeMSIXHasAdministratorsMembership(groups []windows.SIDAndAttributes) (bool, error) {
	for _, group := range groups {
		if group.Sid == nil {
			return false, fmt.Errorf("ordinary-user token contains a group with no readable SID")
		}
		groupSID := group.Sid.String()
		if groupSID == "" {
			return false, fmt.Errorf("ordinary-user token contains a group with no readable SID")
		}
		if strings.EqualFold(groupSID, nativeMSIXAdministratorsSID) {
			return true, nil
		}
	}
	return false, nil
}

func nativeMSIXCommandEnvironment(t *testing.T, directory, history, logPath string) []string {
	t.Helper()
	var err error
	directory, err = filepath.Abs(directory)
	if err != nil {
		t.Fatalf("resolve MSIX console working directory: %v", err)
	}
	history, err = filepath.Abs(history)
	if err != nil {
		t.Fatalf("resolve MSIX console history path: %v", err)
	}
	logPath, err = filepath.Abs(logPath)
	if err != nil {
		t.Fatalf("resolve MSIX console log path: %v", err)
	}
	environment := os.Environ()
	environment = replaceNativeCommandEnv(environment, "HOME", directory)
	environment = replaceNativeCommandEnv(environment, "CNMON_NATIVE_HISTORY", history)
	environment = replaceNativeCommandEnv(environment, "CNMON_NATIVE_LOG", logPath)
	return environment
}

func TestNativeMSIXHasAdministratorsMembership(t *testing.T) {
	adminSID, err := windows.StringToSid(nativeMSIXAdministratorsSID)
	if err != nil {
		t.Fatal("parse Administrators SID")
	}
	userSID, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal("parse fixture user SID")
	}
	tests := []struct {
		name   string
		groups []windows.SIDAndAttributes
		want   bool
	}{
		{name: "deny-only administrator", groups: []windows.SIDAndAttributes{{Sid: adminSID, Attributes: windows.SE_GROUP_USE_FOR_DENY_ONLY}}, want: true},
		{name: "enabled administrator", groups: []windows.SIDAndAttributes{{Sid: adminSID, Attributes: windows.SE_GROUP_ENABLED}}, want: true},
		{name: "ordinary group", groups: []windows.SIDAndAttributes{{Sid: userSID, Attributes: windows.SE_GROUP_ENABLED}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := nativeMSIXHasAdministratorsMembership(test.groups)
			if err != nil || got != test.want {
				t.Fatalf("administrator membership=%t err=%v, want %t", got, err, test.want)
			}
		})
	}
	if _, err := nativeMSIXHasAdministratorsMembership([]windows.SIDAndAttributes{{}}); err == nil {
		t.Fatal("unreadable token group SID was accepted")
	}
}

func nativeMSIXCommandContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func nativeMSIXRun(t *testing.T, executable, directory string, input []byte, args ...string) (int, []byte, []byte) {
	t.Helper()
	ctx, cancel := nativeMSIXCommandContext(t)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.Env = os.Environ()
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.Bytes(), stderr.Bytes()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), stdout.Bytes(), stderr.Bytes()
	}
	t.Fatalf("run installed alias: %v", err)
	return -1, nil, nil
}
