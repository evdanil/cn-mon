package main

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

type configEnvironment struct{ Executable, HomeDir, WorkingDir string }
type configOverrides struct {
	ConfigFile, Theme, LogFile, LogLevel string
	HistoryFile                          *string
	HistoryDays                          *big.Int
}
type settings struct {
	HistoryFile              string
	HistoryDays              *big.Int
	Theme, LogFile, LogLevel string
}

func loadSettings(env configEnvironment, overrides configOverrides) (settings, []string, error) {
	if env.HomeDir == "" {
		var err error
		env.HomeDir, err = currentHomeDir()
		if err != nil {
			return settings{}, nil, err
		}
	}
	if env.WorkingDir == "" {
		var err error
		env.WorkingDir, err = os.Getwd()
		if err != nil {
			return settings{}, nil, fmt.Errorf("resolve working directory: %w", err)
		}
		if env.WorkingDir == "" {
			return settings{}, nil, fmt.Errorf("resolve working directory")
		}
	}
	result := settings{HistoryFile: filepath.Join(env.HomeDir, ".cn-ping-history.db"), HistoryDays: big.NewInt(30), Theme: "default", LogFile: filepath.Join(env.HomeDir, "cn.log"), LogLevel: "INFO"}
	paths := make([]string, 0, 3)
	if env.Executable != "" {
		executable, err := expandHome(env.Executable, env.HomeDir)
		if err != nil {
			return settings{}, nil, err
		}
		if !filepath.IsAbs(executable) {
			executable = filepath.Join(env.WorkingDir, executable)
		}
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		paths = append(paths, filepath.Join(filepath.Dir(executable), ".cn"))
	}
	paths = append(paths, filepath.Join(env.HomeDir, ".cn"))
	if overrides.ConfigFile != "" {
		p, err := expandHome(overrides.ConfigFile, env.HomeDir)
		if err != nil {
			return settings{}, nil, err
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(env.WorkingDir, p)
		}
		paths = append(paths, p)
	}
	values, parseErr := readINI(paths)
	warnings := []string{}
	if parseErr != nil {
		warnings = append(warnings, "invalid configuration file; using built-in defaults for INI settings")
	} else {
		if v, ok := values.get("ping", "history_file"); ok {
			v = cleanQuoted(v)
			if v == "" || filepath.Clean(v) == "." {
				result.HistoryFile = filepath.Join(env.HomeDir, ".cn-ping-history.db")
			} else {
				result.HistoryFile, parseErr = expandHome(v, env.HomeDir)
				if parseErr != nil {
					return settings{}, warnings, parseErr
				}
				if !filepath.IsAbs(result.HistoryFile) {
					result.HistoryFile = filepath.Join(env.WorkingDir, result.HistoryFile)
				}
			}
		}
		if v, ok := values.get("ping", "history_days"); ok {
			if n, valid := pythonInt(v); valid {
				if n.Sign() < 1 {
					n = big.NewInt(1)
				}
				result.HistoryDays = n
			} else {
				warnings = append(warnings, "invalid [ping] history_days; using 30")
			}
		}
		if v, ok := values.get("theme", "theme"); ok {
			v = cleanQuoted(v)
			if validTheme(v) {
				result.Theme = v
			} else {
				result.Theme = "default"
				warnings = append(warnings, "unknown [theme] theme; using default")
			}
		}
		if v, ok := values.get("logging", "logfile"); ok {
			result.LogFile = v
		}
		if v, ok := values.get("logging", "level"); ok {
			result.LogLevel = v
		}
	}
	if overrides.HistoryFile != nil {
		result.HistoryFile = *overrides.HistoryFile
		if result.HistoryFile != "" {
			var err error
			result.HistoryFile, err = expandHome(result.HistoryFile, env.HomeDir)
			if err != nil {
				return settings{}, warnings, err
			}
			if !filepath.IsAbs(result.HistoryFile) {
				result.HistoryFile = filepath.Join(env.WorkingDir, result.HistoryFile)
			}
		}
	}
	if overrides.HistoryDays != nil {
		result.HistoryDays = new(big.Int).Set(overrides.HistoryDays)
	}
	if overrides.Theme != "" {
		result.Theme = overrides.Theme
	}
	if overrides.LogFile != "" {
		result.LogFile = overrides.LogFile
	}
	if overrides.LogLevel != "" {
		result.LogLevel = overrides.LogLevel
	}
	var err error
	result.LogFile, err = expandHome(result.LogFile, env.HomeDir)
	if err != nil {
		return settings{}, warnings, err
	}
	if !filepath.IsAbs(result.LogFile) {
		result.LogFile = filepath.Join(env.WorkingDir, result.LogFile)
	}
	result.LogLevel = normalizeLevel(result.LogLevel)
	return result, warnings, nil
}

func validTheme(s string) bool {
	return s == "default" || s == "monochrome" || s == "pastel" || s == "dark"
}

func chooseHomeDirectory(environmentHome, passwdHome string) (string, error) {
	if environmentHome != "" {
		return environmentHome, nil
	}
	if passwdHome != "" {
		return passwdHome, nil
	}
	return "", fmt.Errorf("cannot resolve home directory")
}

func currentHomeDir() (string, error) {
	if runtime.GOOS == "windows" {
		if environmentHome, ok := os.LookupEnv("USERPROFILE"); ok {
			if environmentHome != "" {
				return environmentHome, nil
			}
			return "", fmt.Errorf("cannot resolve home directory")
		}
		homePath, hasHomePath := os.LookupEnv("HOMEPATH")
		if hasHomePath {
			drive, _ := os.LookupEnv("HOMEDRIVE")
			return filepath.Join(drive, homePath), nil
		}
		return "", fmt.Errorf("cannot resolve home directory")
	}
	environmentHome, _ := os.UserHomeDir()
	if environmentHome != "" {
		return environmentHome, nil
	}
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory")
	}
	return chooseHomeDirectory("", current.HomeDir)
}

func cleanQuoted(s string) string { return strings.Trim(strings.TrimSpace(s), "\"'") }
func expandHome(s, home string) (string, error) {
	if len(s) == 0 || s[0] != '~' {
		return s, nil
	}
	isSeparator := func(b byte) bool {
		if runtime.GOOS == "windows" {
			return b == '/' || b == '\\'
		}
		return b == '/'
	}
	if len(s) == 1 {
		if home == "" {
			return "", fmt.Errorf("cannot resolve home directory")
		}
		return home, nil
	}
	separator := -1
	for i := 1; i < len(s); i++ {
		if isSeparator(s[i]) {
			separator = i
			break
		}
	}
	username := s[1:]
	rest := ""
	if separator >= 0 {
		username, rest = s[1:separator], s[separator+1:]
	}
	var userHome string
	if username == "" {
		userHome = home
	} else if runtime.GOOS == "windows" {
		currentName := os.Getenv("USERNAME")
		if username == currentName {
			userHome = home
		} else {
			if filepath.Base(home) != currentName {
				return "", fmt.Errorf("cannot resolve named home directory")
			}
			userHome = filepath.Join(filepath.Dir(home), username)
		}
	} else {
		account, err := user.Lookup(username)
		if err != nil {
			return "", fmt.Errorf("cannot resolve named home directory")
		}
		userHome = account.HomeDir
	}
	if userHome == "" {
		return "", fmt.Errorf("cannot resolve home directory")
	}
	if rest == "" {
		return userHome, nil
	}
	return filepath.Join(userHome, rest), nil
}

func pythonInt(raw string) (*big.Int, bool) {
	s := strings.TrimSpace(cleanQuoted(raw))
	if s == "" {
		return nil, false
	}
	// Python's base-10 int accepts underscores between digits and Unicode decimal digits.
	var b strings.Builder
	runes := []rune(s)
	digitCount := 0
	for i, r := range runes {
		if r == '_' {
			if i == 0 || i+1 == len(runes) || decimalDigit(runes[i-1]) < 0 || decimalDigit(runes[i+1]) < 0 {
				return nil, false
			}
			continue
		}
		if (r == '+' || r == '-') && i == 0 {
			b.WriteRune(r)
			continue
		}
		if r > unicode.MaxASCII {
			d := decimalDigit(r)
			if d < 0 {
				return nil, false
			}
			b.WriteByte(byte('0' + d))
			digitCount++
			continue
		}
		if r >= '0' && r <= '9' {
			digitCount++
		} else if r != '+' && r != '-' {
			return nil, false
		}
		b.WriteRune(r)
	}
	if digitCount == 0 || digitCount > 4300 {
		return nil, false
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	return n, ok
}
func decimalDigit(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	for _, rr := range unicode.Digit.R16 {
		start, end, stride := rune(rr.Lo), rune(rr.Hi), rune(rr.Stride)
		if r >= start && r <= end && (r-start)%stride == 0 {
			return int((r - start) % 10)
		}
	}
	for _, rr := range unicode.Digit.R32 {
		start, end, stride := rune(rr.Lo), rune(rr.Hi), rune(rr.Stride)
		if r >= start && r <= end && (r-start)%stride == 0 {
			return int((r - start) % 10)
		}
	}
	return -1
}

type iniFile struct {
	defaults map[string]string
	sections map[string]map[string]string
}

func (f iniFile) get(section, option string) (string, bool) {
	if section == "DEFAULT" {
		v, ok := f.defaults[option]
		return v, ok
	}
	options, ok := f.sections[section]
	if !ok {
		return "", false
	}
	if v, ok := options[option]; ok {
		return v, true
	}
	v, ok := f.defaults[option]
	return v, ok
}

func readINI(paths []string) (iniFile, error) {
	merged := iniFile{defaults: map[string]string{}, sections: map[string]map[string]string{}}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		local, err := parseINI(data)
		if err != nil {
			return iniFile{}, err
		}
		for k, v := range local.defaults {
			merged.defaults[k] = v
		}
		for section, opts := range local.sections {
			if merged.sections[section] == nil {
				merged.sections[section] = map[string]string{}
			}
			for k, v := range opts {
				merged.sections[section][k] = v
			}
		}
	}
	return merged, nil
}

func parseINI(data []byte) (iniFile, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !validUTF8(data) {
		return iniFile{}, fmt.Errorf("invalid UTF-8")
	}
	out := iniFile{defaults: map[string]string{}, sections: map[string]map[string]string{}}
	seenSections := map[string]bool{}
	seenOptions := map[string]map[string]bool{"DEFAULT": {}}
	section := "DEFAULT"
	hasSection := false
	lastOption := ""
	indentLevel := 0
	text := string(data)
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for lineNo, rawLine := range lines {
		lineNo++
		line := strings.TrimSuffix(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if hasSection && lastOption != "" {
				appendINIValue(&out, section, lastOption, "")
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		indent := leadingWhitespace(line)
		value := strings.TrimSpace(line)
		if hasSection && lastOption != "" && indent > indentLevel {
			appendINIValue(&out, section, lastOption, value)
			continue
		}
		indentLevel = indent
		if strings.HasPrefix(trimmed, "[") {
			end := strings.LastIndexByte(trimmed, ']')
			if end <= 1 {
				return iniFile{}, fmt.Errorf("invalid section at line %d", lineNo)
			}
			name := trimmed[1:end]
			if name != "DEFAULT" && seenSections[name] {
				return iniFile{}, fmt.Errorf("duplicate section at line %d", lineNo)
			}
			if name != "DEFAULT" {
				seenSections[name] = true
			}
			section = name
			hasSection = true
			lastOption = ""
			if section != "DEFAULT" {
				out.sections[section] = map[string]string{}
				seenOptions[section] = map[string]bool{}
			}
			continue
		}
		if !hasSection {
			return iniFile{}, fmt.Errorf("missing section header at line %d", lineNo)
		}
		cut := strings.IndexAny(line, "=:")
		if cut < 0 {
			return iniFile{}, fmt.Errorf("missing delimiter at line %d", lineNo)
		}
		key := strings.ToLower(strings.TrimSpace(line[:cut]))
		if key == "" {
			return iniFile{}, fmt.Errorf("empty option at line %d", lineNo)
		}
		if seenOptions[section][key] {
			return iniFile{}, fmt.Errorf("duplicate option at line %d", lineNo)
		}
		value = strings.TrimSpace(line[cut+1:])
		seenOptions[section][key] = true
		lastOption = key
		appendINIValue(&out, section, key, value)
	}
	for key, value := range out.defaults {
		out.defaults[key] = strings.TrimRightFunc(value, unicode.IsSpace)
	}
	for name, options := range out.sections {
		for key, value := range options {
			options[key] = strings.TrimRightFunc(value, unicode.IsSpace)
		}
		out.sections[name] = options
	}
	return out, nil
}
func validUTF8(b []byte) bool { return utf8.Valid(b) }

func leadingWhitespace(line string) int {
	for i, r := range line {
		if r != ' ' && r != '\t' {
			return i
		}
	}
	return len(line)
}

func appendINIValue(file *iniFile, section, option, value string) {
	var options map[string]string
	if section == "DEFAULT" {
		options = file.defaults
	} else {
		options = file.sections[section]
	}
	if previous, exists := options[option]; exists {
		options[option] = previous + "\n" + value
	} else {
		options[option] = value
	}
}
