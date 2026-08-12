package cli

import (
	"fmt"
	"io"
)

// usageText is the client's help. Every verb in the system offers --json;
// phase 3 adds the two bands verbs alongside phase 0's spec verbs, phase
// 1's guard and show, and phase 2's daemon verbs.
const usageText = `wt — worktree manager client

usage:
  wt spec validate [path]            parse and validate the spec; path may be
                                     a file or a directory (default: walk up
                                     from cwd to the worktree root, never above)
  wt spec explain --slot N --slug S  resolve the resource table for one slot
      [--base <name>=<port>]...      one base per port resource
      [--home <path>] [--worktree <path>] [--json]
  wt show [--json] [--cwd <dir>]     read the descriptor back from the
                                     working tree
  wt guard [--json] [--cwd <dir>]    the enforcement hook: deny a tool call
      [--tool <name>] [--input <json>]  whose file_path escapes the worktree.
      [--path <file>]                accepts a PreToolUse payload on stdin
  wt daemon status [--prefix <dir>]  the coordinator's state: not registered,
      [--json]                       registered but stopped, running but
                                     unreachable, or running — each broken
                                     state names a different fix
  wt daemon install [--prefix <dir>] register wtd with the platform's
      [--wtd <path>] [--json]        supervisor (launchd on macOS) and
                                     start it; --prefix writes the
                                     registration into a temp directory
                                     instead and loads nothing
  wt bands list [--json]             the port band ledger: the bases each
                                     app holds, and the host-global
                                     reservations no app may allocate from
  wt bands reserve --base <n>=<p>... register this repo's port band from its
      [--json]                       committed spec (the walk-up wt.yaml);
                                     one base per port resource
  wt init --description <text>      attach to the current worktree:
      [--slug <s>] [--param <n>=<v>] allocate, materialise, emit the
      [--json] [--dry-run] [--cwd]   descriptor and .env, run the hooks,
                                     activate; idempotent, and the repair
                                     path for a worktree that drifted
  wt start [--param <n>=<v>]        run the hooks that bring the stack up:
      [--json] [--dry-run] [--cwd]   start, seed, health — no allocation,
                                     no emission, no coordinator
  wt rm --slug <s> [--json]         safety checks (dirty tree, unpushed
      [--dry-run] [--keep-processes] commits, open PR), then reap, tear
      [--purge <flag>]... [--cwd]    down and deallocate in the
                                     coordinator, then git worktree
                                     remove; works with the directory
                                     already gone
  wt bands reserve --host            reserve host-global ports no app may
      --port <p>... --note <text>    allocate from; the note names what
      [--json]                       holds the range
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
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "start":
		return runStart(args[1:], stdout, stderr)
	case "rm":
		return runRm(args[1:], stdout, stderr)
	case "guard":
		return runGuard(args[1:], stdout, stderr)
	case "show":
		return runShow(args[1:], stdout, stderr)
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	case "bands":
		return runBands(args[1:], stdout, stderr)
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
