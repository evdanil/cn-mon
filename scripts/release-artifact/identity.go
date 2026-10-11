// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

func readIdentityBytes(data []byte, sourceRoot string, spec targetSpec, revision string) (identity, error) {
	if err := validateBinaryFormat(data, spec); err != nil {
		return identity{}, err
	}
	temporary, err := os.CreateTemp("", "cn-mon-release-identity-*")
	if err != nil {
		return identity{}, fmt.Errorf("create identity-check temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return identity{}, fmt.Errorf("write identity-check temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return identity{}, fmt.Errorf("close identity-check temporary file: %w", err)
	}
	info, err := buildinfo.ReadFile(temporaryPath)
	if err != nil {
		return identity{}, fmt.Errorf("read binary build information: %w", err)
	}
	if err := validateBuildInfo(info, sourceRoot, spec, revision); err != nil {
		return identity{}, err
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	buildTime, err := time.Parse(time.RFC3339, settings["vcs.time"])
	if err != nil {
		return identity{}, fmt.Errorf("invalid or missing vcs.time: %w", err)
	}
	return identity{time: buildTime}, nil
}

func validateBinaryFormat(data []byte, spec targetSpec) error {
	if spec.goos == "linux" {
		binary, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("binary is not ELF: %w", err)
		}
		if binary.Machine != elf.EM_X86_64 {
			return fmt.Errorf("ELF machine is %s, want x86-64", binary.Machine)
		}
		for _, program := range binary.Progs {
			if program.Type == elf.PT_INTERP {
				return errors.New("Linux binary has a dynamic interpreter")
			}
		}
		return nil
	}
	binary, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("binary is not PE: %w", err)
	}
	if binary.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		return fmt.Errorf("PE machine is 0x%x, want amd64", binary.Machine)
	}
	return nil
}

func validateBuildInfo(info *debug.BuildInfo, sourceRoot string, spec targetSpec, revision string) error {
	if info == nil {
		return errors.New("binary has no Go build information")
	}
	if info.GoVersion != "go1.27.2" {
		return fmt.Errorf("binary Go version is %q, want go1.27.2", info.GoVersion)
	}
	if info.Path != "github.com/evdanil/gotest/cmd/cn-mon" {
		return fmt.Errorf("binary main package is %q", info.Path)
	}
	if info.Main.Path != "github.com/evdanil/gotest" {
		return fmt.Errorf("binary module is %q", info.Main.Path)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != spec.goos || settings["GOARCH"] != "amd64" || settings["CGO_ENABLED"] != "0" || settings["GOAMD64"] != "v1" {
		return fmt.Errorf("binary target settings do not match %s with CGO disabled and GOAMD64=v1", spec.name)
	}
	if settings["-trimpath"] != "true" {
		return errors.New("binary was not built with -trimpath")
	}
	if settings["vcs"] != "git" || settings["vcs.revision"] != revision || !revisionPattern.MatchString(settings["vcs.revision"]) {
		return errors.New("binary VCS revision does not match the requested source revision")
	}
	if settings["vcs.modified"] != "false" {
		return errors.New("binary source tree is not recorded clean")
	}
	if _, err := time.Parse(time.RFC3339, settings["vcs.time"]); err != nil {
		return fmt.Errorf("invalid or missing vcs.time: %w", err)
	}
	if err := validateModules(info); err != nil {
		return err
	}
	if err := validateSourceModules(sourceRoot); err != nil {
		return err
	}
	return nil
}

func validateModules(info *debug.BuildInfo) error {
	seen := make(map[string]bool, len(info.Deps))
	for _, dep := range info.Deps {
		if dep == nil {
			return errors.New("binary build information contains an empty module")
		}
		version, ok := expectedModules[dep.Path]
		if !ok || dep.Version != version {
			return fmt.Errorf("unexpected binary module %s %s", dep.Path, dep.Version)
		}
		if seen[dep.Path] {
			return fmt.Errorf("duplicate binary module %s", dep.Path)
		}
		seen[dep.Path] = true
		if dep.Path == "github.com/prometheus-community/pro-bing" {
			if dep.Replace == nil || dep.Replace.Path != "./third_party/pro-bing" {
				return errors.New("binary does not use the reviewed local pro-bing replacement")
			}
		} else if dep.Replace != nil {
			return fmt.Errorf("binary module %s has an unexpected replacement", dep.Path)
		}
	}
	if len(seen) != len(expectedModules) {
		for module := range expectedModules {
			if !seen[module] {
				return fmt.Errorf("binary is missing expected module %s", module)
			}
		}
	}
	return nil
}

func validateSourceModules(sourceRoot string) error {
	data, err := readRegularFile(filepath.Join(sourceRoot, "go.mod"))
	if err != nil {
		return fmt.Errorf("read source go.mod: %w", err)
	}
	seen := make(map[string]bool, len(expectedModules))
	inRequire := false
	hasModule := false
	hasReplacement := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "//", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "module":
			hasModule = len(fields) == 2 && fields[1] == "github.com/evdanil/gotest"
		case "require":
			if len(fields) == 2 && fields[1] == "(" {
				inRequire = true
				continue
			}
			if len(fields) == 1 {
				inRequire = true
				continue
			}
			if len(fields) == 3 {
				if err := checkSourceModule(expectedModules, seen, fields[1], fields[2]); err != nil {
					return err
				}
			}
		case "replace":
			if len(fields) == 4 && fields[1] == "github.com/prometheus-community/pro-bing" && fields[2] == "=>" && fields[3] == "./third_party/pro-bing" {
				hasReplacement = true
			} else if len(fields) >= 2 && fields[1] == "github.com/prometheus-community/pro-bing" {
				return errors.New("source go.mod has an unexpected pro-bing replacement")
			}
		case ")":
			inRequire = false
		default:
			if inRequire && len(fields) >= 2 {
				if err := checkSourceModule(expectedModules, seen, fields[0], fields[1]); err != nil {
					return err
				}
			}
		}
	}
	if !hasModule || !hasReplacement {
		return errors.New("source go.mod module identity or local pro-bing replacement is invalid")
	}
	if len(seen) != len(expectedModules) {
		for module := range expectedModules {
			if !seen[module] {
				return fmt.Errorf("source go.mod is missing expected module %s", module)
			}
		}
	}
	return nil
}

func checkSourceModule(expected map[string]string, seen map[string]bool, module, version string) error {
	want, ok := expectedModules[module]
	if !ok || want != version {
		return fmt.Errorf("source go.mod has unexpected module %s %s", module, version)
	}
	if seen[module] {
		return fmt.Errorf("source go.mod repeats module %s", module)
	}
	seen[module] = true
	return nil
}
