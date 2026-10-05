// Command nightshift hands ONE ready design spec to a Claude Code agent and
// drives it through a Ralph loop inside a Docker Sandboxes (sbx) container.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// version is set via -ldflags "-X main.version=..."; see the Makefile.
var version = "dev"

const usage = `nightshift — run design specs through a sandboxed Claude Code loop

usage:
  nightshift init                       create the nightshift home (~/.nightshift or $NIGHTSHIFT_HOME)
  nightshift new <slug> [--repo PATH] [--verify CMD]
                                        start a draft spec from the template
  nightshift ready <slug>               move a draft spec to specs/ready/
  nightshift done <slug>                move a ready spec to specs/done/
  nightshift run <spec> [flags]         run one ready spec (nightshift run -h for flags)
  nightshift run --next [flags]         run the oldest eligible ready spec
  nightshift status [<slug>]            list runs, or show one run in detail
  nightshift version                    print the version
`

var errNotImplemented = errors.New("not implemented")

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch runs the top-level command named by args[0] and returns the
// process exit code. Every subcommand other than help/version is a stub
// until its own task wires it up.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintf(stdout, "nightshift %s\n", version)
		return 0
	case "init", "new", "ready", "done", "run", "status":
		fmt.Fprintf(stderr, "nightshift: %v\n", errNotImplemented)
		return 1
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}
