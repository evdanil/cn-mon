package history

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// URI construction adapts _reader_uri in cn_tool/utils/ping_store.py:176–195,
// from cn-tool v0.8.0 commit 2e2f3c40ef6718486f1ca4477f9145029ec495fa
// (Apache-2.0); Go additionally escapes the OS filename as URI path data.
func resolveHistoryPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("history path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve history path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func sqliteURI(path string, readOnly bool) (string, error) {
	abs, err := resolveHistoryPath(path)
	if err != nil {
		return "", err
	}
	return sqliteURIForAbsolute(abs, readOnly), nil
}

func sqliteURIForAbsolute(path string, readOnly bool) string {
	location, host := sqliteURIPath(path)
	mode := "rwc"
	if readOnly {
		mode = "ro"
	}
	query := url.Values{}
	query.Set("mode", mode)
	query.Add("_pragma", "busy_timeout(10000)")
	query.Add("_pragma", "foreign_keys(ON)")
	return (&url.URL{Scheme: "file", Host: host, Path: location, RawQuery: query.Encode()}).String()
}

func sqliteURIPath(path string) (location, host string) {
	if runtime.GOOS != "windows" {
		return path, ""
	}
	return windowsSQLiteURIPath(path)
}

func windowsSQLiteURIPath(path string) (location, host string) {
	path = strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(path, "//") {
		unc := strings.TrimPrefix(path, "//")
		if slash := strings.IndexByte(unc, '/'); slash > 0 {
			return "/" + unc[slash+1:], unc[:slash]
		}
		return "/" + unc, ""
	}
	if len(path) >= 3 && isASCIIAlpha(path[0]) && path[1] == ':' && path[2] == '/' {
		return "/" + path, ""
	}
	return path, ""
}

func isASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
