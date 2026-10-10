//go:build linux

package probing

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestICMPIDFiltersAdmitCorrelatedErrorTypesAndOnlyMatchingEcho(t *testing.T) {
	const id = 0x1234
	v4Errors := []byte{
		byte(ipv4.ICMPTypeDestinationUnreachable),
		byte(ipv4.ICMPTypeTimeExceeded),
		byte(ipv4.ICMPTypeParameterProblem),
	}
	v4VM, err := bpf.NewVM(icmpIDFilterIPv4(id))
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range v4Errors {
		packet := make([]byte, 28)
		packet[0] = 0x45
		packet[20] = typ
		if got, err := v4VM.Run(packet); err != nil || got == 0 {
			t.Errorf("IPv4 ICMP error type %d filtered: len=%d err=%v", typ, got, err)
		}
	}
	v4Echo := make([]byte, 28)
	v4Echo[0], v4Echo[20] = 0x45, byte(ipv4.ICMPTypeEchoReply)
	binary.BigEndian.PutUint16(v4Echo[24:26], id)
	if got, err := v4VM.Run(v4Echo); err != nil || got == 0 {
		t.Errorf("matching IPv4 echo filtered: len=%d err=%v", got, err)
	}
	binary.BigEndian.PutUint16(v4Echo[24:26], id+1)
	if got, err := v4VM.Run(v4Echo); err != nil || got != 0 {
		t.Errorf("nonmatching IPv4 echo admitted: len=%d err=%v", got, err)
	}
	if got, err := v4VM.Run([]byte{0x45, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0, 0}); err != nil || got != 0 {
		t.Errorf("unrelated IPv4 ICMP admitted: len=%d err=%v", got, err)
	}

	v6VM, err := bpf.NewVM(icmpIDFilterIPv6(id))
	if err != nil {
		t.Fatal(err)
	}
	v6Errors := []byte{
		byte(ipv6.ICMPTypeDestinationUnreachable),
		byte(ipv6.ICMPTypePacketTooBig),
		byte(ipv6.ICMPTypeTimeExceeded),
		byte(ipv6.ICMPTypeParameterProblem),
	}
	for _, typ := range v6Errors {
		if got, err := v6VM.Run([]byte{typ, 0, 0, 0, 0, 0, 0, 0}); err != nil || got == 0 {
			t.Errorf("IPv6 ICMP error type %d filtered: len=%d err=%v", typ, got, err)
		}
	}
	v6Echo := []byte{byte(ipv6.ICMPTypeEchoReply), 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(v6Echo[4:6], id)
	if got, err := v6VM.Run(v6Echo); err != nil || got == 0 {
		t.Errorf("matching IPv6 echo filtered: len=%d err=%v", got, err)
	}
	binary.BigEndian.PutUint16(v6Echo[4:6], id+1)
	if got, err := v6VM.Run(v6Echo); err != nil || got != 0 {
		t.Errorf("nonmatching IPv6 echo admitted: len=%d err=%v", got, err)
	}
	if got, err := v6VM.Run([]byte{99, 0, 0, 0, 0, 0, 0, 0}); err != nil || got != 0 {
		t.Errorf("unrelated IPv6 ICMP admitted: len=%d err=%v", got, err)
	}
}
