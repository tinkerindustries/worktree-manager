package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// runDaemon implements the three daemon verbs: `wt daemon status`, `wt
// daemon install` and `wt daemon uninstall`. Each takes --json, as every
// verb in the system does.
func runDaemon(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		WriteError(stderr, UsageError(
			"run 'wt daemon status', 'wt daemon install' or 'wt daemon uninstall'",
			"daemon needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "status":
		return runDaemonStatus(args[1:], stdout, stderr)
	case "install":
		return runDaemonInstall(args[1:], stdout, stderr)
	case "uninstall":
		return runDaemonUninstall(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt daemon status', 'wt daemon install' or 'wt daemon uninstall'",
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
	// Note carries a caveat a reader of the state needs — the systemd
	// lingering warning on Linux, which applies to every state (a
	// coordinator that dies at logout is a coordinator nobody notices
	// until the next login).
	Note string `json:"note,omitempty"`
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
	// The endpoint feeds the fix text; if it cannot be resolved the
	// state is still a state, and the fix says to set WT_ENDPOINT.
	base, _, _ := resolveEndpoint()
	dialErr := daemonDeps.reachable()
	res := daemonStatusOf(registered, running, dialErr, platform.DaemonFixesFor(base))
	// The lingering caveat applies to every state on Linux: a systemd
	// user unit stops at logout unless lingering is enabled, so a running
	// coordinator today is a dead one after logout. The note names the
	// remedy (loginctl enable-linger <user>).
	res.Note = platform.LingeringCaveat(*prefix)

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
	if res.Note != "" {
		fmt.Fprintf(stdout, "note: %s\n", res.Note)
	}
	return ExitOK
}

// daemonInstallResult is the one JSON object `wt daemon install --json`
// prints. RegistrationPath is the primary registration file that was
// written — the launchd plist on macOS, the systemd service unit on
// Linux, the scheduled-task XML on Windows (the field was plist_path
// before phase 8b; nothing outside internal/cli consumed the old name).
type daemonInstallResult struct {
	RegistrationPath string `json:"registration_path"`
	Label            string `json:"label"`
	Loaded           bool   `json:"loaded"`
	// Addr is the address pinned into the registration, and AddrChosen
	// says it was picked here rather than given. A --json consumer needs
	// both: with two users on one machine the second gets a port nobody
	// asked for, and nothing else on the machine reveals which.
	Addr       string `json:"addr"`
	AddrChosen bool   `json:"addr_chosen"`
	// ContainerClients reports whether a container token was configured,
	// never the token itself.
	ContainerClients bool   `json:"container_clients"`
	Note             string `json:"note,omitempty"`
}

// existingRegistrationAddr reports the address this machine's coordinator
// is already using, so a re-install keeps it rather than being walked onto
// a new port by the free-port probe.
//
// It reads `endpoint.json` rather than the registration file: the daemon
// writes it with the address it actually bound, and it is one file on every
// platform where the registration is a plist, a unit or task XML. The
// client's `WT_ENDPOINT` override is deliberately ignored — that says where
// a client should look, not what the coordinator was registered with.
//
// An absent or unreadable file means there is nothing to keep, and the
// caller probes instead. That is the first-install path.
func existingRegistrationAddr() (string, bool) {
	home, err := api.Home()
	if err != nil {
		return "", false
	}
	ep, err := api.ReadEndpoint(api.EndpointPath(home))
	if err != nil {
		return "", false
	}
	u, err := url.Parse(ep.BaseURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Host, true
}

// runDaemonInstall implements `wt daemon install [--prefix <dir>]
// [--wtd <path>] [--json]`: it registers the coordinator with the
// platform's supervisor and starts it. The --prefix option directs the
// registration at a temporary prefix instead of the real supervisor
// location — no test may install a real supervisor unit on the machine
// running it, and under a prefix nothing is loaded either. Each platform
// has its supervisor: launchd on macOS, the systemd user unit with its
// paired socket unit on Linux (phase 8b), the logon scheduled task on
// Windows (phase 8b). A platform with no supervisor refuses with exit 4
// naming the foreground alternative.
func runDaemonInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	prefix := fs.String("prefix", "", "registration prefix instead of the real supervisor location (tests and temp prefixes)")
	wtdFlag := fs.String("wtd", "", "path to the wtd binary (default: next to this wt binary)")
	addrFlag := fs.String("addr", "", "listen address to register (default: the coordinator's own default, or the next free port if it is taken)")
	containerToken := fs.String("container-token", "", "the token that admits container clients; at least 16 characters. Absent, the coordinator accepts host clients only")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback --addr (refused without this)")
	var allowedHosts allowHostList
	fs.Var(&allowedHosts, "allow-host", "a Host header value the coordinator accepts beyond loopback and its own address, e.g. host.docker.internal (repeatable); a container reaching the host by name needs its name here")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt daemon install' with no arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	// The address and the token are independent settings, each checked on
	// its own terms — this is the same check wtd itself runs, so a
	// registration that would be refused at the coordinator's first start
	// is refused here instead (platform.ValidateCoordinatorConfig).
	if err := platform.ValidateCoordinatorConfig(*addrFlag, *containerToken, *allowRemote, allowedHosts); err != nil {
		WriteError(stderr, New(ExitUsage, err.Error(),
			"re-run with a loopback address (or --allow-remote) and, if you configure one, a container token of at least 16 characters"))
		return ExitUsage
	}

	// With no --addr, pin a concrete free port into the registration rather
	// than leaving the unit to discover a clash at start. Two users on one
	// machine is the case that needs it: the second cannot bind the
	// default, and a registration that names a port it can never take is a
	// coordinator that never starts.
	//
	// A re-install keeps the address it already had. The probe would
	// otherwise move it every time: at probe time the running coordinator
	// still holds its own port, so the probe reads it as taken, steps past
	// it, and the reload frees the port the new registration has just
	// stopped naming. An upgrade would walk the coordinator onto a new port
	// and break every container configured with the old WT_ENDPOINT. The
	// probe is for a first install; `--addr` is how an address is changed
	// deliberately.
	addr := *addrFlag
	chosen := false
	reused := false
	if addr == "" {
		if existing, ok := existingRegistrationAddr(); ok {
			addr, reused = existing, true
		} else {
			var perr error
			addr, chosen, perr = platform.ChooseRegistrationAddr(api.DefaultAddr)
			if perr != nil {
				WriteError(stderr, New(ExitFailure, perr.Error(),
					"pass --addr <host:port> with a port you know is free"))
				return ExitFailure
			}
		}
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

	res, err := platform.InstallSupervisor(platform.InstallSupervisorOpts{
		Prefix: *prefix, WtdPath: wtd, Addr: addr,
		ContainerToken: *containerToken, AllowRemote: *allowRemote,
		AllowedHosts: allowedHosts,
	})
	if err != nil {
		if errors.Is(err, platform.ErrNoSupervisor) {
			WriteError(stderr, New(ExitUnavailable, err.Error(), platform.CoordinatorStartCommand("")))
			return ExitUnavailable
		}
		WriteError(stderr, New(ExitFailure, err.Error(),
			"check the registration path's permissions, then re-run: wt daemon install"))
		return ExitFailure
	}

	out := daemonInstallResult{
		RegistrationPath: res.RegistrationPath, Label: res.Label, Loaded: res.Loaded,
		Addr: addr, AddrChosen: chosen, ContainerClients: *containerToken != "",
		Note: res.Note,
	}
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
	fmt.Fprintf(stdout, "installed: %s\n", res.RegistrationPath)
	fmt.Fprintf(stdout, "loaded: %s\n", loaded)
	// The address is configuration and is always reported: a port chosen
	// for the operator rather than by them is the one they most need told,
	// because nothing else on the machine would reveal it. The token is a
	// secret and is never echoed — the operator already holds it, and an
	// install transcript that repeated it would be a leak.
	fmt.Fprintf(stdout, "listening on: %s\n", addr)
	if chosen {
		fmt.Fprintf(stdout, "note: %s was already in use, so this registration takes the next free port\n",
			api.DefaultAddr)
	}
	if reused {
		fmt.Fprintf(stdout, "note: kept the address this coordinator was already using (pass --addr to change it)\n")
	}
	if *containerToken != "" {
		fmt.Fprintf(stdout, "container clients: admitted (set WT_ENDPOINT to http://%s and WT_CLIENT_TOKEN to the token)\n", addr)
	} else {
		fmt.Fprintf(stdout, "container clients: not admitted (re-run with --container-token to admit them)\n")
	}
	if res.Note != "" {
		fmt.Fprintf(stdout, "%s\n", res.Note)
	}
	return ExitOK
}

// allowHostList collects a repeatable --allow-host flag. Each occurrence
// appends, so the flag reads as "and also this host" rather than the last
// one winning, and the registration carries every value the operator gave.
type allowHostList []string

func (l *allowHostList) String() string { return strings.Join(*l, ",") }

func (l *allowHostList) Set(v string) error {
	*l = append(*l, v)
	return nil
}
