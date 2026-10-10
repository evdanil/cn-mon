//go:build windows

package main

import "os"

func registerRunSignals(chan<- os.Signal) {}

func runStopReason(os.Signal) (string, bool) { return "", false }

func ignoreStdoutSIGPIPE() {}
