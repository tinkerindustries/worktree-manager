package cli

// uninstall.go is `wt daemon uninstall`: the reverse of `wt daemon
// install`. It stops the coordinator, deregisters it from the platform's
// supervisor and removes the registration file — all client-local, no
// coordinator call, no route (the HTTP surface is frozen). It takes
// --prefix exactly as install does, so a test never touches the machine's
// real supervisor.
//
// The store is never removed: wt.db is the only record of what is
// allocated on this machine, and deleting it would strand every container,
// port and VM the tool has handed out. Uninstall prints the store path and
// says it was left alone. And it refuses while entries are live — a clean
// uninstall over three still-allocated worktrees would leave nothing that
// knows how to release them. --force is the only way past the refusal.

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// countRegistryEntries reports how many entries the registry holds, by
// asking the coordinator.
//
// It asks rather than reading wt.db, and the distinction is the whole
// point of the client/coordinator split: the store database is never
// client-readable, only endpoint.json is (ARCHITECTURE.md §8.2; CLAUDE.md,
// "The two binaries"). Reading it here would also link SQLite into the
// client — 30 packages and 6 MB for one COUNT(*) — and would put a second
// copy of the store's path and DSN rules somewhere they can drift from
// internal/store's.
//
// The consequence is deliberate and is what --force is for: with the
// coordinator unreachable, the client cannot know what is allocated, so it
// refuses instead of guessing. An uninstall that cannot verify is an
// uninstall that may strand allocations, and refusing is this codebase's
// answer to that everywhere else.
func countRegistryEntries() (int, *Error) {
	sess, err := dialCoordinator()
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	list, lerr := sess.client.List(&api.ListArgs{})
	if lerr != nil {
		return 0, requestErr(sess.endpoint, api.VerbList, lerr)
	}
	return len(list.Entries), nil
}

// daemonUninstallResult is the one JSON object `wt daemon uninstall
// --json` prints.
type daemonUninstallResult struct {
	RegistrationPath string `json:"registration_path,omitempty"`
	// Removed reports whether the registration file existed and was
	// removed; Stopped whether the supervisor was asked to stop the
	// coordinator. Both false means nothing was registered.
	Removed bool `json:"removed"`
	Stopped bool `json:"stopped"`
	// StorePath is where the store lives and StoreKept that uninstall
	// never touches it — the database is the only record of what is
	// allocated on this machine.
	StorePath string `json:"store_path"`
	StoreKept bool   `json:"store_kept"`
	Note      string `json:"note,omitempty"`
}

// runDaemonUninstall implements `wt daemon uninstall [--prefix <dir>]
// [--force] [--json]`: it stops the coordinator, deregisters it from the
// platform's supervisor and removes the registration file, and it never
// touches the store. Before anything is unregistered it refuses while the
// registry still holds entries — a clean uninstall would leave nothing
// that knows how to release the machine's allocations — naming how many
// and the commands that resolve them; --force is the only way past the
// refusal, and it does not release anything either, it just proceeds with
// the store untouched.
func runDaemonUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	prefix := fs.String("prefix", "", "registration prefix instead of the real supervisor location (tests and temp prefixes)")
	force := fs.Bool("force", false, "uninstall even while entries are still allocated (the store is left in place; nothing is released)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt daemon uninstall' with no arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	// The live-entry refusal, before anything is changed. An unreadable
	// registry is a refusal too: whether entries are live cannot be
	// verified, and an uninstall that cannot verify is an uninstall that
	// may strand allocations (refuse rather than partially honour).
	if !*force {
		n, cerr := countRegistryEntries()
		if cerr != nil {
			WriteError(stderr, New(ExitRefused,
				fmt.Sprintf("cannot tell whether anything is still allocated: %s", cerr.Msg),
				"start the coordinator and re-run so the check can run, or pass --force to uninstall without it (anything still allocated will be stranded)"))
			return ExitRefused
		}
		if n > 0 {
			noun := "entries"
			if n == 1 {
				noun = "entry"
			}
			WriteError(stderr, New(ExitRefused,
				fmt.Sprintf("%d %s are still allocated: uninstalling would leave nothing that knows how to release them", n, noun),
				"run 'wt list' to see them, then 'wt rm --slug <s>' for each — and only then uninstall (or pass --force to uninstall anyway; the store is left in place and nothing is released)"))
			return ExitRefused
		}
	}

	res, err := platform.UninstallSupervisor(*prefix)
	if err != nil {
		if errors.Is(err, platform.ErrNoSupervisor) {
			WriteError(stderr, New(ExitUnavailable, err.Error(),
				"there is no supervisor registration to remove on this platform; run 'wt daemon status' to see what state the coordinator is in"))
			return ExitUnavailable
		}
		WriteError(stderr, New(ExitFailure, err.Error(),
			"check the registration path's permissions, then re-run: wt daemon uninstall"))
		return ExitFailure
	}

	home, err := api.Home()
	if err != nil {
		WriteError(stderr, New(ExitFailure, err.Error(), "set WT_HOME or $HOME and re-run: wt daemon uninstall"))
		return ExitFailure
	}

	out := daemonUninstallResult{
		RegistrationPath: res.RegistrationPath,
		Removed:          res.Removed,
		Stopped:          res.Stopped,
		StorePath:        home,
		StoreKept:        true,
		Note:             res.Note,
	}
	if *jsonOut {
		if err := WriteJSON(stdout, out); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	if res.Removed {
		fmt.Fprintf(stdout, "uninstalled: %s\n", res.RegistrationPath)
	} else {
		fmt.Fprintf(stdout, "registration: none (nothing to uninstall at %s)\n", res.RegistrationPath)
	}
	if res.Stopped {
		fmt.Fprintf(stdout, "coordinator: stopped\n")
	}
	fmt.Fprintf(stdout, "store: %s — left alone (it is the only record of what is allocated on this machine; removing it would strand every container, port and VM)\n", home)
	if res.Note != "" {
		fmt.Fprintf(stdout, "note: %s\n", res.Note)
	}
	return ExitOK
}
