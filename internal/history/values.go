package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

const (
	minUnixSeconds = int64(-62135596800) // 0001-01-01T00:00:00Z
	maxUnixSeconds = int64(253402300800) // 10000-01-01T00:00:00Z, exclusive
)

func epochSeconds(value time.Time) (float64, error) {
	value = value.UTC()
	if value.Year() < 1 || value.Year() > 9999 {
		return 0, fmt.Errorf("time %s is outside years 1 through 9999", value)
	}
	seconds := float64(value.Unix()) + float64(value.Nanosecond())/float64(time.Second)
	if math.IsInf(seconds, 0) || math.IsNaN(seconds) {
		return 0, fmt.Errorf("time %s has an invalid epoch value", value)
	}
	if _, err := timeFromEpoch(seconds); err != nil {
		return 0, fmt.Errorf("time %s cannot be represented as epoch seconds: %w", value, err)
	}
	return seconds, nil
}

func timeFromEpoch(seconds float64) (time.Time, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < float64(minUnixSeconds) || seconds >= float64(maxUnixSeconds) {
		return time.Time{}, fmt.Errorf("epoch seconds %v are outside years 1 through 9999", seconds)
	}
	whole := math.Floor(seconds)
	fraction := seconds - whole
	if whole < float64(minUnixSeconds) || whole >= float64(maxUnixSeconds) {
		return time.Time{}, fmt.Errorf("epoch seconds %v are outside years 1 through 9999", seconds)
	}
	sec := int64(whole)
	nanos := int64(math.Round(fraction * float64(time.Second)))
	if nanos == int64(time.Second) {
		sec++
		nanos = 0
	}
	if sec < minUnixSeconds || sec >= maxUnixSeconds {
		return time.Time{}, fmt.Errorf("epoch seconds %v round outside years 1 through 9999", seconds)
	}
	value := time.Unix(sec, nanos).UTC()
	if value.Year() < 1 || value.Year() > 9999 {
		return time.Time{}, fmt.Errorf("epoch seconds %v round outside years 1 through 9999", seconds)
	}
	return value, nil
}

func durationSeconds(value time.Duration) (float64, error) {
	if value < 0 {
		return 0, fmt.Errorf("negative duration %s", value)
	}
	seconds := float64(value) / float64(time.Second)
	if _, err := durationFromSeconds(seconds); err != nil {
		return 0, fmt.Errorf("duration %s cannot be represented as epoch seconds: %w", value, err)
	}
	return seconds, nil
}

func durationFromSeconds(seconds float64) (time.Duration, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, fmt.Errorf("invalid duration seconds %v", seconds)
	}
	whole := math.Floor(seconds)
	if whole > float64(math.MaxInt64/int64(time.Second)) {
		return 0, fmt.Errorf("duration seconds %v exceed time.Duration", seconds)
	}
	sec := int64(whole)
	nanos := int64(math.Round((seconds - whole) * float64(time.Second)))
	if nanos == int64(time.Second) {
		sec++
		nanos = 0
	}
	max := int64(math.MaxInt64)
	maxSec, maxNanos := max/int64(time.Second), max%int64(time.Second)
	if sec > maxSec || (sec == maxSec && nanos > maxNanos) {
		return 0, fmt.Errorf("duration seconds %v exceed time.Duration", seconds)
	}
	return time.Duration(sec*int64(time.Second) + nanos), nil
}

// JSON boundary behavior adapts _dump/_dump_tcp/_load_tcp in
// cn_tool/utils/ping_store.py:196–215, from cn-tool v0.8.0 commit
// 2e2f3c40ef6718486f1ca4477f9145029ec495fa (Apache-2.0); validation here
// additionally prevents Go JSON decoding from silently changing stored text.
func encodeRequest(request Request) (targets, ports, hosts string, err error) {
	targetValues := nonNilStrings(request.Targets)
	portValues := nonNilInts(request.TCPPorts)
	hostValues := nonNilStrings(request.Hosts)
	if !validRequestKey(request.Key) {
		return "", "", "", fmt.Errorf("invalid request key")
	}
	if err := validateStringList(targetValues, false, "targets"); err != nil {
		return "", "", "", err
	}
	if err := validatePorts(portValues); err != nil {
		return "", "", "", err
	}
	if err := validateStringList(hostValues, true, "hosts"); err != nil {
		return "", "", "", err
	}
	encode := func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode request JSON: %w", err)
		}
		return string(encoded), nil
	}
	if targets, err = encode(targetValues); err != nil {
		return "", "", "", err
	}
	if ports, err = encode(portValues); err != nil {
		return "", "", "", err
	}
	if hosts, err = encode(hostValues); err != nil {
		return "", "", "", err
	}
	return targets, ports, hosts, nil
}

func decodeRequestMeta(id int64, key, targets, ports string) (RequestMeta, error) {
	if id <= 0 {
		return RequestMeta{}, fmt.Errorf("invalid request id %d", id)
	}
	if !validRequestKey(key) {
		return RequestMeta{}, fmt.Errorf("invalid request key")
	}
	var out RequestMeta
	out.ID, out.Key = id, key
	if err := decodeJSONArray(targets, &out.Targets); err != nil {
		return RequestMeta{}, fmt.Errorf("invalid request targets: %w", err)
	}
	if err := decodeJSONArray(ports, &out.TCPPorts); err != nil {
		return RequestMeta{}, fmt.Errorf("invalid request TCP ports: %w", err)
	}
	if err := validateStringList(out.Targets, false, "targets"); err != nil {
		return RequestMeta{}, err
	}
	if err := validatePorts(out.TCPPorts); err != nil {
		return RequestMeta{}, err
	}
	return out, nil
}

func decodeRequest(id int64, key, targets, ports, hosts string) (Request, error) {
	meta, err := decodeRequestMeta(id, key, targets, ports)
	if err != nil {
		return Request{}, err
	}
	var decodedHosts []string
	if err := decodeJSONArray(hosts, &decodedHosts); err != nil {
		return Request{}, fmt.Errorf("invalid request hosts: %w", err)
	}
	if err := validateStringList(decodedHosts, true, "hosts"); err != nil {
		return Request{}, err
	}
	return Request{RequestMeta: meta, Hosts: decodedHosts}, nil
}

func encodeTCP(tcp map[int]string) (sql.NullString, error) {
	if tcp == nil {
		return sql.NullString{}, nil
	}
	encoded := make(map[string]string, len(tcp))
	for port, state := range tcp {
		if port < 1 || port > 65535 {
			return sql.NullString{}, fmt.Errorf("invalid TCP port %d", port)
		}
		if !utf8.ValidString(state) {
			return sql.NullString{}, fmt.Errorf("TCP state for port %d is not valid UTF-8", port)
		}
		encoded[strconv.Itoa(port)] = state
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode TCP JSON: %w", err)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

func decodeTCP(text sql.NullString) (map[int]string, error) {
	if !text.Valid {
		return nil, nil
	}
	if !utf8.ValidString(text.String) {
		return nil, fmt.Errorf("TCP JSON is not valid UTF-8")
	}
	if err := rejectInvalidSurrogateEscapes(text.String); err != nil {
		return nil, err
	}
	var encoded map[string]*string
	if err := json.Unmarshal([]byte(text.String), &encoded); err != nil {
		return nil, fmt.Errorf("invalid TCP JSON: %w", err)
	}
	if encoded == nil {
		return nil, fmt.Errorf("TCP JSON must be an object")
	}
	decoded := make(map[int]string, len(encoded))
	for key, value := range encoded {
		if value == nil {
			return nil, fmt.Errorf("TCP state for port key %q must be a string", key)
		}
		port, err := strconv.Atoi(key)
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != key {
			return nil, fmt.Errorf("invalid TCP JSON port key %q", key)
		}
		if !utf8.ValidString(*value) {
			return nil, fmt.Errorf("TCP state for port %d is not valid UTF-8", port)
		}
		decoded[port] = *value
	}
	return decoded, nil
}

func validateRound(hosts []string, round RoundInput) error {
	if _, err := epochSeconds(round.Started); err != nil {
		return fmt.Errorf("invalid round start: %w", err)
	}
	if len(round.Observations) != len(hosts) {
		return fmt.Errorf("observation length %d does not match host count %d", len(round.Observations), len(hosts))
	}
	positions := make(map[string]int, len(hosts))
	for i, host := range hosts {
		if host == "" || !utf8.ValidString(host) {
			return fmt.Errorf("invalid host at position %d", i)
		}
		if _, ok := positions[host]; ok {
			return fmt.Errorf("duplicate host %q", host)
		}
		positions[host] = i
	}
	seen := make(map[string]bool, len(round.Rows))
	for i := 0; i < len(round.Observations); i++ {
		observation := round.Observations[i]
		if !strings.ContainsRune("!RTU.E?", rune(observation)) {
			return fmt.Errorf("invalid observation byte %q at position %d", observation, i)
		}
	}
	for _, row := range round.Rows {
		position, ok := positions[row.Host]
		if !ok {
			return fmt.Errorf("result row has unknown host %q", row.Host)
		}
		if seen[row.Host] {
			return fmt.Errorf("duplicate result row for host %q", row.Host)
		}
		seen[row.Host] = true
		if !strings.ContainsRune("!RT", rune(round.Observations[position])) {
			return fmt.Errorf("unseen host %q has a result row", row.Host)
		}
		if !utf8.ValidString(row.Host) || !utf8.ValidString(row.Address) || !utf8.ValidString(row.Result) {
			return fmt.Errorf("result row for %q contains invalid UTF-8", row.Host)
		}
		if _, err := epochSeconds(row.Time); err != nil {
			return fmt.Errorf("invalid sample time for %q: %w", row.Host, err)
		}
		if _, err := encodeTCP(row.TCP); err != nil {
			return fmt.Errorf("invalid TCP data for %q: %w", row.Host, err)
		}
	}
	for i := 0; i < len(round.Observations); i++ {
		if strings.ContainsRune("!RT", rune(round.Observations[i])) && !seen[hosts[i]] {
			return fmt.Errorf("seen host %q has no result row", hosts[i])
		}
	}
	return nil
}

func normalizeDBError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx == nil || ctx.Err() == nil {
		return err
	}
	if singleCauseIs(err, sqlite3.BUSY) || singleCauseIs(err, sqlite3.INTERRUPT) {
		return &canceledDBError{cause: ctx.Err(), operation: err}
	}
	return err
}

func singleCauseIs(err, target error) bool {
	for err != nil {
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return false
		}
		if unwrapped, ok := err.(interface{ Unwrap() error }); ok {
			err = unwrapped.Unwrap()
			continue
		}
		return errors.Is(err, target)
	}
	return false
}

func validRequestKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func validateStringList(values []string, unique bool, field string) error {
	if values == nil {
		return fmt.Errorf("%s must be a JSON array, not null", field)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || !utf8.ValidString(value) {
			return fmt.Errorf("%s contains an empty or invalid UTF-8 string", field)
		}
		if unique {
			if _, ok := seen[value]; ok {
				return fmt.Errorf("%s contains duplicate value %q", field, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

func validatePorts(ports []int) error {
	if ports == nil {
		return fmt.Errorf("TCP ports must be a JSON array, not null")
	}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid TCP port %d", port)
		}
	}
	return nil
}

func decodeJSONArray(text string, destination any) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("JSON is not valid UTF-8")
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return fmt.Errorf("expected JSON array")
	}
	if err := rejectInvalidSurrogateEscapes(text); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return fmt.Errorf("trailing JSON value")
}

func rejectInvalidSurrogateEscapes(text string) error {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 >= len(text) {
			continue
		}
		if text[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(text) {
			return fmt.Errorf("incomplete Unicode escape")
		}
		code, err := strconv.ParseUint(text[i+2:i+6], 16, 16)
		if err != nil {
			return fmt.Errorf("invalid Unicode escape")
		}
		switch {
		case code >= 0xD800 && code <= 0xDBFF:
			if i+12 > len(text) || text[i+6:i+8] != `\u` {
				return fmt.Errorf("unpaired high surrogate")
			}
			low, err := strconv.ParseUint(text[i+8:i+12], 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return fmt.Errorf("unpaired high surrogate")
			}
			i += 11
		case code >= 0xDC00 && code <= 0xDFFF:
			return fmt.Errorf("unpaired low surrogate")
		default:
			i += 5
		}
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilInts(values []int) []int {
	if values == nil {
		return []int{}
	}
	return values
}
