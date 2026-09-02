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
  wt spec path --slug S | --name N   where a worktree of this repository goes
      [--home <path>] [--root <dir>]    and what it branches from. --slug
      [--json]                       takes a slug and refuses anything else;
                                     --name takes a caller-supplied name and
                                     normalises it, for the callers handed a
                                     name rather than asked for one. --json
                                     adds the slug and the base revision
  wt show [--json | --brief]         read the descriptor back from the
      [--cwd <dir>]                  working tree. --brief is the arrival
                                     form: identity, the isolated values,
                                     and the shared block — what this
                                     worktree has no copy of its own of
  wt guard [--json] [--cwd <dir>]    the enforcement hook: deny a tool call
      [--tool <name>] [--input <json>]  whose file_path escapes the worktree.
      [--path <file>]                accepts a PreToolUse payload on stdin
  wt daemon status [--prefix <dir>]  the coordinator's state: not registered,
      [--json]                       registered but stopped, running but
                                     unreachable, or running — each broken
                                     state names a different fix
  wt daemon install [--prefix <dir>] register wtd with the platform's
      [--wtd <path>] [--json]        supervisor (launchd on macOS) and
      [--tcp <addr>]                 start it; --prefix writes the
      [--tcp-token <token>]          registration into a temp directory
                                     instead and loads nothing; --tcp and
                                     --tcp-token (both together, loopback
                                     address, 16+ characters) also start
                                     the opt-in loopback TCP listener for
                                     hosts where a socket cannot be shared
                                     into a container — every TCP
                                     connection must present the token
  wt daemon uninstall [--prefix <dir>]  the reverse: stop wtd, deregister
      [--force] [--json]             it from the platform's supervisor and
                                     remove the registration. The store is
                                     never removed; uninstall refuses while
                                     registry entries are still allocated,
                                     naming 'wt list' and 'wt rm' — --force
                                     is the only way past the refusal
  wt bands list [--json]             the port band ledger: the bases each
                                     app holds, and the host-global
                                     reservations no app may allocate from
  wt bands suggest [--spec <file>]   propose where the spec's port bases
      [--json]                       could sit — the lowest base per port
                                     resource whose required range fits
                                     against the ledger (colliding with no
                                     band and no host reservation); the
                                     skill chooses only where bases go,
                                     never how large they are
  wt bands reserve --base <n>=<p>... register this repo's port band from its
      [--json]                       committed spec (the walk-up wt.yaml);
                                     one base per port resource
  wt ports scan [--json]             what is listening on this machine now:
                                     every LISTEN socket, sorted by port,
                                     with the owning pid and command; it
                                     reports facts and never reserves
                                     anything — the skill's phase-1 audit
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
      [--purge <resource>]...        down and deallocate in the
      [--keep <resource>]... [--cwd] coordinator, then git worktree
                                     remove; works with the directory
                                     already gone. --purge names a
                                     state-path resource whose store is
                                     deleted, --keep one the spec purges
                                     on teardown or a machine to leave
                                     up. A store survives unless the spec
                                     says purge.on_teardown: always. The
                                     spec's own purge.flag, purge.keep_flag
                                     and keep_flag names are flags too
  wt bands reserve --host            reserve host-global ports no app may
      --port <p>... --note <text>    allocate from; the note names what
      [--json]                       holds the range
  wt list [--json] [--wide]          the registry across every repo: app,
                                     slug, slot, state, and the flags —
                                     stale (directory gone), unverifiable
                                     (a container path the coordinator
                                     cannot stat), reclaimable, foreign.
                                     seed credentials are redacted unless
                                     --wide is given to the owning client
  wt doctor [--json]                 read everything, write nothing; every
                                     finding names the command that fixes
                                     it
  wt reconcile [--json] [--dry-run]  repair entries — the caller's own plus
      [--cwd <dir>]                  aged-out ephemeral ones — through
                                     init's repair path (directory present)
                                     or reap+teardown+drop (directory
                                     gone); --dry-run previews and changes
                                     nothing
  wt clients [--json]                the known clients: kind, last seen,
                                     entries owned, and which ephemeral
                                     clients have aged out
  wt cleanup [--json] [--dry-run]    the only verb that destroys a
      [--cwd <dir>]                  worktree unattended: gated on gh
                                     (missing or unauthenticated gh cleans
                                     nothing and exits 4), asks gh whether
                                     each branch's PR is merged, applies
                                     the full rm safety checks even then,
                                     and never touches an unverifiable
                                     entry; --dry-run previews exactly
                                     what the real run does
  wt claude install [--json]         register wt as Claude Code's worktree
      [--dry-run] [--force]          creator: worktree creation from the
      [--skill=false] [--prefix <d>] editor's own control then allocates an
                                     environment for a repository with a
                                     wt.yaml, and makes the plain worktree
                                     Claude Code would have made for every
                                     other repository. Installs the
                                     worktree-onboarding skill too unless
                                     --skill=false
  wt claude uninstall [--json]       take the registration back out. The
      [--dry-run] [--force]          store, the registry and every
      [--skill=false] [--prefix <d>] allocated worktree are left alone
  wt --version                      print the version and the commit, then
                                     exit (what an installer reports when
                                     it replaces an older wt)
  wt help                            this help

every verb offers --json: exactly one JSON object on stdout, nothing else.
results go to stdout, diagnostics to stderr.

WT_ENDPOINT names the coordinator's location: a base URL such as
http://127.0.0.1:7833 (default when unset and no endpoint.json exists).
The coordinator writes endpoint.json — the base URL and the host token —
into WT_HOME/$HOME/.wt at startup; a host client reads it, and a
container, which never mounts the store, is given WT_ENDPOINT and
WT_CLIENT_TOKEN explicitly.

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
	case "--version", "-version":
		fmt.Fprintf(stdout, "wt %s (%s)\n", version, commit)
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
	case "claude":
		return runClaude(args[1:], stdout, stderr)
	case "bands":
		return runBands(args[1:], stdout, stderr)
	case "ports":
		return runPorts(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(args[1:], stdout, stderr)
	case "clients":
		return runClients(args[1:], stdout, stderr)
	case "cleanup":
		return runCleanup(args[1:], stdout, stderr)
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
			"run 'wt spec validate [path]', 'wt spec explain --slot N --slug S' or 'wt spec path --slug S'",
			"spec needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "validate":
		return runSpecValidate(args[1:], stdout, stderr)
	case "explain":
		return runSpecExplain(args[1:], stdout, stderr)
	case "path":
		return runSpecPath(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt spec validate [path]', 'wt spec explain --slot N --slug S' or 'wt spec path --slug S'",
			"unknown spec verb %q", args[0]))
		return ExitUsage
	}
}
