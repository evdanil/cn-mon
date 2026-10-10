package history

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenSQLiteURIQuotesFilenameAsData(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "space ? # % ü.db")
	for _, readOnly := range []bool{false, true} {
		got, err := sqliteURI(filename, readOnly)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		wantPath, wantHost := sqliteURIPath(filename)
		if parsed.Scheme != "file" || parsed.Path != wantPath || parsed.Host != wantHost {
			t.Errorf("URI changed filename data: %q -> scheme=%q path=%q", filename, parsed.Scheme, parsed.Path)
		}
		query := parsed.Query()
		wantMode := "rwc"
		if readOnly {
			wantMode = "ro"
		}
		if query.Get("mode") != wantMode || len(query["_pragma"]) != 2 || query["_pragma"][0] != "busy_timeout(10000)" || query["_pragma"][1] != "foreign_keys(ON)" {
			t.Errorf("unsafe or missing URI options: %s (%v)", got, query)
		}
		for _, unexpected := range []string{"journal_mode", "vfs", "immutable", "cache"} {
			if query.Has(unexpected) {
				t.Errorf("URI includes unsupported option %q", unexpected)
			}
		}
	}
}

func TestOpenSQLiteURIQuotesWindowsDriveAndUNC(t *testing.T) {
	cases := []struct {
		name, filename, wantHost, wantPath string
	}{
		{"drive", `C:\Users\x\db ?#%.sqlite`, "", "/C:/Users/x/db ?#%.sqlite"},
		{"unc", `\\server\share\dir\db ?#%.sqlite`, "server", "/share/dir/db ?#%.sqlite"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			location, host := windowsSQLiteURIPath(test.filename)
			got := (&url.URL{Scheme: "file", Host: host, Path: location, RawQuery: "mode=rwc"}).String()
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Scheme != "file" || parsed.Host != test.wantHost || parsed.Path != test.wantPath {
				t.Fatalf("URI %q parsed as host=%q path=%q, want host=%q path=%q", got, parsed.Host, parsed.Path, test.wantHost, test.wantPath)
			}
			if strings.Contains(parsed.RawQuery, "#") || strings.Contains(parsed.RawQuery, "?") {
				t.Fatalf("filename characters leaked into query: %q", got)
			}
		})
	}
}

func TestOpenSQLiteURIPreservesLinuxBackslashFilename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}
	root := t.TempDir()
	filename := filepath.Join(root, `history\backup.db`)
	got, err := sqliteURI(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != filename {
		t.Fatalf("URI changed literal backslash filename: %q -> %q", filename, parsed.Path)
	}
	store, err := OpenWriter(context.Background(), filename)
	if err != nil {
		t.Fatalf("open literal backslash filename: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filename); err != nil {
		t.Fatalf("writer did not create exact filename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "history", "backup.db")); err == nil {
		t.Fatal("writer created a different path by treating backslash as a separator")
	}
}

func TestOpenResolveHistoryPathRejectsEmptyAndReturnsAbsolute(t *testing.T) {
	if _, err := resolveHistoryPath(""); err == nil {
		t.Fatal("empty history path accepted")
	}
	got, err := resolveHistoryPath(filepath.Join("relative", "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("resolved path is not absolute: %q", got)
	}
}
