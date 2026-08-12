package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// runDaemon implements the two daemon verbs: `wt daemon status` and
// `wt daemon install`. Both take --json, as every verb in the system does.
func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		WriteError(stderr, UsageError(
			"run 'wt daemon status' or 'wt daemon install'",
			"daemon needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "status":
		return runDaemonStatus(args[1:], stdout, stderr)
	case "install":
		return runDaemonInstall(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt daemon status' or 'wt daemon install'",
			"unknown daemon verb %q", args[0]))
		return ExitUsage
	}
}

// daemonStatusResult is the one JSON object `wt daemon status --json`
// prints.
type daemonStatusResult struct {
	State      string `json:"state"` // running | not_registered | registered_stopped | running_unreachable
	Registered bool   `json:"registered"`
	Running    bool   `json:"running"`
	Reachable  bool   `json:"reachable"`
	Fix        string `json:"fix,omitempty"`
}

// daemonStatusDeps are the observations `daemon status` is built from, as
// functions so the tests can drive all four states without a supervisor:
// whether the registration file exists, whether the supervisor says the
// coordinator is running, and whether the socket answers the hello.
type daemonStatusDeps struct {
	registered func(prefix string) (bool, error)
	running    func(prefix string) (bool, error)
	reachable  func() error
}

// daemonDeps is the production wiring; tests replace it.
var daemonDeps = daemonStatusDeps{
	registered: func(prefix string) (bool, error) {
		path, err := platform.SupervisorRegistrationPath(prefix)
		if err != nil {
			return false, nil // no supervisor on this platform: not registered
		}
		_, err = os.Stat(path)
		return err == nil, nil
	},
	running: func(prefix string) (bool, error) {
		return platform.SupervisorRunning(prefix)
	},
	reachable: func() error {
		// The one dial-and-hello path, reused: any failure — connection
		// refused, no hello answer, a refused hello — means unreachable.
		// Status treats the failure as data, which is why it does not
		// propagate the exit code. The typed nil matters: dialCoordinator
		// returns a *Error, and returning it straight would hand the state
		// machine a non-nil interface holding a nil pointer.
		sess, err := dialCoordinator()
		if sess != nil {
			sess.Close() // status only probes; it holds no conversation
		}
		if err != nil {
			return err
		}
		return nil
	},
}

// daemonStatusOf is the state machine: one of the four states, with a fix
// for each of the three broken ones. The fixes are deliberately different —
// one says install, one says start, one says restart (ARCHITECTURE.md
// §13.1). Reachability wins: a socket that answers the hello is a working
// coordinator whatever the supervisor says (a foreground coordinator on a
// platform with no supervisor reports running, which is the truth).
func daemonStatusOf(registered, running bool, dialErr error, fixes platform.DaemonFixes) daemonStatusResult {
	if dialErr == nil {
		return daemonStatusResult{State: "running", Registered: registered, Running: running, Reachable: true}
	}
	switch {
	case registered && running:
		return daemonStatusResult{State: "running_unreachable", Registered: true, Running: true, Fix: fixes.RunningUnreachable}
	case registered:
		return daemonStatusResult{State: "registered_stopped", Registered: true, Fix: fixes.RegisteredStopped}
	default:
		return daemonStatusResult{State: "not_registered", Fix: fixes.NotRegistered}
	}
}

// stateHuman is the human form of each state.
func stateHuman(s string) string {
	switch s {
	case "not_registered":
		return "not registered"
	case "registered_stopped":
		return "registered but stopped"
	case "running_unreachable":
		return "running but unreachable"
	default:
		return s
	}
}

// runDaemonStatus implements `wt daemon status [--prefix <dir>] [--json]`:
// it distinguishes the four states and names a different fix for each
// broken one. It never exits 5 — the states are its answer, not an error.
func runDaemonStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	prefix := fs.String("prefix", "", "registration prefix instead of the real supervisor location (tests and temp prefixes)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt daemon status' with no arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	registered, err := daemonDeps.registered(*prefix)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("checking the supervisor registration: %v", err), ""))
		return ExitFailure
	}
	running, err := daemonDeps.running(*prefix)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("asking the supervisor: %v", err), ""))
		return ExitFailure
	}
	// The socket path feeds the fix text; if it cannot be resolved the
	// state is still a state, and the fix says to set WT_SOCKET.
	socketPath, _ := platform.SocketPath()
	dialErr := daemonDeps.reachable()
	res := daemonStatusOf(registered, running, dialErr, platform.DaemonFixesFor(socketPath))

	if *jsonOut {
		if err := WriteJSON(stdout, res); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "state: %s\n", stateHuman(res.State))
	if res.Fix != "" {
		fmt.Fprintf(stdout, "fix: %s\n", res.Fix)
	}
	return ExitOK
}

// daemonInstallResult is the one JSON object `wt daemon install --json`
// prints.
type daemonInstallResult struct {
	PlistPath string `json:"plist_path"`
	Label     string `json:"label"`
	Loaded    bool   `json:"loaded"`
	Note      string `json:"note,omitempty"`
}

// runDaemonInstall implements `wt daemon install [--prefix <dir>]
// [--wtd <path>] [--json]`: it registers the coordinator with the
// platform's supervisor and starts it. The --prefix option directs the
// registration at a temporary prefix instead of the real
// ~/Library/LaunchAgents — no test may install a LaunchAgent on the
// machine running it, and under a prefix nothing is loaded either.
// macOS launchd is the one supervisor this phase registers with; on Linux
// and Windows a real registration refuses with exit 4 naming the phase-8
// unit and the foreground alternative.
func runDaemonInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	prefix := fs.String("prefix", "", "registration prefix instead of the real supervisor location (tests and temp prefixes)")
	wtdFlag := fs.String("wtd", "", "path to the wtd binary (default: next to this wt binary)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt daemon install' with no arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	wtd := *wtdFlag
	if wtd == "" {
		var err error
		wtd, err = platform.CoordinatorBinaryPath()
		if err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), "pass --wtd <path> to the coordinator binary"))
			return ExitFailure
		}
	}
	if _, err := os.Stat(wtd); err != nil {
		WriteError(stderr, New(ExitUnavailable,
			fmt.Sprintf("the coordinator binary is not at %s: %v", wtd, err),
			"build it next to wt: go build ./cmd/wt ./cmd/wtd (or pass --wtd <path>)"))
		return ExitUnavailable
	}

	res, err := platform.InstallSupervisor(platform.InstallSupervisorOpts{Prefix: *prefix, WtdPath: wtd})
	if err != nil {
		if errors.Is(err, platform.ErrNoSupervisor) {
			WriteError(stderr, New(ExitUnavailable, err.Error(), platform.CoordinatorStartCommand("")))
			return ExitUnavailable
		}
		WriteError(stderr, New(ExitFailure, err.Error(),
			"check the registration path's permissions, then re-run: wt daemon install"))
		return ExitFailure
	}

	out := daemonInstallResult{PlistPath: res.PlistPath, Label: res.Label, Loaded: res.Loaded, Note: res.Note}
	if *jsonOut {
		if err := WriteJSON(stdout, out); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	loaded := "no"
	if res.Loaded {
		loaded = "yes"
	}
	fmt.Fprintf(stdout, "installed: %s\n", res.PlistPath)
	fmt.Fprintf(stdout, "loaded: %s\n", loaded)
	if res.Note != "" {
		fmt.Fprintf(stdout, "%s\n", res.Note)
	}
	return ExitOK
}
