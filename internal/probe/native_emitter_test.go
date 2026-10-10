package probe

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/icmp"
)

func TestNativeEmitterBuildsMinimumQuotes(t *testing.T) {
	tests := []struct {
		name        string
		destination netip.Addr
	}{
		{name: "ipv4", destination: netip.MustParseAddr("127.0.0.1")},
		{name: "ipv6", destination: netip.MustParseAddr("::1")},
	}
	request := nativeRequest{ID: 0x1234, Seq: 0x5678}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quote, err := quotedEchoRequest(test.destination, request)
			if err != nil {
				t.Fatal(err)
			}
			if test.destination.Is4() {
				if len(quote) != 28 || quote[0]>>4 != 4 || quote[9] != 1 {
					t.Fatalf("invalid minimum IPv4 echo quote: %x", quote)
				}
				if internetChecksum(quote[:20]) != 0 {
					t.Fatalf("invalid IPv4 header checksum: %x", quote)
				}
				if binary.BigEndian.Uint16(quote[2:4]) != 52 || quote[20] != 8 || binary.BigEndian.Uint16(quote[24:26]) != uint16(request.ID) || binary.BigEndian.Uint16(quote[26:28]) != uint16(request.Seq) {
					t.Fatalf("IPv4 quote does not retain request identity: %x", quote)
				}
			} else {
				if len(quote) != 48 || quote[0]>>4 != 6 || quote[6] != 58 {
					t.Fatalf("invalid minimum IPv6 echo quote: %x", quote)
				}
				if binary.BigEndian.Uint16(quote[4:6]) != 32 || quote[40] != 128 || binary.BigEndian.Uint16(quote[44:46]) != uint16(request.ID) || binary.BigEndian.Uint16(quote[46:48]) != uint16(request.Seq) {
					t.Fatalf("IPv6 quote does not retain request identity: %x", quote)
				}
			}

			message, err := nativeICMPErrorMessage(test.destination, test.destination, request)
			if err != nil {
				t.Fatal(err)
			}
			if test.destination.Is4() {
				if internetChecksum(message) != 0 {
					t.Fatalf("invalid outer ICMPv4 checksum: %x", message)
				}
			} else if icmpv6Checksum(test.destination, test.destination, message) != 0 {
				t.Fatalf("invalid outer ICMPv6 checksum: %x", message)
			}
		})
	}
}

// emitNativeICMPError injects a minimum-quotation wire error through an actual
// raw socket. The production Pinger owns the receiving socket and decoder.
func emitNativeICMPError(destination, source netip.Addr, request nativeRequest) error {
	if destination.Is4() != source.Is4() {
		return fmt.Errorf("source and destination families differ")
	}
	message, err := nativeICMPErrorMessage(source, destination, request)
	if err != nil {
		return err
	}
	var network string
	if destination.Is4() {
		network = "ip4:icmp"
	} else {
		network = "ip6:ipv6-icmp"
	}
	conn, err := icmp.ListenPacket(network, source.String())
	if err != nil {
		return fmt.Errorf("listen raw sender %s: %w", source, err)
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return fmt.Errorf("set raw emitter write deadline: %w", err)
	}
	n, err := conn.WriteTo(message, &net.IPAddr{IP: net.IP(destination.AsSlice()), Zone: destination.Zone()})
	if err != nil {
		return fmt.Errorf("write raw error to %s: %w", destination, err)
	}
	if n != len(message) {
		return fmt.Errorf("short raw error write: wrote %d of %d bytes", n, len(message))
	}
	return nil
}

func nativeICMPErrorMessage(source, destination netip.Addr, request nativeRequest) ([]byte, error) {
	if destination.Is4() != source.Is4() {
		return nil, fmt.Errorf("source and destination families differ")
	}
	quote, err := quotedEchoRequest(destination, request)
	if err != nil {
		return nil, err
	}
	message := make([]byte, 8+len(quote))
	if destination.Is4() {
		message[0], message[1] = 3, 13 // Destination Unreachable, administratively prohibited.
		copy(message[8:], quote)
		binary.BigEndian.PutUint16(message[2:4], internetChecksum(message))
	} else {
		message[0], message[1] = 1, 1 // ICMPv6 Destination Unreachable, administratively prohibited.
		copy(message[8:], quote)
		binary.BigEndian.PutUint16(message[2:4], icmpv6Checksum(source, destination, message))
	}
	return message, nil
}

func quotedEchoRequest(destination netip.Addr, request nativeRequest) ([]byte, error) {
	if destination.Is4() {
		packet := make([]byte, 20+8)
		packet[0] = 0x45
		binary.BigEndian.PutUint16(packet[2:4], 52) // Original request includes timestamp and UUID payload; quote is minimum-length.
		packet[8], packet[9] = 64, 1
		address := destination.As4()
		copy(packet[12:16], address[:])
		copy(packet[16:20], address[:])
		binary.BigEndian.PutUint16(packet[10:12], internetChecksum(packet[:20]))
		packet[20], packet[21] = 8, 0
		binary.BigEndian.PutUint16(packet[24:26], uint16(request.ID))
		binary.BigEndian.PutUint16(packet[26:28], uint16(request.Seq))
		binary.BigEndian.PutUint16(packet[22:24], internetChecksum(packet[20:]))
		return packet, nil
	}
	if destination.Is6() {
		packet := make([]byte, 40+8)
		packet[0] = 0x60
		binary.BigEndian.PutUint16(packet[4:6], 32) // Original request includes timestamp and UUID payload; quote is minimum-length.
		packet[6], packet[7] = 58, 64
		address := destination.As16()
		copy(packet[8:24], address[:])
		copy(packet[24:40], address[:])
		packet[40], packet[41] = 128, 0
		binary.BigEndian.PutUint16(packet[44:46], uint16(request.ID))
		binary.BigEndian.PutUint16(packet[46:48], uint16(request.Seq))
		return packet, nil
	}
	return nil, fmt.Errorf("invalid quoted destination %s", destination)
}

func internetChecksum(packet []byte) uint16 {
	var sum uint32
	for len(packet) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[:2]))
		packet = packet[2:]
	}
	if len(packet) == 1 {
		sum += uint32(packet[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func icmpv6Checksum(source, destination netip.Addr, message []byte) uint16 {
	src, dst := source.As16(), destination.As16()
	pseudo := make([]byte, 40+len(message))
	copy(pseudo[:16], src[:])
	copy(pseudo[16:32], dst[:])
	binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(message)))
	pseudo[39] = 58
	copy(pseudo[40:], message)
	return internetChecksum(pseudo)
}
