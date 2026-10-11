// Copyright 2026 cn-mon authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/output"
	"golang.org/x/term"
)

type commandScreenState struct {
	screen *output.Screen
}

func openCommandScreen(state *commandScreenState, control *commandControl, theme string, stdin io.Reader, stderr io.Writer) *output.Screen {
	if state == nil || state.screen != nil {
		return nil
	}
	inputFile, inputIsFile := stdin.(*os.File)
	outputFile, outputIsFile := stderr.(*os.File)
	inputTTY := inputIsFile && term.IsTerminal(int(inputFile.Fd()))
	outputTTY := outputIsFile && term.IsTerminal(int(outputFile.Fd()))
	if !inputTTY && !outputTTY {
		return nil
	}
	if !inputIsFile {
		inputFile = nil
	}
	if !outputIsFile {
		outputFile = nil
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	screen := output.OpenScreen(inputFile, outputFile, theme, noColor, output.KeyHandlers{
		Quit:      func() { control.RequestStop("quit") },
		Interrupt: func() { control.Interrupt() },
	})
	state.screen = screen
	control.SetEmergencyRestore(screen.EmergencyRestore)
	return screen
}

func closeCommandScreen(state *commandScreenState) {
	if state == nil || state.screen == nil {
		return
	}
	screen := state.screen
	state.screen = nil
	screen.Close()
}

func commandNote(state *commandScreenState, stderr io.Writer, message string) {
	if !output.DiagnosticWriteAllowed(stderr) {
		return
	}
	if state != nil && state.screen != nil && state.screen.CanWriteNote() {
		state.screen.Note("cn-mon: " + message)
		return
	}
	writeCommandDiagnostic(stderr, "cn-mon: %s\n", message)
}

func readerModeName(mode commandMode) string {
	switch mode {
	case modeList:
		return "list"
	case modeShow:
		return "show"
	case modeFollow:
		return "follow"
	case modeListRuns:
		return "list-runs"
	case modeDeleteRun:
		return "delete-run"
	case modeDeleteRequest:
		return "delete-request"
	case modeClearHistory:
		return "clear-history"
	default:
		return "run"
	}
}

func runReaderCLI(
	opts cliOptions,
	stdout, stderr io.Writer,
	cfg settings,
	logger *fileLogger,
	logFailure func(error),
	hardCtx context.Context,
	control *commandControl,
	signals *commandSignals,
	runtime commandRuntime,
) int {
	closedLogger := false
	finish := func(code int) int {
		if !closedLogger {
			closedLogger = true
			logFailure(logger.Close())
		}
		return code
	}
	formatOptions := commandReportOptionsFor(runtime, stdout, opts.Format, cfg.Theme)
	write := func(ordinary int, err error) int {
		if err == nil || isClosedOutputPipe(err, stdout) {
			return ordinary
		}
		logFailure(logger.Log("ERROR", "report write failed"))
		commandNote(runtime.screenState, stderr, "write report: "+err.Error())
		return 3
	}

	if opts.Mode == modeFollow && !commandIsTerminal(runtime, stderr) {
		writeCommandDiagnostic(stderr, "cn-mon: --follow requires stderr to be a terminal\n")
		var empty monitor.ReadOutcome
		empty.Run = monitor.RunOutcome{Seen: []monitor.HostSummary{}, Latest: []monitor.LatestRow{}, Unresolved: []string{}}
		code := write(2, writeCommandReport(control, stdout, formatOptions, func() error {
			return output.WriteHistory(stdout, empty, formatOptions)
		}))
		logFailure(logger.Log("INFO", "follow requires stderr terminal"))
		return finish(code)
	}
	if opts.Mode == modeFollow {
		if screen := openCommandScreen(runtime.screenState, control, cfg.Theme, runtime.stdin, stderr); screen != nil {
			defer screen.Close()
		}
	}

	deps := runtime.readDeps
	if deps.Clock == nil {
		deps.Clock = monitor.NewClock()
	}
	if deps.OpenHistory == nil {
		deps.OpenHistory = openReadOnlyHistory
	}
	if deps.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			commandNote(runtime.screenState, stderr, "cannot resolve host name: "+err.Error())
			logFailure(logger.Log("ERROR", "cannot resolve host name"))
			return finish(3)
		}
		deps.Hostname = hostname
	}
	if deps.PIDState == nil {
		deps.PIDState = history.LocalPIDState
	}
	deps.Stop = control.Stop()
	deps.EnterFollowing = func() {
		if signals != nil {
			signals.EnterRunning()
		} else {
			control.EnterRunning()
		}
	}
	if deps.Note == nil {
		deps.Note = func(message string) {
			commandNote(runtime.screenState, stderr, message)
			logFailure(logger.Log("INFO", message))
		}
	}
	if screen := screenFromState(runtime.screenState); screen != nil {
		deps.Live = withScreenLiveHooks(deps.Live, screen)
	}

	switch opts.Mode {
	case modeList:
		rows, err := monitor.List(hardCtx, cfg.HistoryFile, deps)
		if err != nil {
			closeCommandScreen(runtime.screenState)
			commandNote(runtime.screenState, stderr, "history operation failed: "+err.Error())
			logFailure(logger.Log("ERROR", "history operation failed"))
			code := write(3, writeCommandReport(control, stdout, formatOptions, func() error {
				return output.WriteRequests(stdout, []monitor.RequestRow{}, formatOptions)
			}))
			return finish(code)
		}
		ordinary := 1
		if len(rows) != 0 {
			ordinary = 0
		}
		if err := writeCommandReport(control, stdout, formatOptions, func() error {
			return output.WriteRequests(stdout, rows, formatOptions)
		}); err != nil {
			ordinary = write(ordinary, err)
		}
		logFailure(logger.Log("INFO", "list operation finished"))
		return finish(ordinary)
	case modeShow, modeFollow:
		plan := monitor.ReadPlan{HistoryFile: cfg.HistoryFile, Prefix: opts.ID, Since: opts.Since, Follow: opts.Mode == modeFollow}
		out, err := monitor.Read(hardCtx, plan, deps)
		if err != nil {
			closeCommandScreen(runtime.screenState)
			commandNote(runtime.screenState, stderr, "history operation failed: "+err.Error())
			logFailure(logger.Log("ERROR", "history operation failed"))
			var empty monitor.ReadOutcome
			empty.Run = monitor.RunOutcome{Seen: []monitor.HostSummary{}, Latest: []monitor.LatestRow{}, Unresolved: []string{}}
			code := write(3, writeCommandReport(control, stdout, formatOptions, func() error {
				return output.WriteHistory(stdout, empty, formatOptions)
			}))
			return finish(code)
		}
		closeCommandScreen(runtime.screenState)
		code := monitor.ReadExitCode(out)
		if out.Problem != "" {
			deps.Note(out.Problem)
		}
		if err := writeCommandReport(control, stdout, formatOptions, func() error {
			return output.WriteHistory(stdout, out, formatOptions)
		}); err != nil {
			code = write(code, err)
		}
		logFailure(logger.Log("INFO", readerModeName(opts.Mode)+" operation finished"))
		return finish(code)
	default:
		writeCommandDiagnostic(stderr, "cn-mon: invalid reader mode %d\n", opts.Mode)
		return finish(3)
	}
}

func screenFromState(state *commandScreenState) *output.Screen {
	if state == nil {
		return nil
	}
	return state.screen
}

func withScreenLiveHooks(existing *monitor.LiveHooks, screen *output.Screen) *monitor.LiveHooks {
	return &monitor.LiveHooks{
		Enabled: func() bool {
			if !screen.Enabled() {
				return false
			}
			return existing == nil || existing.Enabled == nil || existing.Enabled()
		},
		Draw: func(update monitor.LiveUpdate) {
			screen.Draw(update)
			if existing != nil && existing.Draw != nil {
				existing.Draw(update)
			}
		},
		Change: func(change monitor.Change) {
			screen.Change(change)
			if existing != nil && existing.Change != nil {
				existing.Change(change)
			}
		},
	}
}

func openReadOnlyHistory(ctx context.Context, path string) (monitor.Reader, error) {
	store, err := history.OpenReader(ctx, path)
	if err != nil || store == nil {
		return nil, err
	}
	return store, nil
}

func commandIsTerminal(runtime commandRuntime, writer io.Writer) bool {
	if runtime.isTerminal != nil {
		return runtime.isTerminal(writer)
	}
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func commandReportOptionsFor(runtime commandRuntime, writer io.Writer, format, theme string) output.ReportOptions {
	if runtime.reportOptions != nil {
		return runtime.reportOptions(writer, format, theme)
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	options := output.ReportOptions{Format: format, Theme: theme, Width: 80, NoColor: noColor}
	if file, ok := writer.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		options.Terminal = true
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			options.Width = width
		}
	}
	return options
}

func writeCommandReport(control *commandControl, writer io.Writer, options output.ReportOptions, write func() error) error {
	needsVT := options.Terminal && (options.Format == "" || options.Format == "table")
	var addEmergencyRestore func(func())
	if control != nil {
		addEmergencyRestore = control.AddEmergencyRestore
	}
	return withReportConsoleVT(writer, needsVT, addEmergencyRestore, write)
}
