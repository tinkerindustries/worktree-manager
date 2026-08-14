package platform

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The coordinator's supervisor registration. macOS runs a launchd
// LaunchAgent, Linux a systemd user unit with its paired .socket unit
// (systemd_linux.go), Windows a logon scheduled task (task_windows.go).
// The registration file — the plist, the service unit or the task XML —
// is the "registered" observation `daemon status` checks
// (docs/ARCHITECTURE.md §13.1).
const (
	LaunchAgentLabel    = "com.mrgeoffrich.wtd"
	LaunchAgentFilename = LaunchAgentLabel + ".plist"
	// SystemdUnitLabel is the systemd unit name prefix, matching the
	// launchd label. The two units are <label>.service and
	// <label>.socket (systemd_linux.go).
	SystemdUnitLabel = "com.mrgeoffrich.wtd"
	// SystemdServiceFilename is the service unit's filename. The service
	// is the registration's primary file (what "registered" checks), the
	// .socket unit is its paired half.
	SystemdServiceFilename = SystemdUnitLabel + ".service"
	// SystemdSocketFilename is the socket unit's filename. The socket
	// unit owns the listener; the service consumes it via LISTEN_FDS.
	SystemdSocketFilename = SystemdUnitLabel + ".socket"
	// WindowsTaskName is the scheduled task's name; WindowsTaskFilename
	// is the task XML the scheduler database is registered from (the
	// logon-task registration, phase 8b).
	WindowsTaskName     = "com.mrgeoffrich.wtd"
	WindowsTaskFilename = WindowsTaskName + ".xml"
)

// ErrNoSupervisor is returned where no supervisor exists: a platform this
// project does not target. The remedy always names the foreground mode.
var ErrNoSupervisor = errors.New("no coordinator supervisor on this platform; run wtd in the foreground instead")

// SupervisorRegistrationPath returns the path of the coordinator's
// supervisor registration file. A prefix overrides the real location so a
// test can direct the registration at a temporary directory — no test may
// install a real supervisor unit on the machine running it — and under a
// prefix the registration file is inert data on every platform. Without a
// prefix: macOS ~/Library/LaunchAgents, Linux the user's systemd
// directory, Windows %LOCALAPPDATA%\wt (the task XML the scheduler
// database is registered from).
func SupervisorRegistrationPath(prefix string) (string, error) {
	if prefix != "" {
		return filepath.Join(prefix, SupervisorFilename(runtime.GOOS)), nil
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving the LaunchAgents directory: %w", err)
		}
		return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentFilename), nil
	case "linux":
		dir, err := systemdUserDir("")
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, SystemdServiceFilename), nil
	case "windows":
		dir, err := windowsTaskDir("")
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, WindowsTaskFilename), nil
	}
	return "", ErrNoSupervisor
}

// SupervisorFilename is the registration filename under a test prefix,
// matched to the platform so a prefixed install plants the file a
// prefixed status check looks for.
func SupervisorFilename(goos string) string {
	switch goos {
	case "darwin":
		return LaunchAgentFilename
	case "linux":
		return SystemdServiceFilename
	case "windows":
		return WindowsTaskFilename
	}
	return LaunchAgentFilename
}

// SupervisorRunning reports whether the supervisor currently runs the
// coordinator. On macOS this is launchctl's view, on Linux systemd's
// view of the socket unit, on Windows the Task Scheduler's view of the
// task — each independent of the socket, which is what lets daemon
// status distinguish "registered but stopped" from "running but
// unreachable". A prefix means a test never loaded anything, so the
// answer is false without consulting the supervisor.
func SupervisorRunning(prefix string) (bool, error) {
	switch {
	case runtime.GOOS == "darwin" && prefix == "":
		return launchdRunning()
	case runtime.GOOS == "linux" && prefix == "":
		return systemdRunning(prefix)
	case runtime.GOOS == "windows" && prefix == "":
		return taskSchedulerRunning(prefix)
	}
	return false, nil
}

// LingeringCaveat reports the systemd lingering caveat — a systemd user
// unit stops at logout unless lingering is enabled — where a reader of
// `daemon status` or `daemon install` will see it. Empty on platforms
// without the caveat and under test prefixes. The caveat names the
// remedy: `loginctl enable-linger <user>`.
func LingeringCaveat(prefix string) string { return lingeringCaveat(prefix) }

// InstallSupervisorOpts directs the coordinator's supervisor registration.
type InstallSupervisorOpts struct {
	// Prefix overrides the registration directory (tests). Empty means
	// the real location on the platform's supervisor.
	Prefix string
	// WtdPath is the absolute path the registration will exec — the
	// coordinator binary, normally a sibling of the wt binary that is
	// running `wt daemon install`.
	WtdPath string
	// Addr is the listen address the registration starts wtd with, so a
	// supervisor-managed coordinator listens exactly like a foreground
	// one. Empty means the unit omits --addr and wtd uses its default.
	Addr string
	// ContainerToken is the token that admits container clients. Empty
	// means the unit omits --container-token and the coordinator accepts
	// host clients only. A registration file that carries the token is
	// written 0600 on unix so a machine's other users cannot read it out
	// of the unit file.
	//
	// Addr and ContainerToken are independent: before R1 a TCP listener
	// and its token were opt-in as a pair, but the listener is now the
	// only surface and has a default, so a custom address needs no token
	// (ValidateCoordinatorConfig checks each on its own terms).
	ContainerToken string
	// AllowRemote permits a non-loopback Addr, and is written into the
	// registration so the started coordinator agrees with the check made
	// here.
	AllowRemote bool
	// AllowedHosts are the extra Host header values the registered
	// coordinator accepts beyond loopback and its own address
	// (--allow-host, repeatable). A container that reaches the host by
	// name — host.docker.internal on Docker Desktop — sends that name as
	// its Host, and the DNS-rebinding guard refuses it until the operator
	// names it here. Empty means the unit omits --allow-host and the
	// guard admits loopback and the coordinator's own address alone.
	AllowedHosts []string
}

// InstallSupervisorResult reports what registration wrote and whether the
// supervisor was asked to start the coordinator.
type InstallSupervisorResult struct {
	// RegistrationPath is the primary registration file that was written
	// (the plist, the service unit, or the task XML).
	RegistrationPath string
	Label            string
	Loaded           bool
	Note             string
}

// InstallSupervisor registers the coordinator with the platform's
// supervisor and starts it. The macOS plist deliberately uses RunAtLoad
// and KeepAlive rather than launchd socket activation: launchd hands the
// listener over through launch_activate_socket, a C API, and this project
// is CGO_ENABLED=0 throughout — there is no pure-Go path to that file
// descriptor (docs/ARCHITECTURE.md §4.1 names the Sockets key; the phase
// 2 brief overrides it for that reason). Linux does use systemd socket
// activation, whose LISTEN_FDS handoff is pure-Go readable
// (systemd_linux.go); Windows registers a logon scheduled task
// (task_windows.go).
//
// Under a prefix the registration file is written and nothing is loaded —
// the point of the prefix is that a test never touches the machine's
// supervisor.
func InstallSupervisor(opts InstallSupervisorOpts) (InstallSupervisorResult, error) {
	if opts.WtdPath == "" {
		return InstallSupervisorResult{}, errors.New("the registration file needs the coordinator binary path (wtd)")
	}
	if err := ValidateCoordinatorConfig(opts.Addr, opts.ContainerToken, opts.AllowRemote, opts.AllowedHosts); err != nil {
		return InstallSupervisorResult{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchAgent(opts)
	case "linux":
		return installSystemdUnits(opts.Prefix, opts.WtdPath, opts.Addr, opts.ContainerToken, opts.AllowedHosts)
	case "windows":
		return installWindowsTask(opts.Prefix, opts.WtdPath, opts.Addr, opts.ContainerToken, opts.AllowedHosts)
	}
	return InstallSupervisorResult{}, ErrNoSupervisor
}

// UninstallSupervisorResult reports what uninstalling the registration
// removed and whether the supervisor was asked to stop the coordinator.
type UninstallSupervisorResult struct {
	// RegistrationPath is the primary registration file that would have
	// been removed (the plist, the service unit, or the task XML) — the
	// same file InstallSupervisorResult reports.
	RegistrationPath string
	// Removed reports whether the registration file existed and was
	// removed. False means nothing was registered there.
	Removed bool
	// Stopped reports whether the supervisor was asked to stop the
	// coordinator. False under a test prefix, which never loaded anything.
	Stopped bool
	// Note carries a platform caveat a reader of the result needs — for
	// example launchd still reporting the agent loaded after bootout.
	Note string
}

// UninstallSupervisor reverses InstallSupervisor: stop the coordinator,
// deregister it from the platform's supervisor and remove the registration
// file. The per-platform order is the task's: stop, deregister, remove
// (launchd bootout, systemd disable --now, schtasks /End then /Delete).
// Under a prefix nothing is loaded, so only the registration file(s) are
// removed — the point of the prefix is that a test never touches the
// machine's supervisor.
//
// Uninstalling something never installed succeeds: absence is the goal,
// so a second `wt daemon uninstall` is a no-op that reports nothing was
// registered rather than an error.
func UninstallSupervisor(prefix string) (UninstallSupervisorResult, error) {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchAgent(prefix)
	case "linux":
		return uninstallSystemdUnits(prefix)
	case "windows":
		return uninstallWindowsTask(prefix)
	}
	return UninstallSupervisorResult{}, ErrNoSupervisor
}

// removeRegistrationFile removes one registration file, tolerating its
// absence — an uninstall of something never installed is an uninstall that
// succeeded. It reports whether the file existed.
func removeRegistrationFile(path string) (bool, error) {
	err := os.Remove(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("removing %s: %w", path, err)
}

// installLaunchAgent writes the plist and, outside a prefix, loads it.
func installLaunchAgent(opts InstallSupervisorOpts) (InstallSupervisorResult, error) {
	path, err := SupervisorRegistrationPath(opts.Prefix)
	if err != nil {
		return InstallSupervisorResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("creating the registration directory %s: %w", filepath.Dir(path), err)
	}
	mode := os.FileMode(0o644)
	if opts.ContainerToken != "" {
		// The plist carries the TCP token: readable by the owner alone, so
		// a machine's other users cannot lift the token out of the unit
		// file and connect over the loopback surface (the security pass,
		// phase 9).
		mode = 0o600
	}
	if err := os.WriteFile(path, launchdPlist(opts.WtdPath, opts.Addr, opts.ContainerToken, opts.AllowedHosts), mode); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("writing %s: %w", path, err)
	}
	if opts.Prefix != "" {
		note := "registration written under a test prefix; no launchd state was touched"
		if opts.ContainerToken != "" {
			note += "; the registration carries the loopback TCP token"
		}
		return InstallSupervisorResult{
			RegistrationPath: path, Label: LaunchAgentLabel, Loaded: false,
			Note: note,
		}, nil
	}
	// Real registration: macOS only — the prefix-less path on Linux and
	// Windows is handled by their own installers above.
	if err := loadLaunchAgent(path); err != nil {
		return InstallSupervisorResult{RegistrationPath: path, Label: LaunchAgentLabel},
			fmt.Errorf("registering %s with launchd: %w", path, err)
	}
	return InstallSupervisorResult{RegistrationPath: path, Label: LaunchAgentLabel, Loaded: true}, nil
}

// launchdPlist is the LaunchAgent property list. RunAtLoad starts the
// coordinator when the user logs in, KeepAlive restarts it if it exits —
// the lifecycle launchd socket activation would have provided, without the
// C API. With the container token configured, ProgramArguments carries
// --addr and --container-token; each argument is its own element, so the
// token needs no escaping beyond the XML text rules.
func launchdPlist(wtdPath, addr, containerToken string, allowedHosts []string) []byte {
	args := []string{wtdPath}
	// Each flag is emitted on its own terms. They were coupled while the
	// TCP listener was opt-in as a pair; emitting them together now would
	// write an empty --container-token whenever only an address was given,
	// and an empty token admits no container while looking like it does.
	if addr != "" {
		args = append(args, "--addr", addr)
	}
	if containerToken != "" {
		args = append(args, "--container-token", containerToken)
	}
	for _, h := range allowedHosts {
		args = append(args, "--allow-host", h)
	}
	var elems strings.Builder
	for _, a := range args {
		fmt.Fprintf(&elems, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`, LaunchAgentLabel, elems.String()))
}

// xmlEscape makes a string safe inside the plist's element content.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// CoordinatorBinaryPath is where the coordinator binary sits next to the
// running client binary — the distribution shape (both binaries ship
// together, phase 9), which is what `wt daemon install` registers.
func CoordinatorBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the running binary: %w", err)
	}
	name := "wtd"
	if runtime.GOOS == "windows" {
		name = "wtd.exe"
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

// CoordinatorStartCommand is the command that starts the coordinator, for
// the exit-5 remedy and daemon status (plan.md §3: every error names the
// command that fixes it). endpoint is the resolved base URL (""
// when unresolved); the foreground form names the listen address so the
// user starts wtd where the client expects it.
func CoordinatorStartCommand(endpoint string) string {
	switch runtime.GOOS {
	case "darwin":
		return "wt daemon install"
	case "linux":
		return "register and start the coordinator: wt daemon install (installs the systemd user unit and its paired socket unit)"
	case "windows":
		return "register and start the coordinator: wt daemon install (installs the logon task)"
	default:
		addr := endpointAddr(endpoint)
		if addr == "" {
			return "run wtd in the foreground: wtd (listening on 127.0.0.1:7833 by default)"
		}
		return fmt.Sprintf("run wtd in the foreground: wtd --addr %s", addr)
	}
}

// endpointAddr extracts the host:port from a base URL, for the
// foreground start command's --addr.
func endpointAddr(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// DaemonFixes carries the three broken-state remedies daemon status names.
// They are deliberately different: one says install, one says start, one
// says restart — the distinction is the verb's whole point
// (ARCHITECTURE.md §13.1).
type DaemonFixes struct {
	NotRegistered      string
	RegisteredStopped  string
	RunningUnreachable string
}

// DaemonFixesFor returns the platform's remedies in the platform's terms.
// endpoint is the resolved base URL, feeding the running-but-unreachable
// check text.
func DaemonFixesFor(endpoint string) DaemonFixes {
	start := CoordinatorStartCommand(endpoint)
	addr := endpointAddr(endpoint)
	check := "— and check that WT_ENDPOINT (or endpoint.json) names the address the coordinator listens on"
	switch runtime.GOOS {
	case "darwin":
		domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchAgentLabel)
		return DaemonFixes{
			NotRegistered:      "register and start the coordinator: wt daemon install",
			RegisteredStopped:  fmt.Sprintf("start the coordinator: launchctl kickstart %s (or re-run: wt daemon install)", domain),
			RunningUnreachable: fmt.Sprintf("restart the coordinator: launchctl kickstart -k %s %s", domain, check),
		}
	case "linux":
		return DaemonFixes{
			NotRegistered:      "register and start the coordinator: wt daemon install",
			RegisteredStopped:  fmt.Sprintf("start the coordinator: systemctl --user start %s (or re-run: wt daemon install)", SystemdSocketFilename),
			RunningUnreachable: fmt.Sprintf("restart the coordinator: systemctl --user restart %s — and check that the socket unit's ListenStream matches %s (systemctl --user cat %s)", SystemdSocketFilename, addr, SystemdSocketFilename),
		}
	case "windows":
		return DaemonFixes{
			NotRegistered:      "register and start the coordinator: wt daemon install",
			RegisteredStopped:  fmt.Sprintf("start the coordinator: schtasks /Run /TN %s (or re-run: wt daemon install)", WindowsTaskName),
			RunningUnreachable: fmt.Sprintf("restart the coordinator: schtasks /End /TN %s, then schtasks /Run /TN %s %s", WindowsTaskName, WindowsTaskName, check),
		}
	default:
		return DaemonFixes{
			NotRegistered:      start,
			RegisteredStopped:  start,
			RunningUnreachable: start + " " + check,
		}
	}
}
