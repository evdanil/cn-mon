// Copyright The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Modified for cn-mon; see ../../THIRD_PARTY_NOTICES.md for provenance and the adaptation summary.

package probing

import (
	"bytes"
	"context"
	"errors"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

var testAddr net.Addr = &net.IPAddr{
	IP: net.IPv4(127, 0, 0, 1),
}

func TestProcessPacket(t *testing.T) {
	pinger := makeTestPinger()
	shouldBe1 := 0
	// this function should be called
	pinger.OnRecv = func(_ *Packet) {
		shouldBe1++
	}

	currentUUID := pinger.getCurrentTrackerUUID()
	uuidEncoded, err := currentUUID.MarshalBinary()
	if err != nil {
		t.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), uuidEncoded...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   pinger.id,
		Seq:  pinger.sequence,
		Data: data,
	}
	pinger.awaitingSequences[currentUUID][pinger.sequence] = struct{}{}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)
	AssertTrue(t, shouldBe1 == 1)
}

func TestProcessPacket_IgnoreNonEchoReplies(t *testing.T) {
	pinger := makeTestPinger()
	shouldBe0 := 0
	// this function should not be called because the tracker is mismatched
	pinger.OnRecv = func(_ *Packet) {
		shouldBe0++
	}

	currentUUID, err := pinger.getCurrentTrackerUUID().MarshalBinary()
	if err != nil {
		t.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), currentUUID...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   pinger.id,
		Seq:  pinger.sequence,
		Data: data,
	}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeDestinationUnreachable,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)
	AssertTrue(t, shouldBe0 == 0)
}

func TestProcessPacket_IDMismatch(t *testing.T) {
	pinger := makeTestPinger()
	pinger.protocol = "icmp" // ID is only checked on "icmp" protocol
	shouldBe0 := 0
	// this function should not be called because the tracker is mismatched
	pinger.OnRecv = func(_ *Packet) {
		shouldBe0++
	}

	currentUUID, err := pinger.getCurrentTrackerUUID().MarshalBinary()
	if err != nil {
		t.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), currentUUID...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   999999,
		Seq:  pinger.sequence,
		Data: data,
	}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)
	AssertTrue(t, shouldBe0 == 0)
}

func TestProcessPacket_TrackerMismatch(t *testing.T) {
	pinger := makeTestPinger()
	shouldBe0 := 0
	// this function should not be called because the tracker is mismatched
	pinger.OnRecv = func(_ *Packet) {
		shouldBe0++
	}

	testUUID, err := uuid.New().MarshalBinary()
	if err != nil {
		t.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), testUUID...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   pinger.id,
		Seq:  pinger.sequence,
		Data: data,
	}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)
	AssertTrue(t, shouldBe0 == 0)
}

func TestProcessPacket_LargePacket(t *testing.T) {
	pinger := makeTestPinger()
	pinger.Size = 4096

	currentUUID, err := pinger.getCurrentTrackerUUID().MarshalBinary()
	if err != nil {
		t.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), currentUUID...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   pinger.id,
		Seq:  pinger.sequence,
		Data: data,
	}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)
}

func TestProcessPacket_PacketTooSmall(t *testing.T) {
	pinger := makeTestPinger()
	data := []byte("foo")

	body := &icmp.Echo{
		ID:   pinger.id,
		Seq:  pinger.sequence,
		Data: data,
	}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err := pinger.processPacket(&pkt)
	AssertError(t, err, "")
}

func TestNewPingerValid(t *testing.T) {
	p := New("192.0.2.1")
	err := p.Resolve()
	AssertNoError(t, err)
	AssertEqualStrings(t, "192.0.2.1", p.Addr())
	AssertTrue(t, isIPv4(p.IPAddr().IP))
	AssertFalse(t, p.Privileged())
	AssertEquals(t, 0, p.tclass)
	p.SetPrivileged(true)
	AssertTrue(t, p.Privileged())
	AssertNoError(t, p.SetAddr("192.0.2.2"))
	AssertTrue(t, isIPv4(p.IPAddr().IP))
	AssertNoError(t, p.SetAddr("2001:db8::1"))
	AssertFalse(t, isIPv4(p.IPAddr().IP))
	p.SetTrafficClass(192)
	AssertEquals(t, 192, p.tclass)

	p = New("2001:db8::2")
	AssertNoError(t, p.Resolve())
	AssertFalse(t, isIPv4(p.IPAddr().IP))
	AssertFalse(t, p.Privileged())
	p.SetPrivileged(true)
	AssertTrue(t, p.Privileged())
	AssertNoError(t, p.SetAddr("192.0.2.4"))
	AssertTrue(t, isIPv4(p.IPAddr().IP))
	AssertNoError(t, p.SetAddr("2001:db8::3"))
	AssertFalse(t, isIPv4(p.IPAddr().IP))
}

func TestNewPingerInvalid(t *testing.T) {
	_, err := NewPinger("127.0.0.0.1")
	AssertError(t, err, "127.0.0.0.1")

	_, err = NewPinger("127..0.0.1")
	AssertError(t, err, "127..0.0.1")

	_, err = NewPinger(":::1")
	AssertError(t, err, ":::1")
}

func TestSetIPAddr(t *testing.T) {
	p := New("192.0.2.1")
	addr := &net.IPAddr{IP: net.ParseIP("192.0.2.2")}
	p.SetIPAddr(addr)
	AssertEqualStrings(t, addr.String(), p.Addr())
}

func TestEmptyIPAddr(t *testing.T) {
	_, err := NewPinger("")
	AssertError(t, err, "empty pinger did not return an error")
}

func TestStatisticsSunny(t *testing.T) {
	// Create a localhost ipv4 pinger
	p := New("127.0.0.1")
	err := p.Resolve()
	AssertNoError(t, err)
	AssertEqualStrings(t, "127.0.0.1", p.Addr())

	p.PacketsSent = 10
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})

	stats := p.Statistics()
	if stats.PacketsRecv != 10 {
		t.Errorf("Expected %v, got %v", 10, stats.PacketsRecv)
	}
	if stats.PacketsSent != 10 {
		t.Errorf("Expected %v, got %v", 10, stats.PacketsSent)
	}
	if stats.PacketLoss != 0 {
		t.Errorf("Expected %v, got %v", 0, stats.PacketLoss)
	}
	if stats.MinRtt != time.Duration(1000) {
		t.Errorf("Expected %v, got %v", time.Duration(1000), stats.MinRtt)
	}
	if stats.MaxRtt != time.Duration(1000) {
		t.Errorf("Expected %v, got %v", time.Duration(1000), stats.MaxRtt)
	}
	if stats.AvgRtt != time.Duration(1000) {
		t.Errorf("Expected %v, got %v", time.Duration(1000), stats.AvgRtt)
	}
	if stats.StdDevRtt != time.Duration(0) {
		t.Errorf("Expected %v, got %v", time.Duration(0), stats.StdDevRtt)
	}
}

func TestStatisticsLossy(t *testing.T) {
	// Create a localhost ipv4 pinger
	p := New("127.0.0.1")
	err := p.Resolve()
	AssertNoError(t, err)
	AssertEqualStrings(t, "127.0.0.1", p.Addr())

	p.PacketsSent = 20
	p.updateStatistics(&Packet{Rtt: time.Duration(10)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(10000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(800)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(40)})
	p.updateStatistics(&Packet{Rtt: time.Duration(100000)})
	p.updateStatistics(&Packet{Rtt: time.Duration(1000)})

	stats := p.Statistics()
	if stats.PacketsRecv != 10 {
		t.Errorf("Expected %v, got %v", 10, stats.PacketsRecv)
	}
	if stats.PacketsSent != 20 {
		t.Errorf("Expected %v, got %v", 20, stats.PacketsSent)
	}
	if stats.PacketLoss != 50 {
		t.Errorf("Expected %v, got %v", 50, stats.PacketLoss)
	}
	if stats.MinRtt != time.Duration(10) {
		t.Errorf("Expected %v, got %v", time.Duration(10), stats.MinRtt)
	}
	if stats.MaxRtt != time.Duration(100000) {
		t.Errorf("Expected %v, got %v", time.Duration(100000), stats.MaxRtt)
	}
	if stats.AvgRtt != time.Duration(11585) {
		t.Errorf("Expected %v, got %v", time.Duration(11585), stats.AvgRtt)
	}
	if stats.StdDevRtt != time.Duration(29603) {
		t.Errorf("Expected %v, got %v", time.Duration(29603), stats.StdDevRtt)
	}
}

func TestStatisticsZeroDivision(t *testing.T) {
	p := New("127.0.0.1")
	err := p.Resolve()
	AssertNoError(t, err)
	AssertEqualStrings(t, "127.0.0.1", p.Addr())

	p.PacketsSent = 0
	stats := p.Statistics()

	// If packets were not sent (due to send errors), ensure that
	// PacketLoss is 0 instead of NaN due to zero division
	if stats.PacketLoss != 0 {
		t.Errorf("Expected %v, got %v", 0, stats.PacketLoss)
	}
}

// Test helpers
func makeTestPinger() *Pinger {
	pinger := New("127.0.0.1")

	pinger.ipv4 = true
	pinger.addr = "127.0.0.1"
	pinger.protocol = "icmp"
	pinger.id = 123
	pinger.Size = 0

	return pinger
}

// makeTestPacket emulates a packet with the message msg which come from testAddr
func makeTestPacket(msg []byte) packet {
	return packet{
		bytes:  msg,
		nbytes: len(msg),
		ttl:    24,
		addr:   testAddr,
	}
}

func AssertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("Expected No Error but got %s, Stack:\n%s",
			err, string(debug.Stack()))
	}
}

func AssertError(t *testing.T, err error, info string) {
	t.Helper()
	if err == nil {
		t.Errorf("Expected Error but got %s, %s, Stack:\n%s",
			err, info, string(debug.Stack()))
	}
}

func AssertEqualStrings(t *testing.T, expected, actual string) {
	t.Helper()
	if expected != actual {
		t.Errorf("Expected %s, got %s, Stack:\n%s",
			expected, actual, string(debug.Stack()))
	}
}

func AssertEquals[T comparable](t *testing.T, expected, actual T) {
	t.Helper()
	if expected != actual {
		t.Errorf("Expected %v, got %v, Stack:\n%s",
			expected, actual, string(debug.Stack()))
	}
}

func AssertNotEqualStrings(t *testing.T, expected, actual string) {
	t.Helper()
	if expected == actual {
		t.Errorf("Expected %s, got %s, Stack:\n%s",
			expected, actual, string(debug.Stack()))
	}
}

func AssertTrue(t *testing.T, b bool) {
	t.Helper()
	if !b {
		t.Errorf("Expected True, got False, Stack:\n%s", string(debug.Stack()))
	}
}

func AssertFalse(t *testing.T, b bool) {
	t.Helper()
	if b {
		t.Errorf("Expected False, got True, Stack:\n%s", string(debug.Stack()))
	}
}

func BenchmarkProcessPacket(b *testing.B) {
	pinger := New("127.0.0.1")

	pinger.ipv4 = true
	pinger.addr = "127.0.0.1"
	pinger.protocol = "ip4:icmp"
	pinger.id = 123

	currentUUID, err := pinger.getCurrentTrackerUUID().MarshalBinary()
	if err != nil {
		b.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), currentUUID...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   pinger.id,
		Seq:  pinger.sequence,
		Data: data,
	}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	for k := 0; k < b.N; k++ {
		pinger.processPacket(&pkt)
	}
}

func TestProcessPacket_IgnoresDuplicateSequence(t *testing.T) {
	pinger := makeTestPinger()
	// pinger.protocol = "icmp" // ID is only checked on "icmp" protocol
	shouldBe0 := 0
	dups := 0

	// this function should not be called because the tracker is mismatched
	pinger.OnRecv = func(_ *Packet) {
		shouldBe0++
	}

	pinger.OnDuplicateRecv = func(_ *Packet) {
		dups++
	}

	currentUUID := pinger.getCurrentTrackerUUID()
	uuidEncoded, err := currentUUID.MarshalBinary()
	if err != nil {
		t.Fatalf("unable to marshal UUID binary: %s", err)
	}
	data := append(timeToBytes(time.Now()), uuidEncoded...)
	if remainSize := pinger.Size - timeSliceLength - trackerLength; remainSize > 0 {
		data = append(data, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   123,
		Seq:  0,
		Data: data,
	}
	// register the sequence as sent
	pinger.awaitingSequences[currentUUID][0] = struct{}{}

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: body,
	}

	msgBytes, _ := msg.Marshal(nil)

	pkt := makeTestPacket(msgBytes)

	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)
	// receive a duplicate
	err = pinger.processPacket(&pkt)
	AssertNoError(t, err)

	AssertTrue(t, shouldBe0 == 1)
	AssertTrue(t, dups == 1)
	AssertTrue(t, pinger.PacketsRecvDuplicates == 1)
}

type testPacketConn struct{}

func (c testPacketConn) Close() error                      { return nil }
func (c testPacketConn) ICMPRequestType() icmp.Type        { return ipv4.ICMPTypeEcho }
func (c testPacketConn) SetFlagTTL() error                 { return nil }
func (c testPacketConn) SetReadDeadline(_ time.Time) error { return nil }
func (c testPacketConn) SetTTL(_ int)                      {}
func (c testPacketConn) SetMark(_ uint) error              { return nil }
func (c testPacketConn) SetDoNotFragment() error           { return nil }
func (c testPacketConn) SetBroadcastFlag() error           { return nil }
func (c testPacketConn) InstallICMPIDFilter(_ int) error   { return nil }
func (c testPacketConn) SetIfIndex(_ int)                  {}
func (c testPacketConn) SetSource(_ net.IP)                {}
func (c testPacketConn) SetTrafficClass(uint8) error       { return nil }

func (c testPacketConn) ReadFrom(_ []byte) (n int, ttl int, src net.Addr, err error) {
	return 0, 0, testAddr, nil
}

func (c testPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return len(b), nil
}

type testPacketConnBadWrite struct {
	testPacketConn
}

func (c testPacketConnBadWrite) WriteTo(_ []byte, _ net.Addr) (int, error) {
	return 0, errors.New("bad write")
}

func TestRunBadWrite(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.Count = 1

	err := pinger.Resolve()
	AssertNoError(t, err)

	var conn testPacketConnBadWrite

	err = pinger.run(context.Background(), conn)
	AssertTrue(t, err != nil)

	stats := pinger.Statistics()
	AssertTrue(t, stats != nil)
	if stats == nil {
		t.FailNow()
	}
	AssertTrue(t, stats.PacketsSent == 0)
	AssertTrue(t, stats.PacketsRecv == 0)
}

type testPacketConnBadRead struct {
	testPacketConn
}

func (c testPacketConnBadRead) ReadFrom(_ []byte) (n int, ttl int, src net.Addr, err error) {
	return 0, 0, testAddr, errors.New("bad read")
}

func TestRunBadRead(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.Count = 1

	err := pinger.Resolve()
	AssertNoError(t, err)

	var conn testPacketConnBadRead

	err = pinger.run(context.Background(), conn)
	AssertTrue(t, err != nil)

	stats := pinger.Statistics()
	AssertTrue(t, stats != nil)
	if stats == nil {
		t.FailNow()
	}
	AssertTrue(t, stats.PacketsSent == 1)
	AssertTrue(t, stats.PacketsRecv == 0)
}

type testPacketConnOK struct {
	testPacketConn
	m         sync.Mutex
	writeDone int32
	buf       []byte
	dst       net.Addr
}

func (c *testPacketConnOK) WriteTo(b []byte, dst net.Addr) (int, error) {
	c.m.Lock()
	defer c.m.Unlock()
	c.buf = make([]byte, len(b))
	c.dst = dst
	n := copy(c.buf, b)
	atomic.StoreInt32(&c.writeDone, 1)
	return n, nil
}

func (c *testPacketConnOK) ReadFrom(b []byte) (n int, ttl int, src net.Addr, err error) {
	c.m.Lock()
	defer c.m.Unlock()
	if atomic.LoadInt32(&c.writeDone) == 0 {
		return 0, 0, testAddr, nil
	}
	msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), c.buf)
	if err != nil {
		return 0, 0, nil, err
	}
	msg.Type = ipv4.ICMPTypeEchoReply
	buf, err := msg.Marshal(nil)
	if err != nil {
		return 0, 0, nil, err
	}
	time.Sleep(10 * time.Millisecond)
	return copy(b, buf), 64, testAddr, nil
}

func TestRunOK(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.Count = 1

	err := pinger.Resolve()
	AssertNoError(t, err)

	conn := new(testPacketConnOK)

	err = pinger.run(context.Background(), conn)
	AssertTrue(t, err == nil)

	stats := pinger.Statistics()
	AssertTrue(t, stats != nil)
	if stats == nil {
		t.FailNow()
	}
	AssertTrue(t, stats.PacketsSent == 1)
	AssertTrue(t, stats.PacketsRecv == 1)
	AssertTrue(t, stats.MinRtt >= 10*time.Millisecond)
	AssertTrue(t, stats.MinRtt <= 12*time.Millisecond)
}

func TestRunWithTimeoutContext(t *testing.T) {
	pinger := New("127.0.0.1")

	err := pinger.Resolve()
	AssertNoError(t, err)

	conn := new(testPacketConnOK)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = pinger.run(ctx, conn)
	AssertTrue(t, errors.Is(err, context.DeadlineExceeded))
	elapsedTime := time.Since(start)
	AssertTrue(t, elapsedTime < 10*time.Second)

	stats := pinger.Statistics()
	AssertTrue(t, stats != nil)
	if stats == nil {
		t.FailNow()
	}
	AssertTrue(t, stats.PacketsSent > 0)
	AssertTrue(t, stats.PacketsRecv > 0)
}

type blockedWritePacketConn struct {
	testPacketConn
	closed   chan struct{}
	started  chan struct{}
	once     sync.Once
	closeErr error
}

func (c *blockedWritePacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *blockedWritePacketConn) WriteTo([]byte, net.Addr) (int, error) {
	close(c.started)
	<-c.closed
	if c.closeErr != nil {
		return 0, c.closeErr
	}
	return 0, net.ErrClosed
}
func (c *blockedWritePacketConn) ReadFrom([]byte) (int, int, net.Addr, error) {
	<-c.closed
	return 0, 0, nil, net.ErrClosed
}

func TestRunCancellationClosesSocketBeforeJoiningBlockedWrite(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.SetIPAddr(&net.IPAddr{IP: net.ParseIP("127.0.0.1")})
	pinger.Count = 2
	conn := &blockedWritePacketConn{closed: make(chan struct{}), started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pinger.run(ctx, conn) }()
	select {
	case <-conn.started:
	case <-time.After(100 * time.Millisecond):
		cancel()
		t.Fatal("run did not reach the blocked write")
	}
	cancel()
	select {
	case <-conn.closed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("parent cancellation did not close a blocked ICMP socket")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v, want context.Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("run did not join workers after closing the socket")
	}
	cancel()
}

func TestRunHostTimeoutClosesSocketBeforeJoiningBlockedWrite(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.SetIPAddr(&net.IPAddr{IP: net.ParseIP("127.0.0.1")})
	pinger.Count = 2
	pinger.Timeout = 50 * time.Millisecond
	conn := &blockedWritePacketConn{closed: make(chan struct{}), started: make(chan struct{})}
	sendErrors := 0
	pinger.OnSendError = func(*Packet, error) { sendErrors++ }
	done := make(chan error, 1)
	go func() { done <- pinger.run(context.Background(), conn) }()
	select {
	case <-conn.started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("run did not reach the blocked write")
	}
	select {
	case <-conn.closed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("host timeout did not close a blocked ICMP socket")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host timeout returned execution error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("run did not join workers after host timeout")
	}
	if sendErrors != 0 {
		t.Fatalf("intentional timeout close emitted %d send errors", sendErrors)
	}
}

func TestRunHostTimeoutPreservesIndependentWriteFailure(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.SetIPAddr(&net.IPAddr{IP: net.ParseIP("127.0.0.1")})
	pinger.Count = 2
	pinger.Timeout = 50 * time.Millisecond
	wantErr := errors.New("resource exhaustion")
	conn := &orderedTimeoutPacketConn{
		blockedWritePacketConn: &blockedWritePacketConn{closed: make(chan struct{}), started: make(chan struct{})},
		readReturned:           make(chan struct{}),
		writeErr:               wantErr,
	}
	done := make(chan error, 1)
	go func() { done <- pinger.run(context.Background(), conn) }()
	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("run error = %v, want independent write failure", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("run did not return after independent write failure")
	}
}

type orderedTimeoutPacketConn struct {
	*blockedWritePacketConn
	readReturned chan struct{}
	writeErr     error
}

func (c *orderedTimeoutPacketConn) ReadFrom([]byte) (int, int, net.Addr, error) {
	<-c.closed
	close(c.readReturned)
	return 0, 0, nil, net.ErrClosed
}

func (c *orderedTimeoutPacketConn) WriteTo([]byte, net.Addr) (int, error) {
	close(c.started)
	<-c.closed
	<-c.readReturned
	time.Sleep(20 * time.Millisecond) // Let recvICMP publish its stop-induced error first.
	return 0, c.writeErr
}

func TestRunWithBackgroundContext(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.Count = 10
	pinger.Interval = 100 * time.Millisecond

	err := pinger.Resolve()
	AssertNoError(t, err)

	conn := new(testPacketConnOK)

	err = pinger.run(context.Background(), conn)
	AssertTrue(t, err == nil)

	stats := pinger.Statistics()
	AssertTrue(t, stats != nil)
	if stats == nil {
		t.FailNow()
	}
	AssertTrue(t, stats.PacketsRecv == 10)
}

func TestSetResolveTimeout(t *testing.T) {
	p := New("192.0.2.1")
	p.Count = 3
	p.Timeout = 5 * time.Second
	p.ResolveTimeout = 2 * time.Second
	err := p.Resolve()
	AssertNoError(t, err)

	err = p.SetAddr("192.0.2.1 ")
	AssertError(t, err, "")

	err = p.SetAddr("127.0.0.1 ")
	AssertError(t, err, "")

	err = p.SetAddr("127.0.0.1")
	AssertNoError(t, err)
}

func TestRunStatisticsConcurrent(t *testing.T) {
	p := New("127.0.0.1")
	p.Count = 1
	p.Interval = time.Millisecond
	p.Timeout = time.Second
	AssertNoError(t, p.Resolve())
	statsDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-statsDone:
				return
			default:
				p.Statistics()
			}
		}
	}()
	err := p.run(context.Background(), new(testPacketConnOK))
	close(statsDone)
	AssertNoError(t, err)
}
