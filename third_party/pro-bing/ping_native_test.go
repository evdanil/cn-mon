//go:build native

package probing

import (
	"runtime"
	"testing"
	"time"
)

// These upstream cases require a working raw ICMP socket and therefore run
// only in the explicitly requested native suite.
func TestSetInterfaceNameNative(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = time.Second
	pinger.InterfaceName = "lo"
	err := pinger.Run()
	if runtime.GOOS == "linux" {
		AssertNoError(t, err)
	} else {
		if err == nil {
			t.Fatal("expected interface selection to be unsupported on this platform")
		}
	}
	pinger.InterfaceName = "L()0pB@cK"
	err = pinger.Run()
	if runtime.GOOS == "linux" {
		AssertError(t, err, "device not found")
	} else if err == nil {
		t.Fatal("expected an invalid interface to fail")
	}
}

func TestSetSourceNative(t *testing.T) {
	pinger := New("127.0.0.1")
	pinger.SetPrivileged(true)
	pinger.Count = 1
	pinger.Timeout = time.Second
	pinger.Source = "127.0.0.1"
	AssertNoError(t, pinger.Run())
	pinger.Source = "invalid-ip"
	AssertError(t, pinger.Run(), "invalid source address: invalid-ip")
}
