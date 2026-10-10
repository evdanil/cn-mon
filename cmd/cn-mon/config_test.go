package main

import (
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadSettingsLayeringGrammarAndCoercion(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	work := filepath.Join(root, "work")
	for _, d := range []string{home, bin, work} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(bin, ".cn")
	user := filepath.Join(home, ".cn")
	writeConfig(t, global, "[ping]\nhistory_file=global.db\n[logging]\nlevel=INFO\n")
	writeConfig(t, user, "[theme]\ntheme='dark'\n[logging]\nlogfile=user.log\n")
	link := filepath.Join(root, "cn-mon")
	if err := os.Symlink(filepath.Join(bin, "real-cn-mon"), link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "real-cn-mon"), []byte(""), 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "explicit.cn")
	data, err := os.ReadFile("testdata/config/layers.cn")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(root, "overrides.cn")
	data, err = os.ReadFile("testdata/config/overrides.cn")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{Executable: link, HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: filepath.Base(configPath)})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if cfg.HistoryFile != filepath.Join(home, "layer history.db") || cfg.HistoryDays.Cmp(big.NewInt(11)) != 0 || cfg.Theme != "pastel" || cfg.LogFile != filepath.Join(work, "layer.log") || cfg.LogLevel != "WARNING" {
		t.Fatalf("settings from explicit layer + DEFAULT: %+v", cfg)
	}
	writeConfig(t, user, "[ping]\nhistory_days=4\n[theme]\ntheme='dark'\n[logging]\nlogfile=user.log\n")
	cfg, warnings, err = loadSettings(configEnvironment{Executable: link, HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: "", Theme: "monochrome", LogFile: "", LogLevel: "NOTSET"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if cfg.HistoryDays.Cmp(big.NewInt(4)) != 0 || cfg.Theme != "monochrome" || cfg.LogFile != filepath.Join(work, "user.log") || cfg.LogLevel != "NOTSET" {
		t.Fatalf("empty config/log overrides must leave prior layers: %+v", cfg)
	}
	cfg, _, err = loadSettings(configEnvironment{Executable: link, HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: filepath.Join(root, "overrides.cn")})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HistoryDays.Cmp(big.NewInt(25)) != 0 || cfg.LogLevel != "CRITICAL" {
		t.Fatalf("case-insensitive key / Python integer / layer merge: %+v", cfg)
	}
}

func TestLoadSettingsParseFailureRestoresEveryFallbackThenOverrides(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "bad.cn")
	b, err := os.ReadFile("testdata/config/invalid-duplicate.cn")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: bad, Theme: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || strings.Contains(warnings[0], "history") || strings.Contains(warnings[0], "credential") {
		t.Fatalf("parse failure should report one sanitized fallback warning: %q", warnings)
	}
	if cfg.HistoryFile != filepath.Join(home, ".cn-ping-history.db") || cfg.HistoryDays.Cmp(big.NewInt(30)) != 0 || cfg.Theme != "dark" || cfg.LogFile != filepath.Join(home, "cn.log") || cfg.LogLevel != "INFO" {
		t.Fatalf("parse failure fallback and surviving CLI override: %+v", cfg)
	}
}

func TestINIRejectsCaseInsensitiveDuplicateOptionsAcrossExplicitDefaultHeader(t *testing.T) {
	b, err := os.ReadFile("testdata/config/invalid-duplicate-option.cn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseINI(b); err == nil {
		t.Fatal("expected ConfigParser strict duplicate-option error")
	}
}

func TestLoadSettingsExplicitHistoryOverridesAndRelativeINIPath(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "paths.cn")
	writeConfig(t, config, "[ping]\nhistory_file=./relative.db\nhistory_days=-9\n")
	env := configEnvironment{HomeDir: home, WorkingDir: work}
	cfg, warnings, err := loadSettings(env, configOverrides{ConfigFile: config})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || cfg.HistoryFile != filepath.Join(work, "relative.db") || cfg.HistoryDays.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("relative INI path or clamped signed retention: %+v warnings=%q", cfg, warnings)
	}
	days := big.NewInt(365)
	history := "~/chosen.db"
	cfg, _, err = loadSettings(env, configOverrides{ConfigFile: config, HistoryFile: &history, HistoryDays: days})
	if err != nil {
		t.Fatal(err)
	}
	days.SetInt64(7)
	if cfg.HistoryFile != filepath.Join(home, "chosen.db") || cfg.HistoryDays.Cmp(big.NewInt(365)) != 0 {
		t.Fatalf("CLI history overrides must resolve home and copy arbitrary integer input: %+v", cfg)
	}
}

func TestLoadSettingsThemeAndHistoryValueFallbacks(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "values.cn")
	writeConfig(t, p, "[ping]\nhistory_file = .\nhistory_days = not-an-int\n[theme]\ntheme = Unknown\n[logging]\nlevel = nonsense\n")
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: p})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HistoryFile != filepath.Join(home, ".cn-ping-history.db") || cfg.HistoryDays.Cmp(big.NewInt(30)) != 0 || cfg.Theme != "default" || cfg.LogLevel != "INFO" {
		t.Fatalf("per-setting fallbacks: %+v", cfg)
	}
	if len(warnings) != 2 {
		t.Fatalf("unknown theme and parseable bad day should be reported as warnings: %q", warnings)
	}
}

func TestConfigBOMCommentsContinuationsInterpolationAndPythonLargeInt(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "grammar.cn")
	large := strings.Repeat("9", 4300)
	contents := "# whole-line comment\n[ping]\nhistory_days = " + large + "\n[logging]\nlogfile = %HOME%/# literal\n  nested\nlevel=INFO\n"
	if err := os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, []byte(contents)...), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %q", warnings)
	}
	wantDays, ok := new(big.Int).SetString(large, 10)
	if !ok || cfg.HistoryDays.Cmp(wantDays) != 0 {
		t.Fatalf("large Python integer was not preserved")
	}
	if cfg.LogFile != filepath.Join(work, "%HOME%/# literal\nnested") {
		t.Fatalf("continuation/comment/interpolation grammar produced %q", cfg.LogFile)
	}
}

func TestConfigParserAcceptsRelativeIndentBlankContinuationAndSectionTailComment(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/grammar-formatting.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "formatting.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || cfg.HistoryDays.Cmp(big.NewInt(90)) != 0 || cfg.HistoryFile != filepath.Join(work, "indented.db") || cfg.Theme != "dark" || cfg.LogFile != filepath.Join(work, "formatting.log") || cfg.LogLevel != "ERROR" {
		t.Fatalf("valid ConfigParser indentation/blank continuation/tail comment rejected: settings=%+v warnings=%q", cfg, warnings)
	}
}

func TestConfigParserPreambleErrorRestoresAllSettingsBeforeCLIOverrides(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/invalid-preamble.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "preamble.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path, Theme: "monochrome"})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || cfg.HistoryFile != filepath.Join(home, ".cn-ping-history.db") || cfg.HistoryDays.Cmp(big.NewInt(30)) != 0 || cfg.Theme != "monochrome" || cfg.LogFile != filepath.Join(home, "cn.log") || cfg.LogLevel != "INFO" {
		t.Fatalf("section-header error must discard every INI setting then keep CLI override: settings=%+v warnings=%q", cfg, warnings)
	}
}

func TestPythonIntegerCoercionAllowsWhitespaceInsideOuterQuotes(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/quoted-days.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "quoted.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || cfg.HistoryDays.Cmp(big.NewInt(45)) != 0 {
		t.Fatalf("Python int() accepts whitespace after quote cleanup: days=%s warnings=%q", cfg.HistoryDays, warnings)
	}
}

func TestNamedUserHomeExpansionMatchesPathExpanduser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fixture names the Linux root account")
	}
	rootUser, err := user.Lookup("root")
	if err != nil {
		t.Skip("root account is unavailable")
	}
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/named-user-history.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "named-user.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(rootUser.HomeDir, "archive.db"); cfg.HistoryFile != want {
		t.Fatalf("named-user history path = %q, want Python expanduser path %q", cfg.HistoryFile, want)
	}
}

func TestUnknownNamedUserExpansionReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ntpath.expanduser uses profile siblings rather than account lookup")
	}
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/missing-named-user.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "missing-user.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path}); err == nil {
		t.Fatal("Python Path.expanduser raises for an unknown named user; settings must not silently select a different history database")
	}
}

func TestWindowsNamedUserExpanduserUsesProfileSibling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path expansion")
	}
	home := filepath.Join("C:\\Users", "agent")
	t.Setenv("USERPROFILE", home)
	t.Setenv("USERNAME", "agent")
	got, err := expandHome(`~other\archive.db`, home)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("C:\\Users", "other", "archive.db"); got != want {
		t.Fatalf("ntpath.expanduser named user path = %q, want %q", got, want)
	}
	got, err = expandHome(`~\archive.db`, home)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "archive.db"); got != want {
		t.Fatalf("Windows native home shorthand = %q, want %q", got, want)
	}
}

func TestWindowsNamedUserExpansionRejectsUnmatchedProfileBasename(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path expansion")
	}
	t.Setenv("USERPROFILE", filepath.Join("C:\\Profiles", "custom"))
	t.Setenv("USERNAME", "agent")
	if _, err := expandHome(`~other\archive.db`, os.Getenv("USERPROFILE")); err == nil {
		t.Fatal("ntpath.expanduser leaves an unmatched named-user path unresolved, and Path.expanduser raises")
	}
	if _, err := expandHome(`~Agent\archive.db`, filepath.Join("C:\\Users", "Agent")); err == nil {
		t.Fatal("ntpath compares USERNAME case-sensitively")
	}
	t.Setenv("USERNAME", "")
	if _, err := expandHome(`~other\archive.db`, filepath.Join("C:\\Users", "agent")); err == nil {
		t.Fatal("ntpath leaves named-user paths unresolved when USERNAME is absent")
	}
}

func TestWindowsCurrentHomeFallsBackToDriveAndPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows home environment")
	}
	previous, wasSet := os.LookupEnv("USERPROFILE")
	if err := os.Unsetenv("USERPROFILE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("USERPROFILE", previous)
		} else {
			_ = os.Unsetenv("USERPROFILE")
		}
	})
	t.Setenv("HOMEDRIVE", `D:`)
	t.Setenv("HOMEPATH", `\Users\agent`)
	home, err := currentHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`D:`, `\Users\agent`); home != want {
		t.Fatalf("ntpath home fallback = %q, want %q", home, want)
	}
}

func TestWindowsMissingHomeReturnsErrorBeforeReadingINI(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows home resolution")
	}
	unset := func(name string) {
		old, wasSet := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if wasSet {
				_ = os.Setenv(name, old)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	unset("USERPROFILE")
	unset("HOMEDRIVE")
	unset("HOMEPATH")
	root := t.TempDir()
	config := filepath.Join(root, "invalid.cn")
	if err := os.WriteFile(config, []byte("[ping]\nhistory_days=90\n[ping]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := loadSettings(configEnvironment{}, configOverrides{ConfigFile: config})
	if err == nil || len(warnings) != 0 {
		t.Fatalf("unresolved home must fail before reading INI: err=%v warnings=%q", err, warnings)
	}
}

func TestLoadSettingsUsesPasswdHomeWhenLinuxHOMEIsUnset(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("POSIX passwd home fallback")
	}
	previous, wasSet := os.LookupEnv("HOME")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("HOME", previous)
		} else {
			_ = os.Unsetenv("HOME")
		}
	})
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cfg, _, err := loadSettings(configEnvironment{WorkingDir: work}, configOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(current.HomeDir, ".cn-ping-history.db"); cfg.HistoryFile != want {
		t.Fatalf("missing HOME must use passwd home: got %q want %q", cfg.HistoryFile, want)
	}
}

func TestChooseHomeDirectoryErrorsWithoutAnyResolution(t *testing.T) {
	if _, err := chooseHomeDirectory("", ""); err == nil {
		t.Fatal("must return an error rather than selecting a relative current-directory config/history path")
	}
}

func TestLoadSettingsPropagatesGetwdFailureBeforeReadingConfig(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("deleted-current-directory behavior")
	}
	if os.Getenv("CNMON_TEST_GETWD_HELPER") == "1" {
		dir, err := os.MkdirTemp("", "cn-mon-deleted-cwd-")
		if err != nil {
			os.Exit(2)
		}
		if err := os.Chdir(dir); err != nil {
			os.Exit(3)
		}
		if err := os.Remove(dir); err != nil {
			os.Exit(4)
		}
		_, warnings, err := loadSettings(configEnvironment{HomeDir: os.Getenv("CNMON_TEST_HOME")}, configOverrides{ConfigFile: os.Getenv("CNMON_TEST_CONFIG")})
		if err == nil || !strings.Contains(err.Error(), "working directory") || len(warnings) != 0 {
			fmt.Fprintf(os.Stderr, "loadSettings = err:%v warnings:%q; want getwd error before configuration read\n", err, warnings)
			os.Exit(5)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	home, config := filepath.Join(root, "home"), filepath.Join(root, "explicit.cn")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("[ping]\nhistory_days=90\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CNMON_TEST_GETWD_HELPER", "1")
	t.Setenv("CNMON_TEST_HOME", home)
	t.Setenv("CNMON_TEST_CONFIG", config)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoadSettingsPropagatesGetwdFailureBeforeReadingConfig$")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("getwd helper failed: %v\n%s", err, output)
	}
}

func TestConfigParserAllowsRepeatedDEFAULTWithDistinctOptions(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/repeated-default.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "repeated-default.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || cfg.HistoryDays.Cmp(big.NewInt(45)) != 0 || cfg.LogLevel != "ERROR" || cfg.HistoryFile != filepath.Join(work, "selected.db") {
		t.Fatalf("ConfigParser permits repeated DEFAULT headers with distinct options: settings=%+v warnings=%q", cfg, warnings)
	}
}

func TestConfigParserTrimsFinalBlankLinesFromLoggingValues(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/config/logging-trailing-blank.cn")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "trailing.cn")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := loadSettings(configEnvironment{HomeDir: home, WorkingDir: work}, configOverrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || cfg.LogFile != filepath.Join(work, "run.log") || cfg.LogLevel != "ERROR" {
		t.Fatalf("trailing empty lines must not change logging path/level: settings=%+v warnings=%q", cfg, warnings)
	}
}

func writeConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
