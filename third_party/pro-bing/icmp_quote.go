// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package probing

import (
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const maxQuotedIPv6Extensions = 16

type quotedEcho struct {
	destination net.IP
	id          int
	seq         int
	payload     []byte
}

func (p *Pinger) processICMPError(message *icmp.Message, source net.Addr, receivedAt time.Time) {
	if p.OnICMPError == nil || p.ipaddr == nil {
		return
	}
	family := 6
	if p.ipv4 {
		family = 4
	}
	quote, ok := icmpErrorQuote(family, message)
	if !ok {
		return
	}
	echo, ok := parseQuotedEcho(family, quote)
	if !ok || !p.matchID(echo.id) || !echo.destination.Equal(p.ipaddr.IP) {
		return
	}
	if len(echo.payload) >= timeSliceLength+trackerLength {
		packetUUID, err := p.getPacketUUID(echo.payload)
		if err != nil || packetUUID == nil {
			return
		}
		matchTracker := false
		for i := range p.sentRequests {
			if p.sentRequests[i].id == echo.id && p.sentRequests[i].seq == echo.seq && p.sentRequests[i].tracker == *packetUUID {
				matchTracker = true
				break
			}
		}
		if !matchTracker {
			return
		}
	}
	entry := -1
	for i := range p.sentRequests {
		request := &p.sentRequests[i]
		if request.id == echo.id && request.seq == echo.seq && request.destination.IP.Equal(echo.destination) {
			entry = i
			break
		}
	}
	if entry < 0 {
		return
	}
	sender, ok := ipAddrFromNetAddr(source)
	if !ok {
		return
	}
	if !senderScopeIsUsable(sender, p.sentRequests[entry].destination) {
		return
	}
	destination := &net.IPAddr{IP: append(net.IP(nil), echo.destination...), Zone: p.sentRequests[entry].destination.Zone}
	isTarget := sameScopedIP(sender, p.sentRequests[entry].destination)
	if isTarget {
		if p.sentRequests[entry].targetError {
			return
		}
		p.sentRequests[entry].targetError = true
	} else {
		if p.sentRequests[entry].otherError {
			return
		}
		p.sentRequests[entry].otherError = true
	}
	event := &ICMPError{
		Family:      family,
		Type:        icmpTypeNumber(message.Type),
		Code:        message.Code,
		Sender:      sender,
		Destination: destination,
		ID:          echo.id,
		Seq:         echo.seq,
		ReceivedAt:  receivedAt,
	}
	p.OnICMPError(event)
}

func sameScopedIP(a, b *net.IPAddr) bool {
	if a == nil || b == nil || !a.IP.Equal(b.IP) {
		return false
	}
	if !a.IP.IsLinkLocalUnicast() && !a.IP.IsLinkLocalMulticast() {
		return true
	}
	return sameInterfaceZone(a.Zone, b.Zone)
}

func senderScopeIsUsable(sender, destination *net.IPAddr) bool {
	if sender == nil || destination == nil {
		return false
	}
	if !sender.IP.IsLinkLocalUnicast() && !sender.IP.IsLinkLocalMulticast() {
		return true
	}
	sameAddress := sender.IP.Equal(destination.IP)
	if sameAddress && (sender.Zone == "" || destination.Zone == "") {
		return false
	}
	if sender.Zone != "" && destination.Zone != "" && !sameInterfaceZone(sender.Zone, destination.Zone) {
		return false
	}
	return true
}

var interfaceIndexes sync.Map

func sameInterfaceZone(a, b string) bool {
	if a == b {
		return a != ""
	}
	if a == "" || b == "" {
		return false
	}
	return interfaceIndex(a) > 0 && interfaceIndex(a) == interfaceIndex(b)
}

func interfaceIndex(zone string) int {
	if index, err := strconv.Atoi(zone); err == nil && index > 0 {
		return index
	}
	if value, ok := interfaceIndexes.Load(zone); ok {
		return value.(int)
	}
	iface, err := net.InterfaceByName(zone)
	index := 0
	if err == nil {
		index = iface.Index
	}
	interfaceIndexes.Store(zone, index)
	return index
}

func icmpTypeNumber(value icmp.Type) int {
	switch value := value.(type) {
	case ipv4.ICMPType:
		return int(value)
	case ipv6.ICMPType:
		return int(value)
	default:
		return -1
	}
}

func icmpErrorQuote(family int, message *icmp.Message) ([]byte, bool) {
	if message == nil {
		return nil, false
	}
	if family == 4 {
		switch message.Type {
		case ipv4.ICMPTypeDestinationUnreachable, ipv4.ICMPTypeTimeExceeded, ipv4.ICMPTypeParameterProblem:
		default:
			return nil, false
		}
	} else {
		switch message.Type {
		case ipv6.ICMPTypeDestinationUnreachable, ipv6.ICMPTypePacketTooBig, ipv6.ICMPTypeTimeExceeded, ipv6.ICMPTypeParameterProblem:
		default:
			return nil, false
		}
	}
	switch body := message.Body.(type) {
	case *icmp.DstUnreach:
		return body.Data, true
	case *icmp.TimeExceeded:
		return body.Data, true
	case *icmp.ParamProb:
		return body.Data, true
	case *icmp.PacketTooBig:
		return body.Data, true
	default:
		return nil, false
	}
}

func parseQuotedEcho(family int, quote []byte) (quotedEcho, bool) {
	if family == 4 {
		return parseQuotedIPv4Echo(quote)
	}
	if family == 6 {
		return parseQuotedIPv6Echo(quote)
	}
	return quotedEcho{}, false
}

func parseQuotedIPv4Echo(quote []byte) (quotedEcho, bool) {
	if len(quote) < 20 || quote[0]>>4 != 4 {
		return quotedEcho{}, false
	}
	headerLen := int(quote[0]&0x0f) * 4
	totalLen := int(binary.BigEndian.Uint16(quote[2:4]))
	if headerLen < 20 || totalLen < headerLen+8 || quote[9] != 1 {
		return quotedEcho{}, false
	}
	packetEnd := min(len(quote), totalLen)
	if headerLen > packetEnd-8 {
		return quotedEcho{}, false
	}
	if binary.BigEndian.Uint16(quote[6:8])&0x1fff != 0 {
		return quotedEcho{}, false
	}
	echo := quote[headerLen:packetEnd]
	if echo[0] != byte(ipv4.ICMPTypeEcho) || echo[1] != 0 {
		return quotedEcho{}, false
	}
	return quotedEcho{
		destination: append(net.IP(nil), quote[16:20]...),
		id:          int(binary.BigEndian.Uint16(echo[4:6])),
		seq:         int(binary.BigEndian.Uint16(echo[6:8])),
		payload:     echo[8:],
	}, true
}

func parseQuotedIPv6Echo(quote []byte) (quotedEcho, bool) {
	if len(quote) < 40 || quote[0]>>4 != 6 {
		return quotedEcho{}, false
	}
	declaredPayloadLen := int(binary.BigEndian.Uint16(quote[4:6]))
	packetEnd := min(len(quote), 40+declaredPayloadLen)
	nextHeader := quote[6]
	offset := 40
	for traversed := 0; traversed < maxQuotedIPv6Extensions; traversed++ {
		if nextHeader == 58 {
			if offset > packetEnd-8 || declaredPayloadLen < offset-40+8 {
				return quotedEcho{}, false
			}
			echo := quote[offset:packetEnd]
			if echo[0] != byte(ipv6.ICMPTypeEchoRequest) || echo[1] != 0 {
				return quotedEcho{}, false
			}
			return quotedEcho{
				destination: append(net.IP(nil), quote[24:40]...),
				id:          int(binary.BigEndian.Uint16(echo[4:6])),
				seq:         int(binary.BigEndian.Uint16(echo[6:8])),
				payload:     echo[8:],
			}, true
		}
		var headerLen int
		switch nextHeader {
		case 0, 43, 60: // Hop-by-hop, routing, destination options.
			if offset > packetEnd-2 {
				return quotedEcho{}, false
			}
			headerLen = (int(quote[offset+1]) + 1) * 8
		case 44: // Fragment.
			if offset > packetEnd-8 || binary.BigEndian.Uint16(quote[offset+2:offset+4])&0xfff8 != 0 {
				return quotedEcho{}, false
			}
			headerLen = 8
		case 51: // Authentication Header.
			if offset > packetEnd-2 {
				return quotedEcho{}, false
			}
			headerLen = (int(quote[offset+1]) + 2) * 4
			// AH contains fixed fields through the sequence number (12 bytes),
			// and its total length is padded to an 8-byte boundary in IPv6.
			if headerLen < 12 || headerLen%8 != 0 {
				return quotedEcho{}, false
			}
		default:
			return quotedEcho{}, false
		}
		if headerLen <= 0 || offset > packetEnd-headerLen || declaredPayloadLen < offset-40+headerLen {
			return quotedEcho{}, false
		}
		nextHeader = quote[offset]
		offset += headerLen
	}
	return quotedEcho{}, false
}

func ipAddrFromNetAddr(address net.Addr) (*net.IPAddr, bool) {
	switch value := address.(type) {
	case *net.IPAddr:
		if value == nil || value.IP == nil {
			return nil, false
		}
		return &net.IPAddr{IP: append(net.IP(nil), value.IP...), Zone: value.Zone}, true
	case *net.UDPAddr:
		if value == nil || value.IP == nil {
			return nil, false
		}
		return &net.IPAddr{IP: append(net.IP(nil), value.IP...), Zone: value.Zone}, true
	default:
		return nil, false
	}
}
