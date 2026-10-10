package main

import (
	"os"
	"os/signal"
	"sync"
)

type commandSignals struct {
	control *commandControl
	signals chan os.Signal
	done    chan struct{}
	joined  chan struct{}
	close   sync.Once
}

func installCommandSignals(control *commandControl) *commandSignals {
	watcher := &commandSignals{
		control: control,
		signals: make(chan os.Signal, 4),
		done:    make(chan struct{}),
		joined:  make(chan struct{}),
	}
	if !signal.Ignored(os.Interrupt) {
		signal.Notify(watcher.signals, os.Interrupt)
	}
	go watcher.run()
	return watcher
}

func (w *commandSignals) EnterRunning() {
	if w == nil {
		return
	}
	w.control.EnterRunning()
	registerRunSignals(w.signals)
}

func (w *commandSignals) Close() {
	if w == nil {
		return
	}
	w.close.Do(func() {
		w.control.Finish()
		signal.Stop(w.signals)
		close(w.done)
	})
	<-w.joined
}

func (w *commandSignals) run() {
	defer close(w.joined)
	for {
		select {
		case <-w.done:
			return
		case sig := <-w.signals:
			if sig == os.Interrupt {
				w.control.Interrupt()
				continue
			}
			if reason, ok := runStopReason(sig); ok {
				w.control.RequestStop(reason)
			}
		}
	}
}
