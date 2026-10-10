// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package probing

import (
	"encoding/binary"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestProcessPacketEmitsQuotedICMPError(t *testing.T) {
	pinger := makeTestPinger()
	pinger.SetIPAddr(&net.IPAddr{IP: net.ParseIP("192.0.2.2")})
	const seq = 17
	pinger.sentRequests = []sentRequest{{
		destination: &net.IPAddr{IP: net.ParseIP("192.0.2.2")},
		id:          pinger.id, seq: seq, tracker: pinger.getCurrentTrackerUUID(),
	}}
	quoted := make([]byte, 20+8+timeSliceLength+trackerLength)
	quoted[0] = 0x45
	binary.BigEndian.PutUint16(quoted[2:4], 20+8) // Padding after the declared packet must not become token data.
	quoted[8] = 64
	quoted[9] = 1
	copy(quoted[12:16], net.ParseIP("192.0.2.1").To4())
	copy(quoted[16:20], net.ParseIP("192.0.2.2").To4())
	quoted[20] = byte(ipv4.ICMPTypeEcho)
	binary.BigEndian.PutUint16(quoted[24:26], uint16(pinger.id))
	binary.BigEndian.PutUint16(quoted[26:28], seq)

	message := &icmp.Message{
		Type: ipv4.ICMPTypeDestinationUnreachable,
		Code: 3,
		Body: &icmp.DstUnreach{Data: quoted},
	}
	packetBytes, err := message.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	recv := makeTestPacket(packetBytes)
	recv.addr = &net.IPAddr{IP: net.ParseIP("192.0.2.254")}

	var got *ICMPError
	pinger.OnICMPError = func(event *ICMPError) { got = event }

	if err := pinger.processPacket(&recv); err != nil {
		t.Fatalf("processPacket: %v", err)
	}
	if got == nil {
		t.Fatal("valid quoted destination-unreachable packet did not reach OnICMPError")
	}
	if got.Family != 4 || got.Type != int(ipv4.ICMPTypeDestinationUnreachable) || got.Code != 3 || got.Sender == nil || !got.Sender.IP.Equal(net.ParseIP("192.0.2.254")) || got.Destination == nil || !got.Destination.IP.Equal(net.ParseIP("192.0.2.2")) || got.ID != pinger.id || got.Seq != seq || got.ReceivedAt.IsZero() {
		t.Fatalf("callback data = %+v", got)
	}
}

func TestQuotedIPv6TargetErrorSurvivesIntermediateFromAnotherZoneSpelling(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var iface *net.Interface
	for i := range interfaces {
		if interfaces[i].Index > 0 && interfaces[i].Name != "" {
			iface = &interfaces[i]
			break
		}
	}
	if iface == nil {
		t.Fatal("no interface available for scope fixture")
	}
	targetIP := net.ParseIP("fe80::1")
	pinger := makeTestPinger()
	pinger.ipv4 = false
	pinger.SetIPAddr(&net.IPAddr{IP: targetIP, Zone: iface.Name})
	const seq = 23
	pinger.sentRequests = []sentRequest{{
		destination: &net.IPAddr{IP: targetIP, Zone: iface.Name},
		id:          pinger.id, seq: seq, tracker: pinger.getCurrentTrackerUUID(),
	}}
	quoted := make([]byte, 48+timeSliceLength+trackerLength)
	quoted[0] = 0x60
	binary.BigEndian.PutUint16(quoted[4:6], 8)
	quoted[6] = 58
	copy(quoted[24:40], targetIP.To16())
	quoted[40] = byte(ipv6.ICMPTypeEchoRequest)
	binary.BigEndian.PutUint16(quoted[44:46], uint16(pinger.id))
	binary.BigEndian.PutUint16(quoted[46:48], seq)
	makeError := func(sender *net.IPAddr) packet {
		msg := &icmp.Message{Type: ipv6.ICMPTypeDestinationUnreachable, Code: 0, Body: &icmp.DstUnreach{Data: quoted}}
		bytes, err := msg.Marshal(nil)
		if err != nil {
			t.Fatal(err)
		}
		pkt := makeTestPacket(bytes)
		pkt.addr = sender
		return pkt
	}
	var senders []string
	pinger.OnICMPError = func(event *ICMPError) { senders = append(senders, event.Sender.Zone) }
	packetOne := makeError(&net.IPAddr{IP: net.ParseIP("2001:db8::2")})
	parsedOne, err := icmp.ParseMessage(protocolIPv6ICMP, packetOne.bytes[:packetOne.nbytes])
	if err != nil {
		t.Fatal(err)
	}
	quote, ok := icmpErrorQuote(6, parsedOne)
	if !ok {
		t.Fatalf("quote body unavailable: %#v", parsedOne.Body)
	}
	echo, ok := parseQuotedEcho(6, quote)
	if !ok || !pinger.matchID(echo.id) || !echo.destination.Equal(pinger.ipaddr.IP) {
		t.Fatalf("quote correlation basics failed: echo=%+v ok=%v id=%v dest=%v", echo, ok, pinger.matchID(echo.id), echo.destination.Equal(pinger.ipaddr.IP))
	}
	if len(pinger.sentRequests) != 1 || pinger.sentRequests[0].id != echo.id || pinger.sentRequests[0].seq != echo.seq || !pinger.sentRequests[0].destination.IP.Equal(echo.destination) {
		t.Fatalf("ledger correlation basics failed: echo=%+v ledger=%+v", echo, pinger.sentRequests)
	}
	if _, ok := ipAddrFromNetAddr(packetOne.addr); !ok {
		t.Fatalf("source address not supported: %#v", packetOne.addr)
	}
	pinger.processICMPError(parsedOne, packetOne.addr, time.Now())
	if len(senders) != 1 {
		t.Fatalf("first intermediate callback missing: ipv4=%v proto=%s addr=%+v entry=%+v", pinger.ipv4, pinger.protocol, pinger.ipaddr, pinger.sentRequests)
	}
	packetTwo := makeError(&net.IPAddr{IP: targetIP, Zone: strconv.Itoa(iface.Index)})
	parsedTwo, err := icmp.ParseMessage(protocolIPv6ICMP, packetTwo.bytes[:packetTwo.nbytes])
	if err != nil {
		t.Fatal(err)
	}
	pinger.processICMPError(parsedTwo, packetTwo.addr, time.Now())
	if len(senders) != 2 || senders[1] != strconv.Itoa(iface.Index) {
		t.Fatalf("correlated error callbacks = %v, want intermediate then target in equivalent scope", senders)
	}
}

func TestFinishClearsSuccessfulSendLedger(t *testing.T) {
	pinger := makeTestPinger()
	pinger.sentRequests = []sentRequest{{
		destination: &net.IPAddr{IP: net.ParseIP("192.0.2.2")},
		id:          pinger.id,
		seq:         1,
		tracker:     pinger.getCurrentTrackerUUID(),
	}}
	pinger.finish()
	if len(pinger.sentRequests) != 0 {
		t.Fatalf("finish retained stale sent requests: %+v", pinger.sentRequests)
	}
}
