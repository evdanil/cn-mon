package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

var version = "dev"

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr)) }

func runCLI(args []string, stdout, stderr io.Writer) int {
	opts, err := parseCLI(args, time.Now().UTC())
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
	default:
		fmt.Fprintf(stderr, "cn-mon: this command mode is not implemented yet\n")
		return 3
	}
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
      --format FORMAT     table (default), json, md, or csv

History options:
      --list              List stored requests
      --show ID            Show one request by a 4–64 digit hexadecimal ID or prefix
      --follow ID          Follow a running request

TARGET is an IP address, CIDR, or ASCII DNS name. Options may appear before or after
targets; use -- to end option parsing. Without targets or an input file, use '-' for
stdin. No arguments prints a usage diagnostic and exits 2.

This build implements CLI parsing, configuration, target input, and the pure monitor
model. Target probing, history commands, live display, and report rendering are pending.
Valid operational commands currently exit 3 with a not-implemented diagnostic.
Supported release targets: Linux x86-64 and Windows x86-64.
`
