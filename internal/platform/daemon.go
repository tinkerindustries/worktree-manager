package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The launchd agent that supervises the coordinator on macOS. Only macOS
// has a supervisor in this phase: the Linux systemd unit and the Windows
// service or logon task are phase 8 (plan.md §5, "Phase 8").
const (
	LaunchAgentLabel    = "com.mrgeoffrich.wtd"
	LaunchAgentFilename = LaunchAgentLabel + ".plist"
)

// ErrNoSupervisor is returned where this phase has no supervisor to
// register with — Linux and Windows. The remedy always names the
// foreground mode and the phase that owns the unit.
var ErrNoSupervisor = errors.New("no coordinator supervisor on this platform in this phase (the Linux systemd unit and the Windows service are phase 8); run wtd in the foreground instead")

// SupervisorRegistrationPath returns the path of the coordinator's
// supervisor registration file. A prefix overrides the real location so a
// test can direct the registration at a temporary directory — no test may
// install a LaunchAgent on the machine running it — and under a prefix the
// registration file is inert data on every platform. Without a prefix,
// macOS resolves ~/Library/LaunchAgents and Linux/Windows refuse with
// ErrNoSupervisor.
func SupervisorRegistrationPath(prefix string) (string, error) {
	if prefix != "" {
		return filepath.Join(prefix, LaunchAgentFilename), nil
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving the LaunchAgents directory: %w", err)
		}
		return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentFilename), nil
	}
	return "", ErrNoSupervisor
}

// SupervisorRunning reports whether the supervisor currently runs the
// coordinator. On macOS this is launchctl's view, independent of the
// socket — that independence is what lets daemon status distinguish
// "registered but stopped" from "running but unreachable". A prefix means a
// test never loaded anything, so the answer is false without consulting
// launchd. On Linux and Windows there is no supervisor in this phase.
func SupervisorRunning(prefix string) (bool, error) {
	if runtime.GOOS == "darwin" && prefix == "" {
		return launchdRunning()
	}
	return false, nil
}

// InstallSupervisorOpts directs the coordinator's supervisor registration.
type InstallSupervisorOpts struct {
	// Prefix overrides the registration directory (tests). Empty means the
	// real ~/Library/LaunchAgents on macOS, and ErrNoSupervisor elsewhere.
	Prefix string
	// WtdPath is the absolute path the registration file will exec — the
	// coordinator binary, normally a sibling of the wt binary that is
	// running `wt daemon install`.
	WtdPath string
}

// InstallSupervisorResult reports what registration wrote and whether the
// supervisor was asked to start the coordinator.
type InstallSupervisorResult struct {
	PlistPath string
	Label     string
	Loaded    bool
	Note      string
}

// InstallSupervisor registers the coordinator with the platform's
// supervisor and starts it. The plist deliberately uses RunAtLoad and
// KeepAlive rather than launchd socket activation: launchd hands the
// listener over through launch_activate_socket, a C API, and this project
// is CGO_ENABLED=0 throughout — there is no pure-Go path to that file
// descriptor (docs/ARCHITECTURE.md §4.1 names the Sockets key; the task
// brief overrides it for that reason).
//
// Under a prefix the registration file is written and nothing is loaded —
// the point of the prefix is that a test never touches the machine's
// launchd.
func InstallSupervisor(opts InstallSupervisorOpts) (InstallSupervisorResult, error) {
	path, err := SupervisorRegistrationPath(opts.Prefix)
	if err != nil {
		return InstallSupervisorResult{}, err
	}
	if opts.WtdPath == "" {
		return InstallSupervisorResult{}, errors.New("the registration file needs the coordinator binary path (wtd)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("creating the registration directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, launchdPlist(opts.WtdPath), 0o644); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("writing %s: %w", path, err)
	}
	if opts.Prefix != "" {
		return InstallSupervisorResult{
			PlistPath: path, Label: LaunchAgentLabel, Loaded: false,
			Note: "registration written under a test prefix; no launchd state was touched",
		}, nil
	}
	// Real registration: macOS only — the prefix-less path on Linux and
	// Windows already refused above with ErrNoSupervisor.
	if err := loadLaunchAgent(path); err != nil {
		return InstallSupervisorResult{PlistPath: path, Label: LaunchAgentLabel},
			fmt.Errorf("registering %s with launchd: %w", path, err)
	}
	return InstallSupervisorResult{PlistPath: path, Label: LaunchAgentLabel, Loaded: true}, nil
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
	case "windows":
		return "install the coordinator as a Windows service (phase 8); for now run wtd in the foreground"
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
	case "windows":
		return DaemonFixes{
			NotRegistered:      "install the coordinator as a Windows service (phase 8); for now run wtd in the foreground",
			RegisteredStopped:  "start the coordinator service (phase 8); for now run wtd in the foreground",
			RunningUnreachable: "restart the coordinator (phase 8); for now run wtd in the foreground",
		}
	default:
		return DaemonFixes{
			NotRegistered:      start + " — the Linux supervisor unit is phase 8",
			RegisteredStopped:  start,
			RunningUnreachable: start + " — and check that WT_SOCKET names the socket the coordinator listens on",
		}
	}
}
