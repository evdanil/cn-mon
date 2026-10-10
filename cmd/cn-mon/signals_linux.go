//go:build linux

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func registerRunSignals(ch chan<- os.Signal) {
	var signals []os.Signal
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(sig) {
			signals = append(signals, sig)
		}
	}
	if len(signals) != 0 {
		signal.Notify(ch, signals...)
	}
}

func runStopReason(sig os.Signal) (string, bool) {
	switch sig {
	case syscall.SIGTERM:
		return "term", true
	case syscall.SIGHUP:
		return "hangup", true
	default:
		return "", false
	}
}

func ignoreStdoutSIGPIPE() { signal.Ignore(syscall.SIGPIPE) }
