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
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	_ "modernc.org/sqlite"
)

// dbFileName is the coordinator's database inside the store root. The name
// is restated here rather than imported from internal/store — the client
// must not import the coordinator-only packages (cmd/wt's package-boundary
// test) — and it is part of the documented store contract (CLAUDE.md,
// "Environment": the database at <WT_HOME|$HOME/.wt>/wt.db).
const dbFileName = "wt.db"

// countRegistryEntries reports how many entries the store's registry
// holds, by reading the coordinator's database directly.
//
// This is the one place the client opens wt.db, and the exception to the
// rule that the store database is never client-readable (endpoint.go) is
// deliberate: `wt daemon uninstall` must refuse while entries are live,
// and it is the one verb that has to work when the coordinator is dead or
// gone — a broken installation is exactly when uninstall is needed, and a
// check that dialed the coordinator would make the refusal unanswerable.
// Only the count is read, never an entry's content, so the store keeps its
// secrets. The database is opened read-write like the coordinator opens
// it, because SQLite cannot reliably read a WAL-mode database read-only
// (the -shm wal-index needs write access); nothing is ever written. A
// database from a newer schema that renamed or dropped `entries` refuses
// here with "no such table", the store's own refusal rule (a newer schema
// is never half-read).
//
// A missing database means a machine nothing was ever allocated on: zero
// entries, no refusal.
func countRegistryEntries() (int, error) {
	root, err := api.Home()
	if err != nil {
		return 0, err
	}
	path := filepath.Join(root, dbFileName)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil // no store: nothing is allocated
		}
		return 0, fmt.Errorf("reading the registry at %s: %w", path, err)
	}
	// The path is URL-escaped because SQLite parses the DSN as a URI (a
	// store root whose path contains '?' or '#' must still open) — the
	// same escaping the store's own dsn() applies.
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.ToSlash(path))+
		"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, fmt.Errorf("reading the registry at %s: %w", path, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM entries").Scan(&n); err != nil {
		return 0, fmt.Errorf("reading the registry at %s: %w", path, err)
	}
	return n, nil
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
		n, err := countRegistryEntries()
		if err != nil {
			WriteError(stderr, New(ExitRefused, err.Error(),
				"find out what is allocated first: run 'wt list' (needs the coordinator running), then re-run: wt daemon uninstall (or pass --force)"))
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
