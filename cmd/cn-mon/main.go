package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/evdanil/gotest/internal/history"
	"github.com/evdanil/gotest/internal/monitor"
	"github.com/evdanil/gotest/internal/output"
	"github.com/evdanil/gotest/internal/probe"
	"github.com/evdanil/gotest/internal/target"
	"github.com/google/uuid"
)

var version = "dev"

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
	installSignals bool
	exit           func(int)
}

func runCLIWithRuntime(args []string, stdin io.Reader, stdout, stderr io.Writer, runtime commandRuntime) int {
	startUTC := time.Now().UTC()
	opts, err := parseCLI(args, startUTC)
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: %v\n", err)
		return 2
	}
	switch opts.Mode {
	case modeHelp:
		fmt.Fprint(stdout, usageText)
		return 0
	case modeVersion:
		fmt.Fprintf(stdout, "cn-mon %s\n", version)
		return 0
	}
	if opts.Mode != modeRun || opts.Format != "json" {
		fmt.Fprintln(stderr, "cn-mon: this command mode is not implemented yet")
		return 3
	}

	if stdin == nil {
		stdin = runtime.stdin
	}
	if stdin == nil {
		stdin = os.Stdin
	}
	if runtime.exit == nil {
		runtime.exit = os.Exit
	}
	hardCtx, hardCancel := context.WithCancel(context.Background())
	defer hardCancel()
	control := newCommandControl(hardCancel, runtime.exit)
	var signals *commandSignals
	if runtime.installSignals {
		signals = installCommandSignals(control)
		defer signals.Close()
	}

	objects, err := target.ReadObjects(opts.Objects, opts.File, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: %v\n", err)
		return 2
	}
	if len(objects) == 0 {
		fmt.Fprintln(stderr, "cn-mon: no target objects were provided")
		return 2
	}

	env := runtime.environment
	if env == nil {
		env = currentCommandEnvironment
	}
	configEnv, err := env()
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: cannot resolve configuration paths: %v\n", err)
		return 2
	}
	cfg, warnings, err := loadSettings(configEnv, opts.Overrides)
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: cannot resolve configuration: %v\n", err)
		return 2
	}
	logger, err := openLogger(cfg, configEnv.HomeDir)
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: cannot initialize logging: %v\n", err)
		return 3
	}
	logFailure := func(err error) {
		if err != nil {
			fmt.Fprintf(stderr, "cn-mon: logging write failed: %v\n", err)
		}
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "cn-mon: warning: %s\n", warning)
		logFailure(logger.Log("WARNING", warning))
	}
	logFailure(logger.Log("INFO", "starting target run"))

	request, rejected, err := target.Build(objects, opts.Ports)
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: invalid target request: %v\n", err)
		logFailure(logger.Close())
		return 2
	}
	invalid := len(rejected) != 0
	if len(request.Hosts) == 0 {
		outcome := monitor.RunOutcome{
			Seen:       []monitor.HostSummary{},
			Latest:     []monitor.LatestRow{},
			Unresolved: []string{},
		}
		err = output.WriteRunJSON(stdout, request, outcome, rejected)
		code := reportExitCode(2, err, stdout, stderr)
		logFailure(logger.Close())
		return code
	}

	interval := effectiveInterval(opts)
	var source *string
	if opts.File != nil && *opts.File != "-" {
		base := filepath.Base(*opts.File)
		source = &base
	}
	requestID, err := uuid.NewRandom()
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: cannot generate run ID: %v\n", err)
		logFailure(logger.Close())
		return 3
	}
	hostname, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(stderr, "cn-mon: cannot resolve host name: %v\n", err)
		logFailure(logger.Close())
		return 3
	}
	plan := monitor.RunPlan{
		Request:     request,
		Interval:    interval,
		Duration:    opts.For,
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
			fmt.Fprintf(stderr, "cn-mon: %s\n", message)
			logFailure(logger.Log("INFO", message))
		}
	}
	outcome, runErr := monitor.Run(hardCtx, plan, deps)
	code := monitor.RunExitCode(outcome, invalid)
	if logErr := logger.Log("INFO", "target run finished"); logErr != nil {
		logFailure(logErr)
	}
	if runErr != nil {
		logFailure(logger.Log("ERROR", "target run failed"))
		fmt.Fprintf(stderr, "cn-mon: run failed: %v\n", runErr)
		code = 3
	} else if outcome.HistoryError != nil {
		logFailure(logger.Log("ERROR", "history operation failed"))
	} else if outcome.ProbeFailed {
		logFailure(logger.Log("ERROR", "target probe failed"))
	}
	if err := output.WriteRunJSON(stdout, request, outcome, rejected); err != nil {
		if !isClosedOutputPipe(err, stdout) {
			logFailure(logger.Log("ERROR", "report write failed"))
			fmt.Fprintf(stderr, "cn-mon: write report: %v\n", err)
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
	fmt.Fprintf(stderr, "cn-mon: write report: %v\n", err)
	return 3
}

const usageText = `Usage:
  cn-mon [TARGET ...|-] [-f|--file PATH|-] [--tcp PORTS] [--every INTERVAL] [--for DURATION]
         [--since WHEN] [--history-file PATH] [--history-days DAYS] [--format table|json|md|csv] [COMMON]
  cn-mon --list [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --show ID [--since WHEN] [--history-file PATH] [--format FORMAT] [COMMON]
  cn-mon --follow ID [--since WHEN] [--history-file PATH] [--format FORMAT] [COMMON]
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
      --format FORMAT     json enables target runs in this milestone; table is the final default

History options:
      --list              List stored requests (available in M5)
      --show ID            Show one request (available in M5)
      --follow ID          Follow a running request (available in M5)

TARGET is an IP address, CIDR, or ASCII DNS name. Options may appear before or after
targets; use -- to end option parsing. Without targets or an input file, use '-' for
stdin. No arguments prints a usage diagnostic and exits 2. During M4, specify
--format json for target runs; human reports and history reader commands remain
unavailable until M5.

This build includes configuration/logging, target input, native probing, history
storage, and JSON target-run foundations. Supported release targets: Linux and Windows
x86-64.
`
