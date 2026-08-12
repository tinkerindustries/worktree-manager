package platform

import (
	"errors"
	"fmt"
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
		return "", ErrNoSupervisor // the logon-task registration is phase 8b, commit 3
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
		return LaunchAgentFilename // placeholder until the logon-task registration lands (phase 8b, commit 3)
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
	switch runtime.GOOS {
	case "darwin":
		return installLaunchAgent(opts)
	case "linux":
		return installSystemdUnits(opts.Prefix, opts.WtdPath)
	}
	return InstallSupervisorResult{}, ErrNoSupervisor
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
	if err := os.WriteFile(path, launchdPlist(opts.WtdPath), 0o644); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("writing %s: %w", path, err)
	}
	if opts.Prefix != "" {
		return InstallSupervisorResult{
			RegistrationPath: path, Label: LaunchAgentLabel, Loaded: false,
			Note: "registration written under a test prefix; no launchd state was touched",
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
// C API.
func launchdPlist(wtdPath string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`, LaunchAgentLabel, xmlEscape(wtdPath)))
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
// command that fixes it).
func CoordinatorStartCommand(socketPath string) string {
	switch runtime.GOOS {
	case "darwin":
		return "wt daemon install"
	case "linux":
		return "register and start the coordinator: wt daemon install (installs the systemd user unit and its paired socket unit)"
	case "windows":
		return "install the coordinator as a Windows service or logon task (phase 8b); for now run wtd in the foreground"
	default:
		if socketPath == "" {
			return "run wtd in the foreground with WT_SOCKET set"
		}
		return fmt.Sprintf("run wtd in the foreground: WT_SOCKET=%s wtd", socketPath)
	}
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
func DaemonFixesFor(socketPath string) DaemonFixes {
	start := CoordinatorStartCommand(socketPath)
	switch runtime.GOOS {
	case "darwin":
		domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), LaunchAgentLabel)
		return DaemonFixes{
			NotRegistered:      "register and start the coordinator: wt daemon install",
			RegisteredStopped:  fmt.Sprintf("start the coordinator: launchctl kickstart %s (or re-run: wt daemon install)", domain),
			RunningUnreachable: fmt.Sprintf("restart the coordinator: launchctl kickstart -k %s — and check that WT_SOCKET names the socket the coordinator listens on", domain),
		}
	case "linux":
		return DaemonFixes{
			NotRegistered:      "register and start the coordinator: wt daemon install",
			RegisteredStopped:  fmt.Sprintf("start the coordinator: systemctl --user start %s (or re-run: wt daemon install)", SystemdSocketFilename),
			RunningUnreachable: fmt.Sprintf("restart the coordinator: systemctl --user restart %s — and check that WT_SOCKET matches the socket unit's ListenStream (systemctl --user cat %s)", SystemdSocketFilename, SystemdSocketFilename),
		}
	case "windows":
		return DaemonFixes{
			NotRegistered:      "install the coordinator as a Windows service or logon task (phase 8b); for now run wtd in the foreground",
			RegisteredStopped:  "start the coordinator (phase 8b); for now run wtd in the foreground",
			RunningUnreachable: "restart the coordinator (phase 8b); for now run wtd in the foreground",
		}
	default:
		return DaemonFixes{
			NotRegistered:      start,
			RegisteredStopped:  start,
			RunningUnreachable: start + " — and check that WT_SOCKET names the socket the coordinator listens on",
		}
	}
}
