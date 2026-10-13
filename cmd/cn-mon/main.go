package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/evdanil/cn-mon/internal/history"
	"github.com/evdanil/cn-mon/internal/monitor"
	"github.com/evdanil/cn-mon/internal/output"
	"github.com/evdanil/cn-mon/internal/probe"
	"github.com/evdanil/cn-mon/internal/target"
	"github.com/google/uuid"
)

var version = "dev"
var revision = ""

func main() {
	ignoreStdoutSIGPIPE()
	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runCLIWithRuntime(args, stdin, stdout, stderr, commandRuntime{
		installSignals: true,
		exit:           os.Exit,
	})
}

type commandRuntime struct {
	stdin          io.Reader
	environment    func() (configEnvironment, error)
	runDeps        monitor.RunDeps
	readDeps       monitor.ReadDeps
	isTerminal     func(io.Writer) bool
	reportOptions  func(io.Writer, string, string) output.ReportOptions
	screenState    *commandScreenState
	installSignals bool
	exit           func(int)
}

func runCLIWithRuntime(args []string, stdin io.Reader, stdout, stderr io.Writer, runtime commandRuntime) int {
	startUTC := time.Now().UTC()
	opts, err := parseCLI(args, startUTC)
	if err != nil {
		writeCommandDiagnostic(stderr, "cn-mon: %v\n", err)
		return 2
	}
	switch opts.Mode {
	case modeHelp:
		fmt.Fprint(stdout, usageText)
		return 0
	case modeVersion:
		if revision == "" {
			fmt.Fprintf(stdout, "cn-mon %s\n", version)
		} else {
			fmt.Fprintf(stdout, "cn-mon %s (%s)\n", version, revision)
		}
		return 0
	}
	if stdin == nil {
		stdin = runtime.stdin
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	runtime.stdin = stdin
	if runtime.exit == nil {
		runtime.exit = os.Exit
	}
	if runtime.screenState == nil {
		runtime.screenState = &commandScreenState{}
	}
	hardCtx, hardCancel := context.WithCancel(context.Background())
	defer hardCancel()
	control := newCommandControl(hardCancel, runtime.exit)
	var signals *commandSignals
	if runtime.installSignals {
		signals = installCommandSignals(control)
		defer signals.Close()
	}

	var objects []string
	if opts.Mode == modeRun {
		objects, err = target.ReadObjects(opts.Objects, opts.File, stdin)
		if err != nil {
			writeCommandDiagnostic(stderr, "cn-mon: %v\n", err)
			return 2
		}
		if len(objects) == 0 {
			writeCommandDiagnostic(stderr, "cn-mon: no target objects were provided\n")
			return 2
		}
	}

	env := runtime.environment
	if env == nil {
		env = currentCommandEnvironment
	}
	configEnv, err := env()
	if err != nil {
		writeCommandDiagnostic(stderr, "cn-mon: cannot resolve configuration paths: %v\n", err)
		return 2
	}
	cfg, warnings, err := loadSettings(configEnv, opts.Overrides)
	if err != nil {
		writeCommandDiagnostic(stderr, "cn-mon: cannot resolve configuration: %v\n", err)
		return 2
	}
	var rerun *rerunSelection
	if opts.Mode == modeRerun {
		selected, err := selectRerun(hardCtx, cfg.HistoryFile, opts.ID, opts.Every, opts.For, runtime.readDeps)
		if err != nil {
			var selectionFailure *rerunSelectionFailure
			if errors.As(err, &selectionFailure) {
				writeCommandDiagnostic(stderr, "cn-mon: %s\n", selectionFailure.message)
				return 2
			}
			writeCommandDiagnostic(stderr, "cn-mon: cannot restore saved request: %v\n", err)
			return 3
		}
		rerun = &selected
		opts.Mode = modeRun
	}
	logger, err := openLogger(cfg, configEnv.HomeDir)
	if err != nil {
		writeCommandDiagnostic(stderr, "cn-mon: cannot initialize logging: %v\n", err)
		return 3
	}
	logFailure := func(err error) {
		if err != nil {
			commandNote(runtime.screenState, stderr, "logging write failed: "+err.Error())
		}
	}
	if opts.Mode == modeRun {
		if screen := openCommandScreen(runtime.screenState, control, cfg.Theme, stdin, stderr); screen != nil {
			defer screen.Close()
		}
	}
	for _, warning := range warnings {
		commandNote(runtime.screenState, stderr, "warning: "+warning)
		logFailure(logger.Log("WARNING", warning))
	}
	if opts.Mode == modeRun {
		logFailure(logger.Log("INFO", "starting target run"))
	} else {
		logFailure(logger.Log("INFO", "starting "+readerModeName(opts.Mode)+" operation"))
		if isHistoryManagementMode(opts.Mode) {
			return runHistoryManagementCLI(opts, stdout, stderr, cfg, logger, logFailure, hardCtx, control, runtime)
		}
		return runReaderCLI(opts, stdout, stderr, cfg, logger, logFailure, hardCtx, control, signals, runtime)
	}

	var request target.Request
	var rejected []target.Rejection
	if rerun != nil {
		request = rerun.request
	} else {
		request, rejected, err = target.Build(objects, opts.Ports)
		if err != nil {
			writeCommandDiagnostic(stderr, "cn-mon: invalid target request: %v\n", err)
			logFailure(logger.Close())
			return 2
		}
	}
	invalid := len(rejected) != 0
	if len(request.Hosts) == 0 {
		outcome := monitor.RunOutcome{
			Seen:       []monitor.HostSummary{},
			Latest:     []monitor.LatestRow{},
			Unresolved: []string{},
		}
		closeCommandScreen(runtime.screenState)
		reportOptions := commandReportOptionsFor(runtime, stdout, opts.Format, cfg.Theme)
		err = writeCommandReport(control, stdout, reportOptions, func() error {
			return output.WriteRun(stdout, request, outcome, rejected, reportOptions)
		})
		code := reportExitCode(2, err, stdout, stderr)
		logFailure(logger.Close())
		return code
	}

	interval := effectiveInterval(opts)
	duration := opts.For
	var source *string
	if opts.File != nil && *opts.File != "-" {
		base := filepath.Base(*opts.File)
		source = &base
	}
	if rerun != nil {
		interval = rerun.interval
		duration = rerun.duration
		source = cloneString(rerun.source)
	}
	requestID, err := uuid.NewRandom()
	if err != nil {
		writeCommandDiagnostic(stderr, "cn-mon: cannot generate run ID: %v\n", err)
		logFailure(logger.Close())
		return 3
	}
	hostname, err := os.Hostname()
	if err != nil {
		writeCommandDiagnostic(stderr, "cn-mon: cannot resolve host name: %v\n", err)
		logFailure(logger.Close())
		return 3
	}
	plan := monitor.RunPlan{
		Request:     request,
		Interval:    interval,
		Duration:    duration,
		Since:       opts.Since,
		OwnRunOnly:  opts.OwnRunOnly,
		Source:      source,
		HistoryFile: cfg.HistoryFile,
		HistoryDays: cfg.HistoryDays,
		RunID:       requestID.String(),
		Hostname:    hostname,
		PID:         int64(os.Getpid()),
	}
	if runtime.installSignals {
		signals.EnterRunning()
	} else {
		control.EnterRunning()
	}
	deps := runtime.runDeps
	if deps.Clock == nil {
		deps.Clock = monitor.NewClock()
	}
	if deps.Prober == nil {
		deps.Prober = probe.New()
	}
	if deps.OpenHistory == nil {
		deps.OpenHistory = func(ctx context.Context, path string) (monitor.History, error) {
			return history.OpenWriter(ctx, path)
		}
	}
	deps.Stop = control.Stop()
	if deps.Note == nil {
		deps.Note = func(message string) {
			commandNote(runtime.screenState, stderr, message)
			logFailure(logger.Log("INFO", message))
		}
	}
	if screen := screenFromState(runtime.screenState); screen != nil {
		deps.Live = withScreenLiveHooks(deps.Live, screen)
	}
	outcome, runErr := monitor.Run(hardCtx, plan, deps)
	code := monitor.RunExitCode(outcome, invalid)
	if logErr := logger.Log("INFO", "target run finished"); logErr != nil {
		logFailure(logErr)
	}
	if runErr != nil {
		logFailure(logger.Log("ERROR", "target run failed"))
		commandNote(runtime.screenState, stderr, "run failed: "+runErr.Error())
		code = 3
	} else if outcome.HistoryError != nil {
		logFailure(logger.Log("ERROR", "history operation failed"))
	} else if outcome.ProbeFailed {
		logFailure(logger.Log("ERROR", "target probe failed"))
	}
	closeCommandScreen(runtime.screenState)
	reportOptions := commandReportOptionsFor(runtime, stdout, opts.Format, cfg.Theme)
	if err := writeCommandReport(control, stdout, reportOptions, func() error {
		return output.WriteRun(stdout, request, outcome, rejected, reportOptions)
	}); err != nil {
		if !isClosedOutputPipe(err, stdout) {
			logFailure(logger.Log("ERROR", "report write failed"))
			commandNote(runtime.screenState, stderr, "write report: "+err.Error())
			code = 3
		}
	}
	logFailure(logger.Close())
	return code
}

func currentCommandEnvironment() (configEnvironment, error) {
	executable, err := os.Executable()
	if err != nil {
		return configEnvironment{}, fmt.Errorf("resolve executable: %w", err)
	}
	home, err := currentHomeDir()
	if err != nil {
		return configEnvironment{}, err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return configEnvironment{}, fmt.Errorf("resolve working directory: %w", err)
	}
	return configEnvironment{Executable: executable, HomeDir: home, WorkingDir: workingDir}, nil
}

func effectiveInterval(opts cliOptions) *time.Duration {
	if opts.Every != nil {
		return opts.Every
	}
	if opts.For != nil {
		interval := time.Minute
		return &interval
	}
	return nil
}

func reportExitCode(ordinary int, err error, stdout, stderr io.Writer) int {
	if err == nil || isClosedOutputPipe(err, stdout) {
		return ordinary
	}
	writeCommandDiagnostic(stderr, "cn-mon: write report: %v\n", err)
	return 3
}

func writeCommandDiagnostic(stderr io.Writer, format string, args ...any) {
	if !output.DiagnosticWriteAllowed(stderr) {
		return
	}
	fmt.Fprintf(stderr, format, args...)
}

const usageText = `Usage:
  cn-mon [TARGET ...|-] [-f|--file PATH|-] [--tcp PORTS] [--every INTERVAL] [--for DURATION]
         [--since WHEN] [--history-file PATH] [--history-days DAYS] [--format table|json|md|csv] [COMMON]
  cn-mon --list [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --list-runs REQUEST_ID [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --show ID [--since WHEN] [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --follow ID [--since WHEN] [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --rerun REQUEST_ID [--every INTERVAL] [--for DURATION] [--since WHEN]
         [--history-file PATH] [--history-days DAYS] [--format FORMAT] [COMMON]
  cn-mon --delete-run RUN_ID [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --delete-request REQUEST_ID [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --clear-history [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon -h|--help
  cn-mon -v|--version

Common options:
  -c, --config PATH       Read an additional .cn configuration file
  -t, --theme THEME       default, monochrome, pastel, or dark
  -l, --log-file PATH     Append logs to PATH
      --log-level LEVEL   DEBUG, INFO, WARNING, ERROR, or CRITICAL
  -h, --help              Show this help
  -v, --version           Show the version

Run options:
  -f, --file PATH         Read target objects from a file; '-' reads stdin
      --tcp PORTS         Check 1–5 TCP ports, for example 22,443
      --every INTERVAL    Repeat at intervals of at least 10 seconds
      --for DURATION      Stop after a duration of at least 10 seconds
      --since WHEN        Include history since an age or ISO date/time
      --history-file PATH Override the history database path
      --history-days DAYS Override history retention for a target run
      --format FORMAT     table, json, csv, or md (default: table)
      --rerun ID          Repeat the newest saved run for a request ID or unique prefix

History options:
      --list              List stored requests
      --list-runs ID      List runs for a request ID or unique prefix
      --show ID           Show one request by ID or unique prefix
      --follow ID         Follow a running request in the terminal; q stops following
      --delete-run ID     Delete a run by its exact, case-sensitive run ID
      --delete-request ID Delete one request and all its runs by unique ID prefix
      --clear-history     Delete all stored requests and runs

TARGET is an IP address, CIDR, or ASCII DNS name. Options may appear before or after
targets; use -- to end option parsing. Without targets or an input file, use '-' for
stdin. No arguments prints a usage diagnostic and exits 2. Follow requires a terminal
on stderr. With terminal input, q stops a run or follow and v switches the run display
to table view; structured reports go to stdout and live display/status to stderr.

This build includes configuration and logging, target probing and history, list/show/
follow readers, saved-batch reruns, table/JSON/CSV/Markdown reports, and a best-effort live terminal screen.
Supported release targets: Linux and Windows x86-64.
`
