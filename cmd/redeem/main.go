package main

import (
	"fmt"
	"io"
	"os"

	"github.com/jmo/terminal-redeemer/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}

	switch args[0] {
	case "-h", "--help", "help":
		printHelp(stdout)
		return 0
	case "doctor":
		fmt.Fprintf(stdout, "stateDir=%s\n", config.DefaultStateDir())
		fmt.Fprintln(stdout, "status=ok")
		return 0
	case "capture", "restore", "history", "prune", "bottle":
		fmt.Fprintf(stderr, "subcommand '%s' scaffolded but not implemented yet\n", args[0])
		return 2
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printHelp(stderr)
		return 2
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "redeem - terminal session history and restore")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  redeem [command]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Scaffolded commands:")
	fmt.Fprintln(w, "  capture   Capture window/session state")
	fmt.Fprintln(w, "  restore   Restore from history")
	fmt.Fprintln(w, "  history   Inspect timeline")
	fmt.Fprintln(w, "  prune     Prune old events/snapshots")
	fmt.Fprintln(w, "  bottle    Bottle workflows (V2)")
	fmt.Fprintln(w, "  doctor    Basic environment checks")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -h, --help  Show help")
}
