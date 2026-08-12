package cli

import (
	"fmt"
	"io"
)

// usageText is the client's help. Every verb in the system offers --json;
// phase 0 adds exactly two verbs, spec validate and spec explain.
const usageText = `wt — worktree manager client

usage:
  wt spec validate [path]            parse and validate the spec; path may be
                                     a file or a directory (default: walk up
                                     from cwd to the worktree root, never above)
  wt spec explain --slot N --slug S  resolve the resource table for one slot
      [--base <name>=<port>]...      one base per port resource
      [--home <path>] [--worktree <path>] [--json]
  wt help                            this help

every verb offers --json: exactly one JSON object on stdout, nothing else.
results go to stdout, diagnostics to stderr.

exit codes: 0 success, 1 failure, 2 usage error, 3 refused by a safety
check, 4 required context unavailable, 5 coordinator unreachable.
`

// Run is the whole client, as a function of args and the two streams, so it
// is testable without exec'ing the binary. It returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return ExitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	case "spec":
		return runSpec(args[1:], stdout, stderr)
	default:
		WriteError(stderr, UsageError(
			"run 'wt help' for the verb list",
			"unknown verb %q", args[0]))
		return ExitUsage
	}
}

func runSpec(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		WriteError(stderr, UsageError(
			"run 'wt spec validate [path]' or 'wt spec explain --slot N --slug S'",
			"spec needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "validate":
		return runSpecValidate(args[1:], stdout, stderr)
	case "explain":
		return runSpecExplain(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt spec validate [path]' or 'wt spec explain --slot N --slug S'",
			"unknown spec verb %q", args[0]))
		return ExitUsage
	}
}
