// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func sourceMembers(root string, spec targetSpec, revision string, binaryData []byte) ([]artifactMember, error) {
	documents, err := sourceDocuments(root, revision)
	if err != nil {
		return nil, err
	}
	return []artifactMember{
		{name: filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary)), data: binaryData, mode: binaryMode(spec)},
		{name: filepath.ToSlash(filepath.Join(archiveTopDir(spec), "README.md")), data: documents["README.md"], mode: 0644},
		{name: filepath.ToSlash(filepath.Join(archiveTopDir(spec), "LICENSE")), data: documents["LICENSE"], mode: 0644},
		{name: filepath.ToSlash(filepath.Join(archiveTopDir(spec), "THIRD_PARTY_NOTICES.md")), data: documents["THIRD_PARTY_NOTICES.md"], mode: 0644},
	}, nil
}

func sourceDocuments(root string, revision string) (map[string][]byte, error) {
	paths := []string{"README.md", "LICENSE", "THIRD_PARTY_NOTICES.md"}
	docs := make(map[string][]byte, len(paths))
	for _, name := range paths {
		data, err := readRegularFile(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if name == "README.md" {
			data, err = releaseREADME(data, revision)
			if err != nil {
				return nil, err
			}
		}
		docs[name] = data
	}
	return docs, nil
}

func releaseREADME(input []byte, revision string) ([]byte, error) {
	text := string(input)
	var linkErr error
	text = markdownLink.ReplaceAllStringFunc(text, func(match string) string {
		if linkErr != nil {
			return match
		}
		parts := markdownLink.FindStringSubmatch(match)
		destination := parts[1]
		pathPart, fragment, hasFragment := strings.Cut(destination, "#")
		switch pathPart {
		case "docs/APPLICATION_SPEC.md", "./docs/APPLICATION_SPEC.md":
			pathPart = "https://github.com/evdanil/gotest/blob/" + revision + "/docs/APPLICATION_SPEC.md"
		case "docs/DEVELOPMENT_PLAN.md", "./docs/DEVELOPMENT_PLAN.md":
			pathPart = "https://github.com/evdanil/gotest/blob/" + revision + "/docs/DEVELOPMENT_PLAN.md"
		case "", "LICENSE", "THIRD_PARTY_NOTICES.md":
		default:
			if strings.HasPrefix(pathPart, "https://") || strings.HasPrefix(pathPart, "http://") || strings.HasPrefix(pathPart, "mailto:") {
				return match
			}
			linkErr = fmt.Errorf("unresolved local README link %q", destination)
			return match
		}
		if pathPart == "" || pathPart == "LICENSE" || pathPart == "THIRD_PARTY_NOTICES.md" {
			return match
		}
		if hasFragment {
			pathPart += "#" + fragment
		}
		return strings.Replace(match, destination, pathPart, 1)
	})
	if linkErr != nil {
		return nil, linkErr
	}
	return []byte(text), nil
}

func writeArchive(filename string, members []artifactMember, spec targetSpec, modTime time.Time) error {
	if _, err := os.Lstat(filename); err == nil {
		return fmt.Errorf("archive output already exists: %s", filename)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect archive output: %w", err)
	}
	if err := validateMemberSet(members, spec); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return fmt.Errorf("create archive parent: %w", err)
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer file.Close()
	if spec.zip {
		writer := zip.NewWriter(file)
		if err := writeZIP(writer, members, modTime); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("finish zip: %w", err)
		}
	} else {
		gzipWriter := gzip.NewWriter(file)
		gzipWriter.Name = ""
		gzipWriter.Comment = ""
		gzipWriter.ModTime = time.Time{}
		gzipWriter.OS = 255
		tarWriter := tar.NewWriter(gzipWriter)
		if err := writeTAR(tarWriter, members, modTime); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			return err
		}
		if err := tarWriter.Close(); err != nil {
			return fmt.Errorf("finish tar: %w", err)
		}
		if err := gzipWriter.Close(); err != nil {
			return fmt.Errorf("finish gzip: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}
	return nil
}

func writeTAR(writer *tar.Writer, members []artifactMember, modTime time.Time) error {
	for _, member := range sortedMembers(members) {
		header := &tar.Header{Name: member.name, Mode: int64(member.mode.Perm()), Size: int64(len(member.data)), ModTime: modTime.UTC(), Typeflag: tar.TypeReg, Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatUSTAR}
		if err := writer.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar member %s: %w", member.name, err)
		}
		if _, err := writer.Write(member.data); err != nil {
			return fmt.Errorf("write tar member %s: %w", member.name, err)
		}
	}
	return nil
}

func writeZIP(writer *zip.Writer, members []artifactMember, modTime time.Time) error {
	for _, member := range sortedMembers(members) {
		header := &zip.FileHeader{Name: member.name, Method: zip.Deflate}
		header.SetMode(member.mode.Perm())
		header.Modified = modTime.UTC()
		part, err := writer.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("write zip member %s: %w", member.name, err)
		}
		if _, err := part.Write(member.data); err != nil {
			return fmt.Errorf("write zip member %s: %w", member.name, err)
		}
	}
	return nil
}

func readArchive(filename string, spec targetSpec) (map[string]artifactMember, time.Time, error) {
	if spec.zip {
		return readZIP(filename, spec)
	}
	return readTARGZ(filename, spec)
}

func readZIP(filename string, spec targetSpec) (map[string]artifactMember, time.Time, error) {
	reader, err := zip.OpenReader(filename)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("open zip: %w", err)
	}
	defer reader.Close()
	if reader.Comment != "" {
		return nil, time.Time{}, errors.New("zip archive has an unexpected comment")
	}
	if len(reader.File) != 4 {
		return nil, time.Time{}, fmt.Errorf("zip has %d members, want exactly 4", len(reader.File))
	}
	result := make(map[string]artifactMember, len(reader.File))
	order := make([]string, 0, len(reader.File))
	var archiveTime time.Time
	for _, file := range reader.File {
		if err := validateArchiveName(file.Name, spec); err != nil {
			return nil, time.Time{}, err
		}
		if _, exists := result[file.Name]; exists {
			return nil, time.Time{}, fmt.Errorf("duplicate archive member %q", file.Name)
		}
		if !file.Mode().IsRegular() || file.Mode()&os.ModeSymlink != 0 {
			return nil, time.Time{}, fmt.Errorf("archive member %q is not a regular file", file.Name)
		}
		wantMode := expectedModes(spec)[file.Name]
		wantAttributes := uint32(0100000|wantMode.Perm()) << 16
		if wantMode.Perm()&0200 == 0 {
			wantAttributes |= 1
		}
		if !bytes.Equal(file.Extra, canonicalZipTimeExtra(file.Modified)) || file.Comment != "" || file.CreatorVersion>>8 != 3 || file.ReaderVersion != 20 || file.ExternalAttrs != wantAttributes {
			return nil, time.Time{}, fmt.Errorf("archive member %q has unexpected zip metadata (creator=%#x reader=%d attrs=%#x want=%#x extra=%d comment=%q)", file.Name, file.CreatorVersion, file.ReaderVersion, file.ExternalAttrs, wantAttributes, len(file.Extra), file.Comment)
		}
		if file.Method != zip.Deflate {
			return nil, time.Time{}, fmt.Errorf("archive member %q has unexpected compression method", file.Name)
		}
		modTime := file.Modified.UTC()
		if archiveTime.IsZero() {
			archiveTime = modTime
		} else if !archiveTime.Equal(modTime) {
			return nil, time.Time{}, errors.New("zip members have inconsistent timestamps")
		}
		reader, err := file.Open()
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("open zip member %s: %w", file.Name, err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, time.Time{}, fmt.Errorf("read zip member %s: %w", file.Name, readErr)
		}
		if closeErr != nil {
			return nil, time.Time{}, fmt.Errorf("close zip member %s: %w", file.Name, closeErr)
		}
		result[file.Name] = artifactMember{name: file.Name, data: data, mode: file.Mode()}
		order = append(order, file.Name)
	}
	if err := validateMemberOrder(order, spec); err != nil {
		return nil, time.Time{}, err
	}
	if err := validateMemberMap(result, spec); err != nil {
		return nil, time.Time{}, err
	}
	return result, archiveTime, nil
}

func canonicalZipTimeExtra(modified time.Time) []byte {
	result := make([]byte, 9)
	binary.LittleEndian.PutUint16(result[0:2], 0x5455)
	binary.LittleEndian.PutUint16(result[2:4], 5)
	result[4] = 1
	binary.LittleEndian.PutUint32(result[5:9], uint32(modified.Unix()))
	return result
}

func readTARGZ(filename string, spec targetSpec) (map[string]artifactMember, time.Time, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("open tar.gz: %w", err)
	}
	defer file.Close()
	compressedReader := bufio.NewReader(file)
	gzipReader, err := gzip.NewReader(compressedReader)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read gzip header: %w", err)
	}
	gzipReader.Multistream(false)
	if gzipReader.Name != "" || gzipReader.Comment != "" || !gzipReader.ModTime.IsZero() || gzipReader.OS != 255 || len(gzipReader.Extra) != 0 {
		_ = gzipReader.Close()
		return nil, time.Time{}, errors.New("gzip metadata is not normalized")
	}
	decompressed, err := io.ReadAll(gzipReader)
	closeErr := gzipReader.Close()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("decompress archive: %w", err)
	}
	if closeErr != nil {
		return nil, time.Time{}, fmt.Errorf("finish gzip stream: %w", closeErr)
	}
	if _, err := compressedReader.Peek(1); err == nil {
		return nil, time.Time{}, errors.New("gzip stream has trailing compressed data")
	} else if !errors.Is(err, io.EOF) {
		return nil, time.Time{}, fmt.Errorf("check gzip stream boundary: %w", err)
	}
	archiveReader := tar.NewReader(bytes.NewReader(decompressed))
	result := make(map[string]artifactMember, 4)
	order := make([]string, 0, 4)
	var archiveTime time.Time
	for {
		header, err := archiveReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("read tar header: %w", err)
		}
		if err := validateArchiveName(header.Name, spec); err != nil {
			return nil, time.Time{}, err
		}
		if _, exists := result[header.Name]; exists {
			return nil, time.Time{}, fmt.Errorf("duplicate archive member %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || header.Format != tar.FormatUSTAR {
			return nil, time.Time{}, fmt.Errorf("archive member %q has unexpected type or owner metadata", header.Name)
		}
		if archiveTime.IsZero() {
			archiveTime = header.ModTime.UTC()
		} else if !archiveTime.Equal(header.ModTime.UTC()) {
			return nil, time.Time{}, errors.New("tar members have inconsistent timestamps")
		}
		data, err := io.ReadAll(archiveReader)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("read tar member %s: %w", header.Name, err)
		}
		result[header.Name] = artifactMember{name: header.Name, data: data, mode: os.FileMode(header.Mode)}
		order = append(order, header.Name)
	}
	if err := validateMemberOrder(order, spec); err != nil {
		return nil, time.Time{}, err
	}
	if err := validateMemberMap(result, spec); err != nil {
		return nil, time.Time{}, err
	}
	if err := validateTARTrailer(decompressed, order, result); err != nil {
		return nil, time.Time{}, err
	}
	return result, archiveTime, nil
}

func validateTARTrailer(data []byte, order []string, members map[string]artifactMember) error {
	// tar.Reader stops at the first zero header, so validate the trailer at the
	// exact offset after parsed members rather than only checking the file tail.
	expectedLength := 1024
	for _, name := range order {
		member, ok := members[name]
		if !ok {
			return errors.New("tar stream has invalid member boundary")
		}
		paddedSize := (len(member.data) + 511) / 512 * 512
		expectedLength += 512 + paddedSize
	}
	if len(data) != expectedLength || len(data)%512 != 0 {
		return errors.New("tar stream has invalid trailer length")
	}
	for _, b := range data[expectedLength-1024:] {
		if b != 0 {
			return errors.New("tar stream lacks normalized zero trailer")
		}
	}
	return nil
}

func validateArchiveName(name string, spec targetSpec) error {
	if strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, "../") || name == ".." {
		return fmt.Errorf("unsafe archive member path %q", name)
	}
	if name != filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary)) &&
		name != archiveTopDir(spec)+"/README.md" && name != archiveTopDir(spec)+"/LICENSE" && name != archiveTopDir(spec)+"/THIRD_PARTY_NOTICES.md" {
		return fmt.Errorf("unexpected archive member %q", name)
	}
	return nil
}

func validateMemberSet(members []artifactMember, spec targetSpec) error {
	byName := make(map[string]artifactMember, len(members))
	for _, member := range members {
		if err := validateArchiveName(member.name, spec); err != nil {
			return err
		}
		if _, exists := byName[member.name]; exists {
			return fmt.Errorf("duplicate archive member %q", member.name)
		}
		byName[member.name] = member
	}
	return validateMemberMap(byName, spec)
}

func validateMemberMap(members map[string]artifactMember, spec targetSpec) error {
	if len(members) != 4 {
		return fmt.Errorf("archive has %d members, want exactly 4", len(members))
	}
	for name, mode := range expectedModes(spec) {
		member, ok := members[name]
		if !ok {
			return fmt.Errorf("archive is missing member %q", name)
		}
		if member.mode != mode {
			return fmt.Errorf("archive member %q mode is %04o, want %04o", name, member.mode, mode)
		}
	}
	return nil
}

func validateMemberOrder(order []string, spec targetSpec) error {
	want := []string{
		archiveTopDir(spec) + "/LICENSE",
		archiveTopDir(spec) + "/README.md",
		archiveTopDir(spec) + "/THIRD_PARTY_NOTICES.md",
		filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary)),
	}
	sort.Strings(want)
	if len(order) != len(want) {
		return errors.New("archive member order has wrong length")
	}
	for i := range want {
		if order[i] != want[i] {
			return errors.New("archive members are not in normalized lexical order")
		}
	}
	return nil
}

func expectedModes(spec targetSpec) map[string]os.FileMode {
	return map[string]os.FileMode{
		filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary)): binaryMode(spec),
		archiveTopDir(spec) + "/README.md":                                0644,
		archiveTopDir(spec) + "/LICENSE":                                  0644,
		archiveTopDir(spec) + "/THIRD_PARTY_NOTICES.md":                   0644,
	}
}

func compareDocuments(members map[string]artifactMember, spec targetSpec, docs map[string][]byte) error {
	for name, expected := range docs {
		memberName := path.Join(archiveTopDir(spec), name)
		member, ok := members[memberName]
		if !ok {
			return fmt.Errorf("archive is missing document %q", memberName)
		}
		if !bytes.Equal(member.data, expected) {
			return fmt.Errorf("archive document %q differs from source", memberName)
		}
	}
	return nil
}

func extractArchive(destination string, members map[string]artifactMember, spec targetSpec, modTime time.Time) error {
	if destination == "" {
		return errors.New("extract-dir is required")
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("extract-dir must be a real directory")
		}
		entries, err := os.ReadDir(destination)
		if err != nil {
			return fmt.Errorf("read extract-dir: %w", err)
		}
		if len(entries) != 0 {
			return errors.New("extract-dir must be empty")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect extract-dir: %w", err)
	} else if err := os.MkdirAll(destination, 0755); err != nil {
		return fmt.Errorf("create extract-dir: %w", err)
	}
	for _, member := range sortedMembers(mapToMembers(members)) {
		if err := validateArchiveName(member.name, spec); err != nil {
			return err
		}
		filename := filepath.Join(destination, filepath.FromSlash(member.name))
		if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
			return fmt.Errorf("create extraction directory: %w", err)
		}
		file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, member.mode.Perm())
		if err != nil {
			return fmt.Errorf("create extracted member %s: %w", member.name, err)
		}
		_, writeErr := file.Write(member.data)
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write extracted member %s: %w", member.name, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close extracted member %s: %w", member.name, closeErr)
		}
		if err := os.Chmod(filename, member.mode.Perm()); err != nil {
			return fmt.Errorf("set extracted mode %s: %w", member.name, err)
		}
		if err := os.Chtimes(filename, modTime, modTime); err != nil {
			return fmt.Errorf("set extracted timestamp %s: %w", member.name, err)
		}
	}
	return nil
}

func readRegularFile(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a regular non-symlink file", filename)
	}
	return os.ReadFile(filename)
}

func hashFile(filename string) (string, error) {
	data, err := readRegularFile(filename)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func membersBinary(members []artifactMember, spec targetSpec) []byte {
	for _, member := range members {
		if member.name == filepath.ToSlash(filepath.Join(archiveTopDir(spec), spec.binary)) {
			return member.data
		}
	}
	return nil
}

func sortedMembers(members []artifactMember) []artifactMember {
	result := append([]artifactMember(nil), members...)
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result
}

func mapToMembers(members map[string]artifactMember) []artifactMember {
	result := make([]artifactMember, 0, len(members))
	for _, member := range members {
		result = append(result, member)
	}
	return result
}

func binaryMode(spec targetSpec) os.FileMode {
	if spec.goos == "windows" {
		return 0644
	}
	return 0755
}

func archiveTopDir(spec targetSpec) string {
	return strings.TrimSuffix(spec.archive, map[bool]string{true: ".zip", false: ".tar.gz"}[spec.zip])
}

func normalizedTime(source time.Time, spec targetSpec) time.Time {
	result := source.UTC().Truncate(time.Second)
	if spec.zip {
		result = result.Truncate(2 * time.Second)
	}
	return result
}
