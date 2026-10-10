package main

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evdanil/gotest/internal/target"
)

type commandMode uint8

const (
	modeRun commandMode = iota
	modeList
	modeShow
	modeFollow
	modeHelp
	modeVersion
)

type cliOptions struct {
	Mode       commandMode
	Objects    []string
	File       *string
	Ports      []int
	Every      *time.Duration
	For        *time.Duration
	Since      *time.Time
	OwnRunOnly bool
	Format     string
	ID         string
	Overrides  configOverrides
}

func parseCLI(args []string, startUTC time.Time) (cliOptions, error) {
	opts := cliOptions{Mode: modeRun, Format: "table"}
	var selectedMode commandMode
	var modeConflict bool
	var unknownOption string
	options := true
	value := func(index int, inline string, hasInline bool, name string) (string, int, error) {
		if hasInline {
			return inline, index, nil
		}
		if index+1 >= len(args) {
			return "", index, fmt.Errorf("%s requires a value", name)
		}
		if args[index+1] != "-" && strings.HasPrefix(args[index+1], "-") {
			return "", index, fmt.Errorf("%s requires a value before %q", name, args[index+1])
		}
		return args[index+1], index + 1, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if options && a == "--" {
			options = false
			continue
		}
		if !options || a == "-" || !strings.HasPrefix(a, "-") {
			opts.Objects = append(opts.Objects, a)
			continue
		}
		name, inline, hasInline := splitOption(a)
		switch name {
		case "-h", "--help":
			if hasInline {
				return opts, fmt.Errorf("%s does not take a value", name)
			}
			opts.Mode = modeHelp
			return opts, nil
		case "-v", "--version":
			if hasInline {
				return opts, fmt.Errorf("%s does not take a value", name)
			}
			opts.Mode = modeVersion
			return opts, nil
		case "-c", "--config":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			opts.Overrides.ConfigFile = v
		case "-f", "--file":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			opts.File = &v
		case "-t", "--theme":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			if !oneOf(v, "default", "monochrome", "pastel", "dark") {
				return opts, fmt.Errorf("invalid theme %q", v)
			}
			i = next
			opts.Overrides.Theme = v
		case "-l", "--log-file":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			opts.Overrides.LogFile = v
		case "--log-level":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			if !oneOf(v, "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL") {
				return opts, fmt.Errorf("invalid log level %q", v)
			}
			i = next
			opts.Overrides.LogLevel = v
		case "--history-file":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			if v == "" {
				return opts, fmt.Errorf("--history-file must not be empty")
			}
			opts.Overrides.HistoryFile = &v
		case "--history-days":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			if !asciiDigits(v) {
				return opts, fmt.Errorf("--history-days must be a positive integer")
			}
			days, ok := new(big.Int).SetString(v, 10)
			if !ok || days.Sign() < 1 {
				return opts, fmt.Errorf("--history-days must be a positive integer")
			}
			opts.Overrides.HistoryDays = days
		case "--format":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			switch v {
			case "table", "json", "md", "csv":
			default:
				return opts, fmt.Errorf("invalid format %q (choose table, json, md, or csv)", v)
			}
			opts.Format = v
		case "--tcp":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			ports, err := target.ParsePorts(v)
			if err != nil {
				return opts, err
			}
			opts.Ports = ports
		case "--every":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			d, err := parseCLIDuration(v)
			if err != nil {
				return opts, fmt.Errorf("invalid --every duration %q", v)
			}
			opts.Every = &d
		case "--for":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			d, err := parseCLIDuration(v)
			if err != nil {
				return opts, fmt.Errorf("invalid --for duration %q", v)
			}
			opts.For = &d
		case "--since":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			cutoff, ownRunOnly, err := parseSince(v, startUTC)
			if err != nil {
				return opts, err
			}
			opts.Since, opts.OwnRunOnly = &cutoff, ownRunOnly
		case "--list":
			if hasInline {
				return opts, fmt.Errorf("--list does not take a value")
			}
			if selectedMode != 0 && selectedMode != modeList {
				modeConflict = true
			}
			selectedMode, opts.Mode = modeList, modeList
		case "--show", "--follow":
			v, next, err := value(i, inline, hasInline, name)
			if err != nil {
				return opts, err
			}
			i = next
			selected := modeShow
			if name == "--follow" {
				selected = modeFollow
			}
			if selectedMode != 0 && selectedMode != selected {
				modeConflict = true
			}
			selectedMode, opts.Mode, opts.ID = selected, selected, v
		default:
			if unknownOption == "" {
				unknownOption = name
			}
		}
	}
	if len(args) == 0 {
		return opts, fmt.Errorf("no arguments; use --help for usage")
	}
	if unknownOption != "" {
		return opts, fmt.Errorf("unknown option %q", unknownOption)
	}
	if modeConflict {
		return opts, fmt.Errorf("--list, --show, and --follow are mutually exclusive")
	}
	if opts.Mode != modeRun {
		if len(opts.Objects) != 0 || opts.File != nil || len(opts.Ports) != 0 || opts.Every != nil || opts.For != nil {
			return opts, fmt.Errorf("reader modes do not accept targets, --file, --tcp, --every, or --for")
		}
		if opts.Mode == modeList && opts.Since != nil {
			return opts, fmt.Errorf("--list does not accept --since")
		}
		if opts.Overrides.HistoryDays != nil {
			return opts, fmt.Errorf("--history-days applies to target runs only")
		}
	} else if len(opts.Objects) == 0 && opts.File == nil {
		return opts, fmt.Errorf("a target, '-' or --file is required")
	}
	if (opts.Mode == modeShow || opts.Mode == modeFollow) && !validRequestID(opts.ID) {
		return opts, fmt.Errorf("%s requires a 4–64 character hexadecimal request ID", map[commandMode]string{modeShow: "--show", modeFollow: "--follow"}[opts.Mode])
	}
	if opts.Every != nil && *opts.Every < 10*time.Second {
		return opts, fmt.Errorf("--every must be at least 10 seconds")
	}
	if opts.For != nil && *opts.For < 10*time.Second {
		return opts, fmt.Errorf("--for must be at least 10 seconds")
	}
	if opts.File != nil && *opts.File == "" {
		return opts, fmt.Errorf("--file path must not be empty")
	}
	if opts.For != nil && opts.Every != nil && *opts.For < *opts.Every {
		return opts, fmt.Errorf("--for must be at least --every")
	}
	return opts, nil
}

func splitOption(arg string) (name, inline string, hasInline bool) {
	if strings.HasPrefix(arg, "--") {
		if at := strings.IndexByte(arg, '='); at >= 0 {
			return arg[:at], arg[at+1:], true
		}
		return arg, "", false
	}
	if len(arg) > 2 && arg[0] == '-' && arg[1] != '-' {
		if arg[2] == '=' {
			return arg[:2], arg[3:], true
		}
		return arg[:2], arg[2:], true
	}
	return arg, "", false
}

func parseCLIDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if len(s) < 2 || len(s) > 5 || !asciiDigits(s[:len(s)-1]) {
		return 0, fmt.Errorf("invalid duration")
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration")
	}
	unit := strings.ToLower(s[len(s)-1:])
	var factor time.Duration
	switch unit {
	case "s":
		factor = time.Second
	case "m":
		factor = time.Minute
	case "h":
		factor = time.Hour
	case "d":
		factor = 24 * time.Hour
	case "w":
		factor = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration")
	}
	if n > int64((1<<63-1)/factor) {
		return 0, fmt.Errorf("duration too large")
	}
	return time.Duration(n) * factor, nil
}

func parseSince(raw string, startUTC time.Time) (time.Time, bool, error) {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 {
		unit := strings.ToLower(s[len(s)-1:])
		if unit == "m" || unit == "h" || unit == "d" || unit == "w" {
			n, err := parseNumber(s[:len(s)-1])
			if err != nil {
				return time.Time{}, false, fmt.Errorf("invalid --since value %q", raw)
			}
			var d time.Duration
			switch unit {
			case "m":
				d = time.Duration(n) * time.Minute
			case "h":
				d = time.Duration(n) * time.Hour
			case "d":
				d = time.Duration(n) * 24 * time.Hour
			case "w":
				d = time.Duration(n) * 7 * 24 * time.Hour
			}
			return startUTC.UTC().Add(-d), n == 0, nil
		}
	}
	when, err := parseISODateTime(s)
	if err == nil {
		return when, false, nil
	}
	return time.Time{}, false, fmt.Errorf("invalid --since value %q", raw)
}

var (
	isoCalendarDate     = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})(?s:(.*))$`)
	isoBasicDate        = regexp.MustCompile(`^([0-9]{4})([0-9]{2})([0-9]{2})(?s:(.*))$`)
	isoWeekDateExtended = regexp.MustCompile(`^([0-9]{4})-W([0-9]{2})(?:-([1-7]))?(?s:(.*))$`)
	isoWeekDateBasic    = regexp.MustCompile(`^([0-9]{4})W([0-9]{2})(?:([1-7]))?(?s:(.*))$`)
	isoWeekDatePrefix   = regexp.MustCompile(`^[0-9]{4}-W[0-9]{2}`)
)

// parseISODateTime follows datetime.fromisoformat's date/time forms used by the
// reference CLI. Fractions are truncated to microseconds as in Python datetime.
func parseISODateTime(value string) (time.Time, error) {
	date, clock, err := parseISODate(value)
	if err != nil {
		return time.Time{}, err
	}
	if weekDate, weekClock, ok := parseWeekDateHour(value); ok {
		date, clock = weekDate, weekClock
	}
	if clock == "" {
		return date.UTC(), nil
	}
	separator, size := utf8.DecodeRuneInString(clock)
	if separator == utf8.RuneError && size == 1 || size == 0 || len(clock) == size {
		return time.Time{}, fmt.Errorf("invalid ISO date-time")
	}
	clock = clock[size:]
	hour, minute, second, nanos, offset, err := parseISOClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	local := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, second, nanos, time.UTC)
	utc := local.Add(-offset).UTC()
	if utc.Year() < 1 || utc.Year() > 9999 {
		return time.Time{}, fmt.Errorf("ISO date-time outside supported range")
	}
	return utc, nil
}

// parseWeekDateHour handles the fromisoformat ambiguity where a hyphen after
// an extended week date is the datetime separator, not a weekday separator:
// 2026-W41-11 is Monday at 11:00, while 2026-W41-6T11 is Saturday at 11:00.
func parseWeekDateHour(value string) (time.Time, string, bool) {
	prefix := isoWeekDatePrefix.FindString(value)
	if prefix == "" || len(value) == len(prefix) || value[len(prefix)] != '-' {
		return time.Time{}, "", false
	}
	remainder := value[len(prefix):]
	separator, size := utf8.DecodeRuneInString(remainder)
	if separator != '-' || size == 0 || len(remainder) == size {
		return time.Time{}, "", false
	}
	if _, _, _, _, _, err := parseISOClock(remainder[size:]); err != nil {
		return time.Time{}, "", false
	}
	base, _, err := parseISODate(prefix)
	if err != nil {
		return time.Time{}, "", false
	}
	return base, remainder, true
}

func parseISODate(value string) (date time.Time, clock string, err error) {
	for _, pattern := range []*regexp.Regexp{isoCalendarDate, isoBasicDate} {
		match := pattern.FindStringSubmatch(value)
		if match == nil {
			continue
		}
		year, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		day, _ := strconv.Atoi(match[3])
		if year < 1 {
			return time.Time{}, "", fmt.Errorf("invalid ISO year")
		}
		date = time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		if date.Year() != year || int(date.Month()) != month || date.Day() != day {
			return time.Time{}, "", fmt.Errorf("invalid ISO date")
		}
		return date, match[4], nil
	}
	for _, pattern := range []*regexp.Regexp{isoWeekDateExtended, isoWeekDateBasic} {
		match := pattern.FindStringSubmatch(value)
		if match == nil {
			continue
		}
		year, _ := strconv.Atoi(match[1])
		week, _ := strconv.Atoi(match[2])
		if year < 1 {
			return time.Time{}, "", fmt.Errorf("invalid ISO year")
		}
		weekday := 1
		if match[3] != "" {
			weekday, _ = strconv.Atoi(match[3])
		}
		if week < 1 || week > 53 {
			return time.Time{}, "", fmt.Errorf("invalid ISO week")
		}
		jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
		weekOneMonday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
		date = weekOneMonday.AddDate(0, 0, (week-1)*7+weekday-1)
		actualYear, actualWeek := date.ISOWeek()
		if actualYear != year || actualWeek != week {
			return time.Time{}, "", fmt.Errorf("invalid ISO week")
		}
		if date.Year() > 9999 {
			return time.Time{}, "", fmt.Errorf("ISO date outside supported range")
		}
		return date, match[4], nil
	}
	return time.Time{}, "", fmt.Errorf("invalid ISO date")
}

func parseISOClock(value string) (hour, minute, second, nanos int, offset time.Duration, err error) {
	clock := value
	if strings.HasSuffix(clock, "Z") || strings.HasSuffix(clock, "z") {
		clock = clock[:len(clock)-1]
	} else if at := strings.LastIndexAny(clock[1:], "+-"); at >= 0 {
		at++
		offset, err = parseISOOffset(clock[at:])
		if err != nil {
			return 0, 0, 0, 0, 0, err
		}
		clock = clock[:at]
	}

	var fraction string
	if at := strings.IndexAny(clock, ".,"); at >= 0 {
		fraction = clock[at+1:]
		clock = clock[:at]
		if !asciiDigits(fraction) {
			return 0, 0, 0, 0, 0, fmt.Errorf("invalid ISO fraction")
		}
	}
	parts := strings.Split(clock, ":")
	if len(parts) == 1 {
		switch len(clock) {
		case 2:
			parts = []string{clock}
		case 4:
			parts = []string{clock[:2], clock[2:]}
		case 6:
			parts = []string{clock[:2], clock[2:4], clock[4:]}
		default:
			return 0, 0, 0, 0, 0, fmt.Errorf("invalid ISO time")
		}
	} else if len(parts) > 3 {
		return 0, 0, 0, 0, 0, fmt.Errorf("invalid ISO time")
	}
	if len(parts) == 0 || len(parts[0]) != 2 || !asciiDigits(parts[0]) {
		return 0, 0, 0, 0, 0, fmt.Errorf("invalid ISO hour")
	}
	hour, _ = strconv.Atoi(parts[0])
	if len(parts) > 1 {
		if len(parts[1]) != 2 || !asciiDigits(parts[1]) {
			return 0, 0, 0, 0, 0, fmt.Errorf("invalid ISO minute")
		}
		minute, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		if len(parts[2]) != 2 || !asciiDigits(parts[2]) {
			return 0, 0, 0, 0, 0, fmt.Errorf("invalid ISO second")
		}
		second, _ = strconv.Atoi(parts[2])
	}
	if hour > 23 || minute > 59 || second > 59 {
		return 0, 0, 0, 0, 0, fmt.Errorf("ISO time out of range")
	}
	if fraction != "" {
		if len(fraction) > 6 {
			fraction = fraction[:6]
		}
		for len(fraction) < 6 {
			fraction += "0"
		}
		micros, _ := strconv.Atoi(fraction)
		nanos = micros * 1000
	}
	return hour, minute, second, nanos, offset, nil
}

func parseISOOffset(value string) (time.Duration, error) {
	if len(value) < 2 || value[0] != '+' && value[0] != '-' {
		return 0, fmt.Errorf("invalid ISO offset")
	}
	sign := 1
	if value[0] == '-' {
		sign = -1
	}
	text := value[1:]
	var fraction string
	if at := strings.IndexAny(text, ".,"); at >= 0 {
		fraction = text[at+1:]
		text = text[:at]
		if !asciiDigits(fraction) {
			return 0, fmt.Errorf("invalid ISO offset fraction")
		}
	}
	parts := strings.Split(text, ":")
	if len(parts) == 1 {
		switch len(text) {
		case 2:
			parts = []string{text}
		case 4:
			parts = []string{text[:2], text[2:]}
		case 6:
			parts = []string{text[:2], text[2:4], text[4:]}
		default:
			return 0, fmt.Errorf("invalid ISO offset")
		}
	} else if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid ISO offset")
	}
	if len(parts[0]) != 2 || !asciiDigits(parts[0]) {
		return 0, fmt.Errorf("invalid ISO offset hour")
	}
	hours, _ := strconv.Atoi(parts[0])
	minutes, seconds := 0, 0
	if len(parts) > 1 {
		if len(parts[1]) != 2 || !asciiDigits(parts[1]) {
			return 0, fmt.Errorf("invalid ISO offset minute")
		}
		minutes, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		if len(parts[2]) != 2 || !asciiDigits(parts[2]) {
			return 0, fmt.Errorf("invalid ISO offset second")
		}
		seconds, _ = strconv.Atoi(parts[2])
	}
	var micros int
	if fraction != "" {
		if len(fraction) > 6 {
			fraction = fraction[:6]
		}
		for len(fraction) < 6 {
			fraction += "0"
		}
		micros, _ = strconv.Atoi(fraction)
	}
	wholeSeconds := hours*3600 + minutes*60 + seconds
	if wholeSeconds >= 24*60*60 {
		return 0, fmt.Errorf("ISO offset out of range")
	}
	// datetime.fromisoformat preserves fractional offset seconds except when
	// the whole-second offset is zero, which it canonicalizes to UTC.
	if wholeSeconds == 0 {
		micros = 0
	}
	n := time.Duration(wholeSeconds)*time.Second + time.Duration(micros)*time.Microsecond
	return time.Duration(sign) * n, nil
}

func parseNumber(s string) (int64, error) {
	if len(s) < 1 || len(s) > 4 || !asciiDigits(s) {
		return 0, fmt.Errorf("invalid number")
	}
	return strconv.ParseInt(s, 10, 64)
}

func asciiDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validRequestID(id string) bool {
	if len(id) < 4 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func oneOf(s string, choices ...string) bool {
	for _, choice := range choices {
		if s == choice {
			return true
		}
	}
	return false
}
