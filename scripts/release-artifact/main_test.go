// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestPackRejectsInvalidRevisionBeforeFileAccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"pack",
		"-binary", "/does/not/exist/cn-mon",
		"-source-root", "/does/not/exist",
		"-out-dir", "/does/not/exist",
		"-version", "0.3.0",
		"-revision", "BAD",
		"-commit-unix", "0",
		"-target", "linux_amd64",
	}
	err := run(args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("invalid revision error = %v, want a revision validation error", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("invalid request wrote stdout: %q", stdout.String())
	}
}

func TestReleaseVersionIsStrictly030(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.2.0", "0.3.1", "v0.3.0"} {
		t.Run(version, func(t *testing.T) {
			_, err := validateInputs(version, strings.Repeat("a", 40), "linux_amd64")
			if err == nil {
				t.Fatalf("obsolete or mixed version %q accepted", version)
			}
		})
	}
	if _, err := validateInputs("0.3.0", strings.Repeat("a", 40), "linux_amd64"); err != nil {
		t.Fatalf("current release rejected: %v", err)
	}
}

func TestReleaseArchiveRoundTripIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "README.md"), []byte("See [spec](docs/APPLICATION_SPEC.md#runtime) and [plan](docs/DEVELOPMENT_PLAN.md#release).\nRead [license](LICENSE).\n"))
	writeFixture(t, filepath.Join(root, "LICENSE"), []byte("license bytes\r\n"))
	writeFixture(t, filepath.Join(root, "THIRD_PARTY_NOTICES.md"), []byte("notices bytes\r\n"))
	revision := "0123456789abcdef0123456789abcdef01234567"
	modTime := time.Date(2026, 10, 11, 1, 7, 26, 0, time.UTC)
	for _, targetName := range []string{"linux_amd64", "windows_amd64"} {
		t.Run(targetName, func(t *testing.T) {
			spec := targets[targetName]
			docs, err := sourceDocuments(root, revision)
			if err != nil {
				t.Fatal(err)
			}
			members := fixtureMembers(spec, docs)
			first := filepath.Join(t.TempDir(), spec.archive)
			second := filepath.Join(t.TempDir(), spec.archive)
			if err := writeArchive(first, members, spec, normalizedTime(modTime, spec)); err != nil {
				t.Fatal(err)
			}
			if err := writeArchive(second, members, spec, normalizedTime(modTime, spec)); err != nil {
				t.Fatal(err)
			}
			firstBytes, err := os.ReadFile(first)
			if err != nil {
				t.Fatal(err)
			}
			secondBytes, err := os.ReadFile(second)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatal("identical inputs produced different archive bytes")
			}
			got, gotTime, err := readArchive(first, spec)
			if err != nil {
				t.Fatal(err)
			}
			if !gotTime.Equal(normalizedTime(modTime, spec)) {
				t.Fatalf("archive time %s, want %s", gotTime, normalizedTime(modTime, spec))
			}
			if err := compareDocuments(got, spec, docs); err != nil {
				t.Fatal(err)
			}
			if got[filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary))].mode.Perm() != binaryMode(spec) {
				t.Fatal("binary mode did not survive archive round trip")
			}
			if targetName == "linux_amd64" {
				readme := string(got[archiveTopDir(spec)+"/README.md"].data)
				if !strings.Contains(readme, "blob/"+revision+"/docs/APPLICATION_SPEC.md#runtime") || !strings.Contains(readme, "blob/"+revision+"/docs/DEVELOPMENT_PLAN.md#release") {
					t.Fatalf("release README links were not revision-pinned: %q", readme)
				}
			}
			extract := filepath.Join(t.TempDir(), "fresh extract with spaces")
			if err := os.Mkdir(extract, 0755); err != nil {
				t.Fatal(err)
			}
			if err := extractArchive(extract, got, spec, gotTime); err != nil {
				t.Fatal(err)
			}
			extracted, err := os.ReadFile(filepath.Join(extract, archiveTopDir(spec), spec.binary))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(extracted, []byte("fixture binary bytes")) {
				t.Fatalf("extracted binary = %q", extracted)
			}
			if err := extractArchive(extract, got, spec, gotTime); err == nil || !strings.Contains(err.Error(), "empty") {
				t.Fatalf("occupied extraction result = %v, want empty-destination error", err)
			}
		})
	}
}

func TestReleaseREADMERejectsUnknownLocalLinks(t *testing.T) {
	revision := "0123456789abcdef0123456789abcdef01234567"
	got, err := releaseREADME([]byte("[known](docs/APPLICATION_SPEC.md#x) [bad](missing.md)"), revision)
	if err == nil || !strings.Contains(err.Error(), "unresolved local README link") {
		t.Fatalf("unknown local link error = %v", err)
	}
	if got != nil {
		t.Fatalf("failed README conversion returned bytes %q", got)
	}
}

func TestValidateBuildInfoRequiresCleanExactIdentityAndModules(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), []byte(fixtureGoMod))
	info := validFixtureBuildInfo()
	if err := validateBuildInfo(info, root, targets["linux_amd64"], "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	for name, mutate := range map[string]func(*debug.BuildInfo){
		"dirty":               func(info *debug.BuildInfo) { setBuildSetting(info, "vcs.modified", "true") },
		"wrong target":        func(info *debug.BuildInfo) { setBuildSetting(info, "GOOS", "windows") },
		"false trimpath":      func(info *debug.BuildInfo) { setBuildSetting(info, "-trimpath", "false") },
		"missing trimpath":    func(info *debug.BuildInfo) { removeBuildSetting(info, "-trimpath") },
		"wrong revision":      func(info *debug.BuildInfo) { setBuildSetting(info, "vcs.revision", strings.Repeat("f", 40)) },
		"missing module":      func(info *debug.BuildInfo) { info.Deps = info.Deps[:len(info.Deps)-1] },
		"wrong local replace": func(info *debug.BuildInfo) { info.Deps[4].Replace.Path = "github.com/prometheus-community/pro-bing" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validFixtureBuildInfo()
			mutate(candidate)
			if err := validateBuildInfo(candidate, root, targets["linux_amd64"], "0123456789abcdef0123456789abcdef01234567"); err == nil {
				t.Fatal("invalid build identity was accepted")
			}
		})
	}
}

func TestVerifyRejectsBadDigestBeforeExtraction(t *testing.T) {
	archive := filepath.Join(t.TempDir(), targets["linux_amd64"].archive)
	writeFixture(t, archive, []byte("not an archive"))
	destination := filepath.Join(t.TempDir(), "extract")
	var stdout, stderr bytes.Buffer
	args := []string{"verify", "-archive", archive, "-sha256", strings.Repeat("0", 64), "-source-root", t.TempDir(), "-extract-dir", destination, "-version", releaseVersion, "-revision", strings.Repeat("a", 40), "-target", "linux_amd64"}
	err := run(args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("bad digest error = %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("bad archive created extraction destination: %v", err)
	}
}

func TestReadArchiveRejectsUnsafeDuplicateAndSymlinkMembers(t *testing.T) {
	spec := targets["windows_amd64"]
	top := archiveTopDir(spec)
	base := []rawArchiveMember{
		{name: top + "/LICENSE", mode: 0644, data: []byte("license")},
		{name: top + "/README.md", mode: 0644, data: []byte("readme")},
		{name: top + "/THIRD_PARTY_NOTICES.md", mode: 0644, data: []byte("notices")},
		{name: top + "/" + spec.binary, mode: 0644, data: []byte("binary")},
	}
	for _, test := range []struct {
		name    string
		mutate  func([]rawArchiveMember) []rawArchiveMember
		wantErr string
	}{
		{name: "traversal", mutate: func(entries []rawArchiveMember) []rawArchiveMember {
			entries[3].name = top + "/../escape"
			return entries
		}, wantErr: "unsafe archive member path"},
		{name: "duplicate", mutate: func(entries []rawArchiveMember) []rawArchiveMember {
			entries[2].name = entries[1].name
			return entries
		}, wantErr: "duplicate archive member"},
		{name: "symlink", mutate: func(entries []rawArchiveMember) []rawArchiveMember {
			entries[3].mode = os.ModeSymlink | 0777
			return entries
		}, wantErr: "not a regular file"},
		{name: "extra metadata", mutate: func(entries []rawArchiveMember) []rawArchiveMember {
			entries[3].extra = []byte{0xfe, 0xca, 0x02, 0x00, 0x01, 0x02}
			return entries
		}, wantErr: "unexpected zip metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := append([]rawArchiveMember(nil), base...)
			entries = test.mutate(entries)
			filename := filepath.Join(t.TempDir(), spec.archive)
			writeRawZIP(t, filename, entries)
			_, _, err := readArchive(filename, spec)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("malformed archive error = %v, want substring %q", err, test.wantErr)
			}
		})
	}

	linux := targets["linux_amd64"]
	entries := []rawArchiveMember{
		{name: archiveTopDir(linux) + "/LICENSE", mode: 0644, data: []byte("license")},
		{name: archiveTopDir(linux) + "/README.md", mode: 0644, data: []byte("readme")},
		{name: archiveTopDir(linux) + "/THIRD_PARTY_NOTICES.md", mode: 0644, data: []byte("notices")},
		{name: archiveTopDir(linux) + "/" + linux.binary, mode: 0755, data: []byte("binary")},
	}
	entries[3].name = archiveTopDir(linux) + "/../escape"
	filename := filepath.Join(t.TempDir(), linux.archive)
	writeRawTARGZ(t, filename, entries)
	if _, _, err := readArchive(filename, linux); err == nil || !strings.Contains(err.Error(), "unsafe archive member path") {
		t.Fatalf("unsafe tar.gz member error = %v", err)
	}
}

func TestReadTARGZRejectsPayloadAfterEndMarker(t *testing.T) {
	spec := targets["linux_amd64"]
	top := archiveTopDir(spec)
	entries := []rawArchiveMember{
		{name: top + "/LICENSE", mode: 0644, data: []byte("license")},
		{name: top + "/README.md", mode: 0644, data: []byte("readme")},
		{name: top + "/THIRD_PARTY_NOTICES.md", mode: 0644, data: []byte("notices")},
		{name: top + "/" + spec.binary, mode: 0755, data: []byte("binary")},
	}
	filename := filepath.Join(t.TempDir(), spec.archive)
	writeRawTARGZWithTrailing(t, filename, entries, append(bytes.Repeat([]byte{'X'}, 512), make([]byte, 1024)...))
	if _, _, err := readArchive(filename, spec); err == nil || !strings.Contains(err.Error(), "invalid trailer length") {
		t.Fatalf("tar.gz payload-after-end error = %v, want invalid trailer length", err)
	}
}

func TestReadTARGZRejectsSecondGZIPMember(t *testing.T) {
	spec := targets["linux_amd64"]
	top := archiveTopDir(spec)
	entries := []rawArchiveMember{
		{name: top + "/LICENSE", mode: 0644, data: []byte("license")},
		{name: top + "/README.md", mode: 0644, data: []byte("readme")},
		{name: top + "/THIRD_PARTY_NOTICES.md", mode: 0644, data: []byte("notices")},
		{name: top + "/" + spec.binary, mode: 0755, data: []byte("binary")},
	}
	filename := filepath.Join(t.TempDir(), spec.archive)
	writeRawTARGZ(t, filename, entries)

	var emptyMember bytes.Buffer
	emptyWriter := gzip.NewWriter(&emptyMember)
	if err := emptyWriter.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(emptyMember.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readArchive(filename, spec); err == nil {
		t.Fatal("tar.gz with a second gzip member was accepted")
	}
}

func TestReadZIPRejectsPrefixedArchive(t *testing.T) {
	spec := targets["windows_amd64"]
	canonical := filepath.Join(t.TempDir(), spec.archive)
	members := fixtureMembers(spec, map[string][]byte{
		"README.md":              []byte("readme"),
		"LICENSE":                []byte("license"),
		"THIRD_PARTY_NOTICES.md": []byte("notices"),
	})
	if err := writeArchive(canonical, members, spec, time.Date(2026, 10, 11, 1, 7, 26, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	canonicalBytes, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	for name, container := range map[string]struct{ prefix, suffix []byte }{
		"plain prefix":            {prefix: []byte("self-extracting prefix")},
		"zip-signature-shaped":    {prefix: append([]byte("PK\x03\x04"), make([]byte, 26)...)},
		"trailing container junk": {suffix: []byte("trailing data")},
	} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), spec.archive)
			containerBytes := append(append([]byte(nil), container.prefix...), canonicalBytes...)
			containerBytes = append(containerBytes, container.suffix...)
			if err := os.WriteFile(filename, containerBytes, 0644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readArchive(filename, spec); err == nil {
				t.Fatal("ZIP archive with a prefix was accepted")
			}
		})
	}
}

func TestReadTARGZRejectsNoncanonicalEncoding(t *testing.T) {
	spec := targets["linux_amd64"]
	top := archiveTopDir(spec)
	base := []rawArchiveMember{
		{name: top + "/LICENSE", mode: 0644, data: []byte("license")},
		{name: top + "/README.md", mode: 0644, data: []byte("readme")},
		{name: top + "/THIRD_PARTY_NOTICES.md", mode: 0644, data: []byte("notices")},
		{name: top + "/" + spec.binary, mode: 0755, data: []byte("binary")},
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "nonzero gzip XFL", mutate: func(t *testing.T, filename string) {
			data, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			data[8] = 2
			if err := os.WriteFile(filename, data, 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "regular member linkname", mutate: func(t *testing.T, filename string) {
			entries := append([]rawArchiveMember(nil), base...)
			entries[0].linkname = "unexpected-target"
			writeRawTARGZ(t, filename, entries)
		}},
		{name: "nonzero intermember padding", mutate: func(t *testing.T, filename string) {
			compressed, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := gzip.NewReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatal(err)
			}
			tarBytes, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			paddingOffset := 512 + len(base[0].data)
			tarBytes[paddingOffset] = 1
			var mutated bytes.Buffer
			writer := gzip.NewWriter(&mutated)
			writer.ModTime = time.Time{}
			writer.OS = 255
			if _, err := writer.Write(tarBytes); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, mutated.Bytes(), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), spec.archive)
			writeRawTARGZ(t, filename, base)
			test.mutate(t, filename)
			if _, _, err := readArchive(filename, spec); err == nil {
				t.Fatal("noncanonical tar.gz encoding was accepted")
			}
		})
	}
}

func fixtureMembers(spec targetSpec, docs map[string][]byte) []artifactMember {
	return []artifactMember{
		{name: archiveTopDir(spec) + "/" + spec.binary, data: []byte("fixture binary bytes"), mode: binaryMode(spec)},
		{name: archiveTopDir(spec) + "/README.md", data: docs["README.md"], mode: 0644},
		{name: archiveTopDir(spec) + "/LICENSE", data: docs["LICENSE"], mode: 0644},
		{name: archiveTopDir(spec) + "/THIRD_PARTY_NOTICES.md", data: docs["THIRD_PARTY_NOTICES.md"], mode: 0644},
	}
}

func writeFixture(t *testing.T, filename string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, 0644); err != nil {
		t.Fatal(err)
	}
}

type rawArchiveMember struct {
	name     string
	mode     os.FileMode
	data     []byte
	extra    []byte
	linkname string
}

func writeRawZIP(t *testing.T, filename string, entries []rawArchiveMember) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		header.Modified = time.Date(2026, 10, 11, 1, 7, 26, 0, time.UTC)
		header.Extra = entry.extra
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRawTARGZ(t *testing.T, filename string, entries []rawArchiveMember) {
	writeRawTARGZWithTrailing(t, filename, entries, nil)
}

func writeRawTARGZWithTrailing(t *testing.T, filename string, entries []rawArchiveMember, trailing []byte) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.ModTime = time.Time{}
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := byte(tar.TypeReg)
		if entry.mode&os.ModeSymlink != 0 {
			typeflag = tar.TypeSymlink
		}
		header := &tar.Header{Name: entry.name, Linkname: entry.linkname, Mode: int64(entry.mode.Perm()), Size: int64(len(entry.data)), ModTime: time.Date(2026, 10, 11, 1, 7, 26, 0, time.UTC), Typeflag: typeflag, Format: tar.FormatUSTAR, Uid: 0, Gid: 0}
		if typeflag == tar.TypeSymlink {
			header.Linkname = "target"
			header.Size = 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if len(trailing) != 0 {
		if _, err := gzipWriter.Write(trailing); err != nil {
			t.Fatal(err)
		}
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

const fixtureGoMod = `module github.com/evdanil/cn-mon

go 1.26.0

require github.com/prometheus-community/pro-bing v0.9.2-0.20261008151844-bcbba8f702ab

require (
	github.com/ncruces/go-sqlite3-wasm/v6 v6.3.35304
	github.com/ncruces/julianday v1.0.0
	golang.org/x/sync v0.23.0
)

require (
	github.com/google/uuid v1.6.0
	github.com/ncruces/go-sqlite3 v0.35.6
	github.com/rivo/uniseg v0.4.7
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.44.0
)

replace github.com/prometheus-community/pro-bing => ./third_party/pro-bing
`

func validFixtureBuildInfo() *debug.BuildInfo {
	info := &debug.BuildInfo{Path: "github.com/evdanil/cn-mon/cmd/cn-mon", Main: debug.Module{Path: "github.com/evdanil/cn-mon"}, GoVersion: "go1.27.2"}
	dependencies := []struct{ path, version string }{
		{"github.com/google/uuid", "v1.6.0"},
		{"github.com/ncruces/go-sqlite3", "v0.35.6"},
		{"github.com/ncruces/go-sqlite3-wasm/v6", "v6.3.35304"},
		{"github.com/ncruces/julianday", "v1.0.0"},
		{"github.com/prometheus-community/pro-bing", "v0.9.2-0.20261008151844-bcbba8f702ab"},
		{"github.com/rivo/uniseg", "v0.4.7"},
		{"golang.org/x/net", "v0.56.0"},
		{"golang.org/x/sync", "v0.23.0"},
		{"golang.org/x/sys", "v0.48.0"},
		{"golang.org/x/term", "v0.44.0"},
	}
	for _, dependency := range dependencies {
		module := &debug.Module{Path: dependency.path, Version: dependency.version}
		if dependency.path == "github.com/prometheus-community/pro-bing" {
			module.Replace = &debug.Module{Path: "./third_party/pro-bing"}
		}
		info.Deps = append(info.Deps, module)
	}
	info.Settings = []debug.BuildSetting{
		{Key: "GOOS", Value: "linux"},
		{Key: "GOARCH", Value: "amd64"},
		{Key: "CGO_ENABLED", Value: "0"},
		{Key: "GOAMD64", Value: "v1"},
		{Key: "-trimpath", Value: "true"},
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
		{Key: "vcs.modified", Value: "false"},
		{Key: "vcs.time", Value: "2026-10-11T01:07:26Z"},
	}
	return info
}

func setBuildSetting(info *debug.BuildInfo, key, value string) {
	for i := range info.Settings {
		if info.Settings[i].Key == key {
			info.Settings[i].Value = value
			return
		}
	}
	info.Settings = append(info.Settings, debug.BuildSetting{Key: key, Value: value})
}

func removeBuildSetting(info *debug.BuildInfo, key string) {
	for i := range info.Settings {
		if info.Settings[i].Key == key {
			info.Settings = append(info.Settings[:i], info.Settings[i+1:]...)
			return
		}
	}
}
