// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const releaseVersion = "0.2.0"

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var markdownLink = regexp.MustCompile(`\]\(([^)]+)\)`)
var expectedModules = map[string]string{
	"github.com/google/uuid":                   "v1.6.0",
	"github.com/ncruces/go-sqlite3":            "v0.35.6",
	"github.com/ncruces/go-sqlite3-wasm/v6":    "v6.3.35304",
	"github.com/ncruces/julianday":             "v1.0.0",
	"github.com/prometheus-community/pro-bing": "v0.9.2-0.20261008151844-bcbba8f702ab",
	"github.com/rivo/uniseg":                   "v0.4.7",
	"golang.org/x/net":                         "v0.56.0",
	"golang.org/x/sync":                        "v0.23.0",
	"golang.org/x/sys":                         "v0.48.0",
	"golang.org/x/term":                        "v0.44.0",
}

type targetSpec struct {
	name    string
	goos    string
	binary  string
	archive string
	zip     bool
}

var targets = map[string]targetSpec{
	"linux_amd64":   {name: "linux_amd64", goos: "linux", binary: "cn-mon", archive: "cn-mon_0.2.0_linux_amd64.tar.gz"},
	"windows_amd64": {name: "windows_amd64", goos: "windows", binary: "cn-mon.exe", archive: "cn-mon_0.2.0_windows_amd64.zip", zip: true},
}

type receipt struct {
	Version       string `json:"version"`
	Revision      string `json:"revision"`
	Target        string `json:"target"`
	ArchivePath   string `json:"archive_path"`
	ArchiveSHA256 string `json:"archive_sha256"`
	BinaryPath    string `json:"binary_path"`
	BinarySHA256  string `json:"binary_sha256"`
}

type artifactMember struct {
	name string
	data []byte
	mode os.FileMode
}

type identity struct {
	time time.Time
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "release-artifact:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected pack or verify")
	}
	var result receipt
	var err error
	switch args[0] {
	case "pack":
		result, err = runPack(args[1:])
	case "verify":
		result, err = runVerify(args[1:])
	default:
		return fmt.Errorf("unknown operation %q", args[0])
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func runPack(args []string) (receipt, error) {
	flags := flag.NewFlagSet("pack", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	binary := flags.String("binary", "", "absolute built executable")
	sourceRoot := flags.String("source-root", "", "exact source checkout")
	outDir := flags.String("out-dir", "", "archive output directory")
	version := flags.String("version", "", "expected release version")
	revision := flags.String("revision", "", "full source revision")
	commitUnix := flags.String("commit-unix", "", "selected commit time in Unix seconds")
	targetName := flags.String("target", "", "linux_amd64 or windows_amd64")
	if err := flags.Parse(args); err != nil {
		return receipt{}, err
	}
	if flags.NArg() != 0 {
		return receipt{}, errors.New("unexpected positional arguments")
	}
	spec, err := validateInputs(*version, *revision, *targetName)
	if err != nil {
		return receipt{}, err
	}
	if !filepath.IsAbs(*binary) {
		return receipt{}, errors.New("binary path must be absolute")
	}
	if *sourceRoot == "" || *outDir == "" || *commitUnix == "" {
		return receipt{}, errors.New("source-root, out-dir, and commit-unix are required")
	}
	commit, err := strconvInt64(*commitUnix)
	if err != nil {
		return receipt{}, fmt.Errorf("invalid commit-unix: %w", err)
	}
	root, err := filepath.Abs(*sourceRoot)
	if err != nil {
		return receipt{}, fmt.Errorf("resolve source root: %w", err)
	}
	binaryData, err := readRegularFile(*binary)
	if err != nil {
		return receipt{}, fmt.Errorf("read binary: %w", err)
	}
	identity, err := readIdentityBytes(binaryData, root, spec, *revision)
	if err != nil {
		return receipt{}, err
	}
	if identity.time.Unix() != commit {
		return receipt{}, fmt.Errorf("commit-unix %d disagrees with binary vcs.time %d", commit, identity.time.Unix())
	}
	members, err := sourceMembers(root, spec, *revision, binaryData)
	if err != nil {
		return receipt{}, err
	}
	outDirAbs, err := filepath.Abs(*outDir)
	if err != nil {
		return receipt{}, fmt.Errorf("resolve output directory: %w", err)
	}
	archivePath := filepath.Join(outDirAbs, spec.archive)
	if err := os.MkdirAll(outDirAbs, 0755); err != nil {
		return receipt{}, fmt.Errorf("create output directory: %w", err)
	}
	archiveTime := normalizedTime(identity.time, spec)
	if err := writeArchive(archivePath, members, spec, archiveTime); err != nil {
		return receipt{}, err
	}
	archiveHash, err := hashFile(archivePath)
	if err != nil {
		return receipt{}, err
	}
	binaryHash := hashBytes(membersBinary(members, spec))
	return receipt{Version: *version, Revision: *revision, Target: spec.name, ArchivePath: archivePath, ArchiveSHA256: archiveHash, BinaryPath: *binary, BinarySHA256: binaryHash}, nil
}

func runVerify(args []string) (receipt, error) {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "archive file")
	wantSHA := flags.String("sha256", "", "expected archive SHA-256")
	sourceRoot := flags.String("source-root", "", "exact source checkout")
	extractDir := flags.String("extract-dir", "", "fresh extraction directory")
	version := flags.String("version", "", "expected release version")
	revision := flags.String("revision", "", "full source revision")
	targetName := flags.String("target", "", "linux_amd64 or windows_amd64")
	if err := flags.Parse(args); err != nil {
		return receipt{}, err
	}
	if flags.NArg() != 0 {
		return receipt{}, errors.New("unexpected positional arguments")
	}
	spec, err := validateInputs(*version, *revision, *targetName)
	if err != nil {
		return receipt{}, err
	}
	if *archive == "" || !filepath.IsAbs(*archive) || *sourceRoot == "" || *extractDir == "" || !filepath.IsAbs(*extractDir) || *wantSHA == "" {
		return receipt{}, errors.New("archive (absolute), sha256, source-root, and extract-dir are required")
	}
	if !digestPattern.MatchString(*wantSHA) {
		return receipt{}, errors.New("sha256 must be 64 lowercase hexadecimal characters")
	}
	if filepath.Base(*archive) != spec.archive {
		return receipt{}, fmt.Errorf("archive filename must be %s", spec.archive)
	}
	gotSHA, err := hashFile(*archive)
	if err != nil {
		return receipt{}, fmt.Errorf("hash archive: %w", err)
	}
	if gotSHA != *wantSHA {
		return receipt{}, errors.New("archive SHA-256 does not match")
	}
	root, err := filepath.Abs(*sourceRoot)
	if err != nil {
		return receipt{}, fmt.Errorf("resolve source root: %w", err)
	}
	expectedDocs, err := sourceDocuments(root, *revision)
	if err != nil {
		return receipt{}, err
	}
	members, archiveTime, err := readArchive(*archive, spec)
	if err != nil {
		return receipt{}, err
	}
	if err := compareDocuments(members, spec, expectedDocs); err != nil {
		return receipt{}, err
	}
	binary := members[filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary))]
	identity, err := readIdentityBytes(binary.data, root, spec, *revision)
	if err != nil {
		return receipt{}, err
	}
	if !archiveTime.Equal(normalizedTime(identity.time, spec)) {
		return receipt{}, fmt.Errorf("archive timestamp %s disagrees with binary vcs.time %s", archiveTime.Format(time.RFC3339), identity.time.Format(time.RFC3339))
	}
	if err := extractArchive(*extractDir, members, spec, archiveTime); err != nil {
		return receipt{}, err
	}
	archiveAbs, err := filepath.Abs(*archive)
	if err != nil {
		return receipt{}, err
	}
	binaryPath := filepath.Join(*extractDir, archiveTopDir(spec), spec.binary)
	return receipt{Version: *version, Revision: *revision, Target: spec.name, ArchivePath: archiveAbs, ArchiveSHA256: gotSHA, BinaryPath: binaryPath, BinarySHA256: hashBytes(binary.data)}, nil
}

func validateInputs(version, revision, targetName string) (targetSpec, error) {
	if version != releaseVersion {
		return targetSpec{}, fmt.Errorf("version must be %s", releaseVersion)
	}
	if !revisionPattern.MatchString(revision) {
		return targetSpec{}, errors.New("revision must be 40 lowercase hexadecimal characters")
	}
	spec, ok := targets[targetName]
	if !ok {
		return targetSpec{}, errors.New("target must be linux_amd64 or windows_amd64")
	}
	return spec, nil
}

func strconvInt64(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("empty value")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("must be Unix timestamp seconds")
		}
	}
	return strconv.ParseInt(value, 10, 64)
}
