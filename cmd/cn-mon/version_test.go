// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
)

func TestVersionOutputIncludesReleaseRevisionOnlyWhenSet(t *testing.T) {
	oldVersion, oldRevision := version, revision
	t.Cleanup(func() { version, revision = oldVersion, oldRevision })
	var stdout, stderr bytes.Buffer

	version, revision = "dev", ""
	if code := runCLI([]string{"--version"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("development version exit=%d stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "cn-mon dev\n"; got != want {
		t.Fatalf("development version = %q, want %q", got, want)
	}

	stdout.Reset()
	version, revision = "0.1.0", "0123456789abcdef0123456789abcdef01234567"
	if code := runCLI([]string{"--version"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("release version exit=%d stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "cn-mon 0.1.0 (0123456789abcdef0123456789abcdef01234567)\n"; got != want {
		t.Fatalf("release version = %q, want %q", got, want)
	}
}
