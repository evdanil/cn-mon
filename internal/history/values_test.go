package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
)

func TestOpenEpochSecondsRoundTripAndBounds(t *testing.T) {
	cases := []time.Time{
		time.Time{},
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1969, 12, 31, 23, 59, 59, 125_000_000, time.FixedZone("offset", 90*60)),
		time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	}
	if _, err := epochSeconds(time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC)); err == nil {
		t.Fatal("epochSeconds accepted a value that rounds into year 10000")
	}
	for _, in := range cases {
		seconds, err := epochSeconds(in)
		if err != nil {
			t.Fatalf("epochSeconds(%s): %v", in, err)
		}
		got, err := timeFromEpoch(seconds)
		if err != nil {
			t.Fatalf("timeFromEpoch(%v): %v", seconds, err)
		}
		want := in.UTC().Round(time.Nanosecond)
		if !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("round trip %s: got %s (%s), want %s UTC", in, got, got.Location(), want)
		}
	}
}

func TestOpenEpochSecondsRejectsNonfiniteAndOutOfRange(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -62135596801, 253402300800} {
		if _, err := timeFromEpoch(value); err == nil {
			t.Errorf("timeFromEpoch(%v) accepted out-of-range value", value)
		}
	}
}

func TestOpenDurationConversionsPreserveLargeIntervalsAndRejectOverflow(t *testing.T) {
	large := time.Duration(9999 * 7 * 24 * time.Hour)
	seconds, err := durationSeconds(large)
	if err != nil {
		t.Fatal(err)
	}
	got, err := durationFromSeconds(seconds)
	if err != nil {
		t.Fatal(err)
	}
	if got != large {
		t.Fatalf("large duration round trip: got %v, want %v", got, large)
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), float64(math.MaxInt64)/float64(time.Second) + 10} {
		if _, err := durationFromSeconds(value); err == nil {
			t.Errorf("durationFromSeconds(%v) accepted invalid duration", value)
		}
	}
	if _, err := durationSeconds(time.Duration(math.MaxInt64)); err == nil {
		t.Fatal("durationSeconds accepted a float encoding that cannot be decoded")
	}
}

func TestOpenRequestJSONPreservesOrderAndNoTCPEmptyArray(t *testing.T) {
	req := Request{RequestMeta: RequestMeta{Key: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Targets: []string{"z", "a"}}, Hosts: []string{"host-z", "host-a"}}
	targets, ports, hosts, err := encodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if targets != `["z","a"]` || ports != `[]` || hosts != `["host-z","host-a"]` {
		t.Fatalf("encoded arrays lost order or nil/empty semantics: %q %q %q", targets, ports, hosts)
	}
	decoded, err := decodeRequest(1, req.Key, targets, ports, hosts)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.TCPPorts) != 0 || decoded.TCPPorts == nil || decoded.Targets[0] != "z" || decoded.Hosts[0] != "host-z" {
		t.Fatalf("decoded request mismatch: %#v", decoded)
	}
	var parsed []string
	if err := json.Unmarshal([]byte(targets), &parsed); err != nil || parsed[0] != "z" {
		t.Fatalf("not standard JSON array: %v %v", parsed, err)
	}
}

func TestOpenRequestEncoderNormalizesNilArrays(t *testing.T) {
	request := Request{RequestMeta: RequestMeta{Key: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}
	targets, ports, hosts, err := encodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if targets != `[]` || ports != `[]` || hosts != `[]` {
		t.Fatalf("nil request arrays encoded as %q %q %q, want []", targets, ports, hosts)
	}
}

func TestOpenDecodeRequestMetaDoesNotNeedHosts(t *testing.T) {
	key := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	got, err := decodeRequestMeta(3, key, `["name","10.0.0.1"]`, `[80,443]`)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 3 || got.Targets[0] != "name" || got.TCPPorts[1] != 443 {
		t.Fatalf("decoded metadata mismatch: %#v", got)
	}
}

func TestOpenRequestCodecRejectsUnpairedSurrogatesWithoutRejectingValidUnicode(t *testing.T) {
	key := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	for _, targets := range []string{`["\ud800"]`, `["\udc00"]`} {
		if _, err := decodeRequestMeta(1, key, targets, `[]`); err == nil {
			t.Errorf("accepted unpaired request string %s", targets)
		}
	}
	got, err := decodeRequestMeta(1, key, `["\ud83d\ude00","�"]`, `[]`)
	if err != nil || len(got.Targets) != 2 || got.Targets[0] != "😀" || got.Targets[1] != "�" {
		t.Fatalf("valid Unicode changed: %#v err=%v", got.Targets, err)
	}
}

func TestOpenRequestCodecsRejectMalformedOrInvalidIdentity(t *testing.T) {
	validKey := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	for _, key := range []string{"", "abcd", "g" + validKey[1:], validKey + "0"} {
		req := Request{RequestMeta: RequestMeta{Key: key}, Hosts: []string{"host"}}
		if _, _, _, err := encodeRequest(req); err == nil {
			t.Errorf("encodeRequest accepted key %q", key)
		}
	}
	for name, input := range map[string]struct{ targets, ports, hosts string }{
		"null arrays":     {`null`, `[]`, `["h"]`},
		"wrong elem":      {`[1]`, `[]`, `["h"]`},
		"null host":       {`[]`, `[]`, `null`},
		"empty host":      {`[]`, `[]`, `[""]`},
		"duplicate host":  {`[]`, `[]`, `["h","h"]`},
		"bad port":        {`[]`, `[65536]`, `["h"]`},
		"fractional port": {`[]`, `[80.5]`, `["h"]`},
		"wrong id":        {`[]`, `[]`, `["h"]`},
	} {
		t.Run(name, func(t *testing.T) {
			id := int64(1)
			if name == "wrong id" {
				id = 0
			}
			if _, err := decodeRequest(id, validKey, input.targets, input.ports, input.hosts); err == nil {
				t.Fatal("decodeRequest accepted malformed value")
			}
		})
	}
}

func TestOpenTCPCodecPreservesNilEmptyAndArbitraryText(t *testing.T) {
	null, err := encodeTCP(nil)
	if err != nil || null.Valid {
		t.Fatalf("nil map should encode as SQL NULL: %#v %v", null, err)
	}
	empty, err := encodeTCP(map[int]string{})
	if err != nil || !empty.Valid || empty.String != `{}` {
		t.Fatalf("empty map should encode as {}: %#v %v", empty, err)
	}
	values := map[int]string{80: "open", 443: "legacy custom detail", 65535: ""}
	encoded, err := encodeTCP(values)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeTCP(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for port, want := range values {
		if got[port] != want {
			t.Errorf("port %d: got %q, want %q", port, got[port], want)
		}
	}
	if _, err := decodeTCP(sql.NullString{String: `[]`, Valid: true}); err == nil {
		t.Fatal("TCP array accepted instead of object")
	}
	if _, err := decodeTCP(sql.NullString{String: `{"80":null}`, Valid: true}); err == nil {
		t.Fatal("TCP null value accepted instead of string")
	}
	if _, err := decodeTCP(sql.NullString{String: `{"80":"\ud800"}`, Valid: true}); err == nil {
		t.Fatal("TCP unpaired surrogate accepted")
	}
	valid, err := decodeTCP(sql.NullString{String: `{"80":"\ud83d\ude00"}`, Valid: true})
	if err != nil || valid[80] != "😀" {
		t.Fatalf("valid TCP Unicode changed: %#v err=%v", valid, err)
	}
}

func TestOpenValidateRoundRequiresExactlySeenRows(t *testing.T) {
	hosts := []string{"a", "b", "c", "d"}
	valid := RoundInput{Observations: "!RT.", Rows: []ResultRow{
		{Host: "a", Address: "192.0.2.1", Result: "OK"},
		{Host: "b", Address: "192.0.2.2", Result: "REJECTED"},
		{Host: "c", Address: "192.0.2.3", Result: "open"},
	}}
	if err := validateRound(hosts, valid); err != nil {
		t.Fatalf("valid sparse seen rows rejected: %v", err)
	}
	bad := []RoundInput{
		{Observations: "!RT", Rows: valid.Rows},
		{Observations: "!RTz", Rows: valid.Rows},
		{Observations: "!RT.", Rows: append(append([]ResultRow(nil), valid.Rows...), ResultRow{Host: "d"})},
		{Observations: "!RT.", Rows: valid.Rows[:2]},
		{Observations: "!RT.", Rows: []ResultRow{valid.Rows[0], valid.Rows[0], valid.Rows[2]}},
		{Observations: "!RZ.", Rows: valid.Rows},
	}
	for i, round := range bad {
		if err := validateRound(hosts, round); err == nil {
			t.Errorf("invalid round %d accepted", i)
		}
	}
	if err := validateRound([]string{"a", "a"}, valid); err == nil {
		t.Fatal("duplicate request hosts accepted")
	}
}

func TestOpenNormalizeDBErrorPreservesCauseOnlyForInterruptedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	busy := error(sqlite3.BUSY)
	got := normalizeDBError(ctx, busy)
	if !errors.Is(got, context.Canceled) || !errors.Is(got, busy) {
		t.Fatalf("interrupted busy error lost causes: %v", got)
	}
	constraint := error(sqlite3.CONSTRAINT)
	got = normalizeDBError(ctx, constraint)
	if errors.Is(got, context.Canceled) || !errors.Is(got, constraint) {
		t.Fatalf("unrelated constraint was relabeled: %v", got)
	}
}
