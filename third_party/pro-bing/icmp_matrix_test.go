// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package probing

import (
	"encoding/binary"
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestICMPErrorQuoteTypeFamilyMatrix(t *testing.T) {
	data := []byte{0xde, 0xad, 0xbe, 0xef}
	cases := []struct {
		name    string
		family  int
		typ     icmp.Type
		body    icmp.MessageBody
		allowed bool
	}{
		{"v4 destination unreachable", 4, ipv4.ICMPTypeDestinationUnreachable, &icmp.DstUnreach{Data: data}, true},
		{"v4 time exceeded", 4, ipv4.ICMPTypeTimeExceeded, &icmp.TimeExceeded{Data: data}, true},
		{"v4 parameter problem", 4, ipv4.ICMPTypeParameterProblem, &icmp.ParamProb{Data: data}, true},
		{"v6 destination unreachable", 6, ipv6.ICMPTypeDestinationUnreachable, &icmp.DstUnreach{Data: data}, true},
		{"v6 packet too big", 6, ipv6.ICMPTypePacketTooBig, &icmp.PacketTooBig{Data: data}, true},
		{"v6 time exceeded", 6, ipv6.ICMPTypeTimeExceeded, &icmp.TimeExceeded{Data: data}, true},
		{"v6 parameter problem", 6, ipv6.ICMPTypeParameterProblem, &icmp.ParamProb{Data: data}, true},
		{"v4 rejects echo", 4, ipv4.ICMPTypeEchoReply, &icmp.Echo{}, false},
		{"v6 rejects echo", 6, ipv6.ICMPTypeEchoReply, &icmp.Echo{}, false},
		{"v4 rejects v6-only packet-too-big", 4, ipv6.ICMPTypePacketTooBig, &icmp.PacketTooBig{Data: data}, false},
		{"v6 rejects v4 time-exceeded", 6, ipv4.ICMPTypeTimeExceeded, &icmp.TimeExceeded{Data: data}, false},
		{"rejects unknown family", 5, ipv4.ICMPTypeDestinationUnreachable, &icmp.DstUnreach{Data: data}, false},
		{"rejects wrong body", 4, ipv4.ICMPTypeDestinationUnreachable, &icmp.Echo{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quote, ok := icmpErrorQuote(tc.family, &icmp.Message{Type: tc.typ, Body: tc.body})
			if ok != tc.allowed {
				t.Fatalf("icmpErrorQuote(family=%d,type=%v) accepted=%v, want %v", tc.family, tc.typ, ok, tc.allowed)
			}
			if ok && string(quote) != string(data) {
				t.Fatalf("quote = %x, want %x", quote, data)
			}
		})
	}
}

func TestQuotedIPv4MinimumAndHeaderValidation(t *testing.T) {
	minimum := matrixIPv4Quote(net.ParseIP("192.0.2.2"), 123, 17, nil)
	got, ok := parseQuotedEcho(4, minimum)
	if !ok || got.id != 123 || got.seq != 17 || !got.destination.Equal(net.ParseIP("192.0.2.2")) {
		t.Fatalf("minimum IPv4 quote parsed as %+v, ok=%v", got, ok)
	}
	for _, tc := range []struct {
		name string
		edit func([]byte)
	}{
		{"bad version", func(b []byte) { b[0] = 0x65 }},
		{"ihl below minimum", func(b []byte) { b[0] = 0x44 }},
		{"declared length shorter than header and echo", func(b []byte) { binary.BigEndian.PutUint16(b[2:4], 27) }},
		{"wrong protocol", func(b []byte) { b[9] = 17 }},
		{"noninitial fragment", func(b []byte) { binary.BigEndian.PutUint16(b[6:8], 1) }},
		{"not echo request", func(b []byte) { b[20] = byte(ipv4.ICMPTypeEchoReply) }},
		{"nonzero echo code", func(b []byte) { b[21] = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := append([]byte(nil), minimum...)
			tc.edit(bad)
			if _, ok := parseQuotedEcho(4, bad); ok {
				t.Fatalf("malformed quote was accepted: %x", bad)
			}
		})
	}
	for n := 0; n < len(minimum); n++ {
		if _, ok := parseQuotedEcho(4, minimum[:n]); ok {
			t.Fatalf("truncated IPv4 quote of %d bytes was accepted", n)
		}
	}
}

func TestQuotedIPv6ExtensionChainsAndRejectsUnsupportedHeaders(t *testing.T) {
	chains := []struct {
		name  string
		types []byte
		final bool
	}{
		{"hop-by-hop", []byte{0}, true},
		{"routing", []byte{43}, true},
		{"destination options", []byte{60}, true},
		{"initial fragment", []byte{44}, true},
		{"authentication header", []byte{51}, true},
		{"combined chain", []byte{0, 43, 60, 51}, true},
		{"noninitial fragment", []byte{44, 0xff}, false},
		{"unknown next header", []byte{99}, false},
		{"ESP", []byte{50}, false},
	}

	for _, size := range []int{8, 12} {
		t.Run("malformed AH length "+strconv.Itoa(size), func(t *testing.T) {
			if _, ok := parseQuotedEcho(6, matrixIPv6AHQuote(net.ParseIP("2001:db8::2"), 321, 43, nil, size)); ok {
				t.Fatalf("invalid %d-byte AH header was accepted", size)
			}
		})
	}
	for _, tc := range chains {
		t.Run(tc.name, func(t *testing.T) {
			quote := matrixIPv6Quote(net.ParseIP("2001:db8::2"), 321, 43, nil, tc.types)
			if !tc.final {
				binary.BigEndian.PutUint16(quote[42:44], 8)
			}
			got, ok := parseQuotedEcho(6, quote)
			if ok != tc.final {
				t.Fatalf("parseQuotedEcho accepted=%v, want %v; echo=%+v quote=%x", ok, tc.final, got, quote)
			}
			if tc.final && (got.id != 321 || got.seq != 43 || !got.destination.Equal(net.ParseIP("2001:db8::2"))) {
				t.Fatalf("extension quote parsed as %+v", got)
			}
		})
	}
	// The parser's documented traversal limit is 16 extension headers.
	tooDeep := make([]byte, 17)
	for i := range tooDeep {
		tooDeep[i] = 0
	}
	if _, ok := parseQuotedEcho(6, matrixIPv6Quote(net.ParseIP("2001:db8::2"), 1, 1, nil, tooDeep)); ok {
		t.Fatal("IPv6 extension chain beyond the parser bound was accepted")
	}
	truncated := matrixIPv6Quote(net.ParseIP("2001:db8::2"), 1, 1, nil, []byte{0})
	truncated = truncated[:41]
	if _, ok := parseQuotedEcho(6, truncated); ok {
		t.Fatal("truncated IPv6 extension header was accepted")
	}
}

func TestProcessPacketAttributesEverySupportedOuterErrorType(t *testing.T) {
	cases := []struct {
		family int
		typ    icmp.Type
	}{
		{4, ipv4.ICMPTypeDestinationUnreachable},
		{4, ipv4.ICMPTypeTimeExceeded},
		{4, ipv4.ICMPTypeParameterProblem},
		{6, ipv6.ICMPTypeDestinationUnreachable},
		{6, ipv6.ICMPTypePacketTooBig},
		{6, ipv6.ICMPTypeTimeExceeded},
		{6, ipv6.ICMPTypeParameterProblem},
	}
	for _, tc := range cases {
		t.Run(icmpTypeCaseName(tc.family, tc.typ), func(t *testing.T) {
			p, target, tracker := matrixPinger(tc.family, 19)
			quote := matrixQuote(tc.family, target.IP, p.id, 19, matrixPayload(t, tracker))
			sender := matrixOtherAddress(tc.family)
			var event *ICMPError
			p.OnICMPError = func(got *ICMPError) { event = got }
			packet := matrixErrorPacket(t, tc.family, tc.typ, quote, sender)
			if err := p.processPacket(&packet); err != nil {
				t.Fatalf("processPacket: %v", err)
			}
			if event == nil || event.Family != tc.family || event.Type != icmpTypeCode(tc.typ) || event.Sender == nil || !event.Sender.IP.Equal(sender.IP) || event.Destination == nil || !event.Destination.IP.Equal(target.IP) || event.ID != p.id || event.Seq != 19 {
				t.Fatalf("attributed error = %+v", event)
			}
			if !event.ReceivedAt.IsZero() {
				return
			}
			t.Fatal("callback did not record receive time")
		})
	}
}

func TestProcessPacketSeparatesTargetAndIntermediateDeduplicationPerRequest(t *testing.T) {
	p, target, tracker := matrixPinger(4, 30, 31)
	var events []*ICMPError
	p.OnICMPError = func(event *ICMPError) { events = append(events, event) }
	other := &net.IPAddr{IP: net.ParseIP("192.0.2.254")}
	for _, seq := range []int{31, 30} {
		quote := matrixQuote(4, target.IP, p.id, seq, matrixPayload(t, tracker))
		for _, sender := range []*net.IPAddr{other, other, target, target} {
			packet := matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote, sender)
			if err := p.processPacket(&packet); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(events) != 4 {
		t.Fatalf("callbacks = %d, want one target and one intermediate event for each sent request: %+v", len(events), events)
	}
	if got := []int{events[0].Seq, events[1].Seq, events[2].Seq, events[3].Seq}; !reflect.DeepEqual(got, []int{31, 31, 30, 30}) {
		t.Fatalf("deduplicated callback sequence order = %v", got)
	}
	if events[0].Sender.IP.Equal(target.IP) || !events[1].Sender.IP.Equal(target.IP) || events[2].Sender.IP.Equal(target.IP) || !events[3].Sender.IP.Equal(target.IP) {
		t.Fatalf("deduplication collapsed sender classes or request entries: %+v", events)
	}
}

func TestMinimumQuoteCorrelatesFromSuccessfulSendLedger(t *testing.T) {
	p, target, _ := matrixPinger(4, 39)
	var event *ICMPError
	p.OnICMPError = func(got *ICMPError) { event = got }
	quote := matrixIPv4Quote(target.IP, p.id, 39, nil)
	if len(quote) != 28 {
		t.Fatalf("minimum IPv4 quote length=%d, want IPv4 header plus ICMP echo header", len(quote))
	}
	packet := matrixErrorPacket(t, 4, ipv4.ICMPTypeTimeExceeded, quote, &net.IPAddr{IP: net.ParseIP("192.0.2.254")})
	if err := p.processPacket(&packet); err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Seq != 39 || event.ID != p.id {
		t.Fatalf("minimum quote did not correlate through the send ledger: event=%v", event)
	}
}

func TestMalformedAndSenderlessErrorsDoNotConsumeLedgerDedup(t *testing.T) {
	p, target, tracker := matrixPinger(4, 44)
	quote := matrixQuote(4, target.IP, p.id, 44, matrixPayload(t, tracker))
	var events []*ICMPError
	p.OnICMPError = func(event *ICMPError) { events = append(events, event) }

	malformed := matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote[:12], &net.IPAddr{IP: net.ParseIP("192.0.2.254")})
	if err := p.processPacket(&malformed); err != nil {
		t.Fatal(err)
	}
	validQuoteSenderless := matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote, nil)
	validQuoteSenderless.addr = &net.TCPAddr{IP: net.ParseIP("192.0.2.254"), Port: 80}
	if err := p.processPacket(&validQuoteSenderless); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("malformed or unsupported sender generated attribution: %+v", events)
	}
	valid := matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote, &net.IPAddr{IP: net.ParseIP("192.0.2.254")})
	if err := p.processPacket(&valid); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("ignored error consumed ledger dedup state: callbacks=%d", len(events))
	}
}

func TestQuotedErrorRejectsWrongFamilyAddressIDSequenceAndUUID(t *testing.T) {
	cases := []struct {
		name   string
		quote  func(*testing.T, *Pinger, net.IP, uuid.UUID) []byte
		family int
	}{
		{"wrong destination", func(t *testing.T, p *Pinger, _ net.IP, tracker uuid.UUID) []byte {
			return matrixQuote(4, net.ParseIP("192.0.2.3"), p.id, 42, matrixPayload(t, tracker))
		}, 4},
		{"wrong identifier", func(t *testing.T, p *Pinger, target net.IP, tracker uuid.UUID) []byte {
			return matrixQuote(4, target, p.id+1, 42, matrixPayload(t, tracker))
		}, 4},
		{"wrong sequence", func(t *testing.T, p *Pinger, target net.IP, tracker uuid.UUID) []byte {
			return matrixQuote(4, target, p.id, 43, matrixPayload(t, tracker))
		}, 4},
		{"wrong UUID", func(t *testing.T, p *Pinger, target net.IP, _ uuid.UUID) []byte {
			return matrixQuote(4, target, p.id, 42, matrixPayload(t, uuid.New()))
		}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, target, tracker := matrixPinger(4, 42)
			called := false
			p.OnICMPError = func(*ICMPError) { called = true }
			quote := tc.quote(t, p, target.IP, tracker)
			packet := matrixErrorPacket(t, tc.family, ipv4.ICMPTypeDestinationUnreachable, quote, &net.IPAddr{IP: net.ParseIP("192.0.2.254")})
			if err := p.processPacket(&packet); err != nil {
				t.Fatal(err)
			}
			if called {
				t.Fatal("unmatched quote produced an ICMPError callback")
			}
		})
	}

	t.Run("wrong family type", func(t *testing.T) {
		p, target, tracker := matrixPinger(4, 42)
		called := false
		p.OnICMPError = func(*ICMPError) { called = true }
		quote := matrixQuote(4, target.IP, p.id, 42, matrixPayload(t, tracker))
		message := &icmp.Message{Type: ipv6.ICMPTypeDestinationUnreachable, Code: 0, Body: &icmp.DstUnreach{Data: quote}}
		p.processICMPError(message, &net.IPAddr{IP: net.ParseIP("192.0.2.254")}, time.Now())
		if called {
			t.Fatal("wrong-family outer type produced callback")
		}
	})
}

func TestErrorAfterEchoStillCorrelatesFromSentLedger(t *testing.T) {
	p, target, tracker := matrixPinger(4, 51)
	p.awaitingSequences[tracker][51] = struct{}{}
	var replies, errorsSeen int
	p.OnRecv = func(*Packet) { replies++ }
	p.OnICMPError = func(*ICMPError) { errorsSeen++ }
	payload := matrixPayload(t, tracker)
	echo := &icmp.Message{Type: ipv4.ICMPTypeEchoReply, Code: 0, Body: &icmp.Echo{ID: p.id, Seq: 51, Data: payload}}
	echoBytes, err := echo.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	replyPacket := makeTestPacket(echoBytes)
	replyPacket.addr = target
	if err := p.processPacket(&replyPacket); err != nil {
		t.Fatal(err)
	}
	if replies != 1 || len(p.sentRequests) != 1 {
		t.Fatalf("after echo: replies=%d sent-ledger=%+v", replies, p.sentRequests)
	}
	quote := matrixQuote(4, target.IP, p.id, 51, payload)
	errorPacket := matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote, &net.IPAddr{IP: net.ParseIP("192.0.2.254")})
	if err := p.processPacket(&errorPacket); err != nil {
		t.Fatal(err)
	}
	if errorsSeen != 1 || len(p.sentRequests) != 1 {
		t.Fatalf("error after echo callbacks=%d sent-ledger=%+v", errorsSeen, p.sentRequests)
	}
}

func TestProcessPacketUsesReceivedPrefixAndCallbackOwnsAddresses(t *testing.T) {
	p, target, tracker := matrixPinger(4, 61)
	var event *ICMPError
	p.OnICMPError = func(got *ICMPError) { event = got }
	quote := matrixQuote(4, target.IP, p.id, 61, matrixPayload(t, tracker))
	sender := &net.IPAddr{IP: net.ParseIP("192.0.2.254")}
	valid := matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote, sender)
	valid.bytes = append(valid.bytes, valid.bytes...)
	valid.nbytes = 8 // Only the outer ICMP header arrived; the allocated tail contains a valid packet.
	if err := p.processPacket(&valid); err != nil {
		t.Fatal(err)
	}
	if event != nil {
		t.Fatalf("unreceived bytes beyond nbytes were parsed: %+v", event)
	}

	valid = matrixErrorPacket(t, 4, ipv4.ICMPTypeDestinationUnreachable, quote, sender)
	if err := p.processPacket(&valid); err != nil {
		t.Fatal(err)
	}
	if event == nil {
		t.Fatal("valid packet did not produce callback")
	}
	sender.IP[0] ^= 0xff
	valid.bytes[len(valid.bytes)-1] ^= 0xff
	if !event.Sender.IP.Equal(net.ParseIP("192.0.2.254")) || !event.Destination.IP.Equal(target.IP) {
		t.Fatalf("callback retained caller-owned packet/address storage: %+v", event)
	}
}

func TestScopedSenderAttributionRejectsAmbiguousScopesWithoutConsumingDedup(t *testing.T) {
	interfaceIndexes.Store("matrix-destination-zone", 77)
	interfaceIndexes.Store("77", 77)
	interfaceIndexes.Store("78", 78)
	t.Cleanup(func() {
		interfaceIndexes.Delete("matrix-destination-zone")
		interfaceIndexes.Delete("77")
		interfaceIndexes.Delete("78")
	})

	p, _, tracker := matrixPinger(6, 71, 72)
	target := &net.IPAddr{IP: net.ParseIP("fe80::1").To16(), Zone: "matrix-destination-zone"}
	p.SetIPAddr(target)
	for i := range p.sentRequests {
		p.sentRequests[i].destination = &net.IPAddr{IP: append(net.IP(nil), target.IP...), Zone: target.Zone}
	}
	var events []*ICMPError
	p.OnICMPError = func(event *ICMPError) { events = append(events, event) }
	send := func(seq int, sender *net.IPAddr) {
		t.Helper()
		quote := matrixQuote(6, target.IP, p.id, seq, matrixPayload(t, tracker))
		packet := matrixErrorPacket(t, 6, ipv6.ICMPTypeDestinationUnreachable, quote, sender)
		if err := p.processPacket(&packet); err != nil {
			t.Fatal(err)
		}
	}

	// Neither an unscoped sender nor the same address on another interface can
	// be attributed to the scoped target or consume its callback ledger.
	send(71, &net.IPAddr{IP: net.ParseIP("fe80::1").To16()})
	send(71, &net.IPAddr{IP: net.ParseIP("fe80::1").To16(), Zone: "78"})
	send(71, &net.IPAddr{IP: net.ParseIP("fe80::2").To16(), Zone: "78"})
	if len(events) != 0 {
		t.Fatalf("ambiguous or wrong-scope link-local sender was attributed: count=%d first sender=%s zone=%q", len(events), events[0].Sender.IP, events[0].Sender.Zone)
	}

	// A zone name and its numeric interface index identify the same target.
	send(71, &net.IPAddr{IP: net.ParseIP("fe80::1").To16(), Zone: "77"})
	if len(events) != 1 || !sameScopedIP(events[0].Sender, target) {
		t.Fatalf("equivalent target scope did not produce target callback: %+v", events)
	}

	// A numerically different link-local sender without a scope remains
	// intermediate evidence; it cannot be mistaken for the scoped target.
	send(71, &net.IPAddr{IP: net.ParseIP("fe80::2").To16()})
	if len(events) != 2 || !events[1].Sender.IP.Equal(net.ParseIP("fe80::2")) || sameScopedIP(events[1].Sender, target) {
		t.Fatalf("unscoped different link-local sender was not intermediate evidence: %+v", events)
	}

	// A global intermediate router is not scoped and remains attributable too.
	send(72, &net.IPAddr{IP: net.ParseIP("2001:db8::fe").To16()})
	if len(events) != 3 || events[2].Sender.IP.IsLinkLocalUnicast() || sameScopedIP(events[2].Sender, target) {
		t.Fatalf("global intermediate sender did not produce independent callback: %+v", events)
	}
}

func TestFailedSendDoesNotEnterBoundedSentLedger(t *testing.T) {
	p, _, _ := matrixPinger(4, 0)
	for i := 0; i < 3; i++ {
		p.sequence = i
		if err := p.sendICMP(testPacketConn{}); err != nil {
			t.Fatalf("successful fake write %d: %v", i, err)
		}
	}
	if len(p.sentRequests) != 2 || p.sentRequests[0].seq != 1 || p.sentRequests[1].seq != 2 {
		t.Fatalf("successful-send history = %+v, want last two requests", p.sentRequests)
	}
	p.sequence = 3
	err := p.sendICMP(testPacketConnBadWrite{})
	if err == nil || len(p.sentRequests) != 2 || p.sentRequests[0].seq != 1 || p.sentRequests[1].seq != 2 {
		t.Fatalf("failed write changed sent-request history: err=%v history=%+v", err, p.sentRequests)
	}
}

func TestSameScopedIPUsesInterfaceIdentityOnlyForScopedAddresses(t *testing.T) {
	interfaceIndexes.Store("matrix-zone-name", 77)
	interfaceIndexes.Store("77", 77)
	t.Cleanup(func() {
		interfaceIndexes.Delete("matrix-zone-name")
		interfaceIndexes.Delete("77")
	})
	cases := []struct {
		name string
		a, b *net.IPAddr
		want bool
	}{
		{"same global address ignores zone", &net.IPAddr{IP: net.ParseIP("2001:db8::1"), Zone: "a"}, &net.IPAddr{IP: net.ParseIP("2001:db8::1"), Zone: "b"}, true},
		{"same link-local zone", &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "77"}, &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "77"}, true},
		{"equivalent name and index", &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "matrix-zone-name"}, &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "77"}, true},
		{"different link-local zones", &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "77"}, &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "78"}, false},
		{"unscoped link-local not equal", &net.IPAddr{IP: net.ParseIP("fe80::1")}, &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "77"}, false},
		{"different addresses", &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "77"}, &net.IPAddr{IP: net.ParseIP("fe80::2"), Zone: "77"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameScopedIP(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameScopedIP(%+v,%+v)=%v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func FuzzQuotedEchoParsersNeverPanic(f *testing.F) {
	f.Add(byte(4), matrixIPv4Quote(net.ParseIP("192.0.2.2"), 123, 17, nil))
	f.Add(byte(6), matrixIPv6Quote(net.ParseIP("2001:db8::2"), 123, 17, nil, []byte{0, 60}))
	f.Add(byte(4), []byte{0x45})
	f.Add(byte(6), []byte{0x60, 0, 0, 0})
	f.Fuzz(func(t *testing.T, family byte, quote []byte) {
		if len(quote) > 65535 {
			return
		}
		_, _ = parseQuotedEcho(int(family), quote)
	})
}

func matrixIPv4Quote(destination net.IP, id, seq int, payload []byte) []byte {
	quote := make([]byte, 20+8+len(payload))
	quote[0] = 0x45
	binary.BigEndian.PutUint16(quote[2:4], uint16(len(quote)))
	quote[8], quote[9] = 64, 1
	copy(quote[12:16], net.ParseIP("192.0.2.1").To4())
	copy(quote[16:20], destination.To4())
	quote[20] = byte(ipv4.ICMPTypeEcho)
	binary.BigEndian.PutUint16(quote[24:26], uint16(id))
	binary.BigEndian.PutUint16(quote[26:28], uint16(seq))
	copy(quote[28:], payload)
	return quote
}

func matrixIPv6Quote(destination net.IP, id, seq int, payload []byte, extensions []byte) []byte {
	extensionBytes := 0
	for _, extension := range extensions {
		extensionBytes += matrixIPv6ExtensionLength(extension)
	}
	quote := make([]byte, 40+extensionBytes+8+len(payload))
	quote[0] = 0x60
	binary.BigEndian.PutUint16(quote[4:6], uint16(len(quote)-40))
	if len(extensions) == 0 {
		quote[6] = 58
	} else {
		quote[6] = extensions[0]
	}
	copy(quote[8:24], net.ParseIP("2001:db8::1").To16())
	copy(quote[24:40], destination.To16())
	offset := 40
	for i := range extensions {
		if i+1 == len(extensions) {
			quote[offset] = 58
		} else {
			quote[offset] = extensions[i+1]
		}
		if extensions[i] == 51 {
			quote[offset+1] = 2 // AH length: (2 + 2) * 4 = 16 bytes.
		} else {
			quote[offset+1] = 0
		}
		offset += matrixIPv6ExtensionLength(extensions[i])
	}
	quote[offset] = byte(ipv6.ICMPTypeEchoRequest)
	binary.BigEndian.PutUint16(quote[offset+4:offset+6], uint16(id))
	binary.BigEndian.PutUint16(quote[offset+6:offset+8], uint16(seq))
	copy(quote[offset+8:], payload)
	return quote
}

func matrixIPv6ExtensionLength(extension byte) int {
	if extension == 51 {
		return 16
	}
	return 8
}

func matrixIPv6AHQuote(destination net.IP, id, seq int, payload []byte, ahLength int) []byte {
	quote := make([]byte, 40+ahLength+8+len(payload))
	quote[0] = 0x60
	binary.BigEndian.PutUint16(quote[4:6], uint16(len(quote)-40))
	quote[6] = 51
	copy(quote[8:24], net.ParseIP("2001:db8::1").To16())
	copy(quote[24:40], destination.To16())
	quote[40] = 58
	quote[41] = byte(ahLength/4 - 2)
	if ahLength >= 12 {
		binary.BigEndian.PutUint32(quote[48:52], 1) // AH sequence number follows the SPI field.
	}
	offset := 40 + ahLength
	quote[offset] = byte(ipv6.ICMPTypeEchoRequest)
	binary.BigEndian.PutUint16(quote[offset+4:offset+6], uint16(id))
	binary.BigEndian.PutUint16(quote[offset+6:offset+8], uint16(seq))
	copy(quote[offset+8:], payload)
	return quote
}

func matrixPinger(family int, sequences ...int) (*Pinger, *net.IPAddr, uuid.UUID) {
	p := makeTestPinger()
	var destination *net.IPAddr
	if family == 4 {
		destination = &net.IPAddr{IP: net.ParseIP("192.0.2.2").To4()}
		p.ipv4 = true
	} else {
		destination = &net.IPAddr{IP: net.ParseIP("2001:db8::2").To16()}
		p.ipv4 = false
	}
	p.SetIPAddr(destination)
	tracker := p.getCurrentTrackerUUID()
	for _, seq := range sequences {
		p.sentRequests = append(p.sentRequests, sentRequest{
			destination: &net.IPAddr{IP: append(net.IP(nil), destination.IP...), Zone: destination.Zone},
			id:          p.id,
			seq:         seq,
			tracker:     tracker,
		})
	}
	return p, destination, tracker
}

func matrixPayload(t *testing.T, tracker uuid.UUID) []byte {
	t.Helper()
	encoded, err := tracker.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return append(timeToBytes(time.Now()), encoded...)
}

func matrixQuote(family int, target net.IP, id, seq int, payload []byte) []byte {
	if family == 4 {
		return matrixIPv4Quote(target, id, seq, payload)
	}
	return matrixIPv6Quote(target, id, seq, payload, nil)
}

func matrixOtherAddress(family int) *net.IPAddr {
	if family == 4 {
		return &net.IPAddr{IP: net.ParseIP("192.0.2.254").To4()}
	}
	return &net.IPAddr{IP: net.ParseIP("2001:db8::fe").To16()}
}

func matrixErrorPacket(t *testing.T, family int, typ icmp.Type, quote []byte, sender *net.IPAddr) packet {
	t.Helper()
	var body icmp.MessageBody
	switch typ {
	case ipv4.ICMPTypeDestinationUnreachable, ipv6.ICMPTypeDestinationUnreachable:
		body = &icmp.DstUnreach{Data: quote}
	case ipv4.ICMPTypeTimeExceeded, ipv6.ICMPTypeTimeExceeded:
		body = &icmp.TimeExceeded{Data: quote}
	case ipv4.ICMPTypeParameterProblem, ipv6.ICMPTypeParameterProblem:
		body = &icmp.ParamProb{Data: quote}
	case ipv6.ICMPTypePacketTooBig:
		body = &icmp.PacketTooBig{Data: quote}
	default:
		t.Fatalf("unsupported matrix outer type %v", typ)
	}
	message := &icmp.Message{Type: typ, Code: 0, Body: body}
	data, err := message.Marshal(nil)
	if err != nil {
		t.Fatalf("marshal ICMP error: %v", err)
	}
	result := makeTestPacket(data)
	result.addr = sender
	return result
}

func icmpTypeCaseName(family int, typ icmp.Type) string {
	return "v" + strconv.Itoa(family) + "_type" + strconv.Itoa(icmpTypeCode(typ))
}

func icmpTypeCode(typ icmp.Type) int {
	switch value := typ.(type) {
	case ipv4.ICMPType:
		return int(value)
	case ipv6.ICMPType:
		return int(value)
	default:
		return -1
	}
}
