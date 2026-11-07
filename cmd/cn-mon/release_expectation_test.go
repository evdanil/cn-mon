// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var releaseStableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// releaseExpectedVersionOutput returns the exact --version output of the
// release under qualification, derived from the version and revision that the
// release receipt exports. Absent or malformed values are an error, never a
// reason to skip, so a qualification cannot pass against a stale expectation.
func releaseExpectedVersionOutput() (string, error) {
	version := os.Getenv(releaseVersionEnv)
	revision := os.Getenv(releaseRevisionEnv)
	if !releaseStableVersionPattern.MatchString(version) {
		return "", fmt.Errorf("%s must be a stable MAJOR.MINOR.PATCH version", releaseVersionEnv)
	}
	if !releaseRevisionPattern.MatchString(revision) {
		return "", fmt.Errorf("%s must be 40 lowercase hex characters", releaseRevisionEnv)
	}
	return fmt.Sprintf("cn-mon %s (%s)\n", version, revision), nil
}

func TestReleaseExpectedVersionOutputIsDerivedFromReceiptValues(t *testing.T) {
	good := strings.Repeat("ab", 20)
	cases := []struct {
		name     string
		version  string
		revision string
		unset    string
		want     string
		wantErr  bool
	}{
		{name: "release 0.4.0", version: "0.4.0", revision: good, want: "cn-mon 0.4.0 (" + good + ")\n"},
		{name: "pinned 0.3.0 archive", version: "0.3.0", revision: "bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed", want: "cn-mon 0.3.0 (bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed)\n"},
		{name: "multi-digit components", version: "10.20.30", revision: good, want: "cn-mon 10.20.30 (" + good + ")\n"},
		{name: "version unset", version: "0.4.0", revision: good, unset: releaseVersionEnv, wantErr: true},
		{name: "revision unset", version: "0.4.0", revision: good, unset: releaseRevisionEnv, wantErr: true},
		{name: "version empty", version: "", revision: good, wantErr: true},
		{name: "revision empty", version: "0.4.0", revision: "", wantErr: true},
		{name: "version with v prefix", version: "v0.4.0", revision: good, wantErr: true},
		{name: "version with two components", version: "0.4", revision: good, wantErr: true},
		{name: "version with four components", version: "0.4.0.0", revision: good, wantErr: true},
		{name: "version with prerelease", version: "0.4.0-rc1", revision: good, wantErr: true},
		{name: "version with leading zero", version: "0.04.0", revision: good, wantErr: true},
		{name: "version with trailing newline", version: "0.4.0\n", revision: good, wantErr: true},
		{name: "version with space", version: " 0.4.0", revision: good, wantErr: true},
		{name: "revision uppercase", version: "0.4.0", revision: strings.ToUpper(good), wantErr: true},
		{name: "revision short", version: "0.4.0", revision: good[:39], wantErr: true},
		{name: "revision long", version: "0.4.0", revision: good + "a", wantErr: true},
		{name: "revision not hex", version: "0.4.0", revision: strings.Repeat("g", 40), wantErr: true},
		{name: "revision with trailing newline", version: "0.4.0", revision: good + "\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(releaseVersionEnv, tc.version)
			t.Setenv(releaseRevisionEnv, tc.revision)
			if tc.unset != "" {
				if err := os.Unsetenv(tc.unset); err != nil {
					t.Fatal(err)
				}
			}
			got, err := releaseExpectedVersionOutput()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted version=%q revision=%q unset=%q, want an error", tc.version, tc.revision, tc.unset)
				}
				if got != "" {
					t.Fatalf("rejected input still returned expectation %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected well-formed receipt values: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expectation = %q, want %q", got, tc.want)
			}
		})
	}
}
