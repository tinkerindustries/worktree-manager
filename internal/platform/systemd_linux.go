//go:build linux

package platform

// systemd_linux.go is the Linux half of the supervisor seam (phase 8b,
// plan.md §5 "Phase 8"): the systemd user unit and its paired .socket unit,
// installed under the user's own systemd directory, registered and started
// by `wt daemon install` exactly as the launchd agent already works on
// macOS (docs/ARCHITECTURE.md §4.1's hosting table: Linux starts the
// coordinator from a paired .socket unit).
//
// Socket activation is used, and the choice is deliberate. macOS refuses
// launchd socket activation because launchd hands the listener over
// through launch_activate_socket, a C API, and this project is
// CGO_ENABLED=0 throughout. systemd passes file descriptors through the
// LISTEN_FDS/LISTEN_PID environment variables, which pure Go reads, so
// the design of record's "paired .socket unit" is implementable here
// without that obstacle. The .socket unit owns the listener — SocketMode
// 0700 makes the socket's permission model declarative — and the service
// consumes the descriptor with --activate (ActivatedListener). The
// socket survives a coordinator crash, so clients' connections queue in
// the kernel while systemd restarts wtd instead of hitting a stale-file
// race; wtd stays resident once activated (the sweeper timers need a
// resident process), so on-demand start is a lazy first start, not a
// stop-when-idle regime.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// systemdUserDir resolves the directory the user's own units live in:
// ~/.config/systemd/user (XDG_CONFIG_HOME honoured), or the test prefix.
func systemdUserDir(prefix string) (string, error) {
	if prefix != "" {
		return prefix, nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the systemd user directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// systemctl runs one systemctl --user invocation.
func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// systemdServiceUnit is the service unit. ExecStart passes --activate so
// wtd consumes the listener the .socket unit owns (LISTEN_FDS), and
// Restart=always is the systemd analogue of launchd's KeepAlive. The
// Requires/After pair ties the service to its socket unit, so the socket
// exists whenever the service starts — including the login-time start.
// With the opt-in loopback TCP surface configured, ExecStart also carries
// --tcp and --tcp-token; each argument is quoted under systemd's rules, and
// a unit that carries the token is written 0600 (systemd accepts it, and a
// machine's other users cannot read the token out of the unit file — the
// security pass, phase 9).
func systemdServiceUnit(wtdPath, tcpAddr, tcpToken string) []byte {
	exec := systemdEscapeExec(wtdPath) + " --activate"
	if tcpAddr != "" {
		exec += " --tcp " + systemdEscapeExec(tcpAddr) + " --tcp-token " + systemdEscapeExec(tcpToken)
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=Worktree Manager coordinator (wtd)
Requires=%s
After=%s

[Service]
ExecStart=%s
Restart=always

[Install]
WantedBy=default.target
`, SystemdSocketFilename, SystemdSocketFilename, exec))
}

// systemdSocketUnit is the paired .socket unit. ListenStream uses %t, the
// systemd specifier for the user runtime directory — the same
// $XDG_RUNTIME_DIR/wt/sock the platform's default socket path resolves to
// — so the unit and the binaries cannot drift apart. SocketMode 0700 makes
// the socket's restriction to the owning user declarative
// (docs/ARCHITECTURE.md §12.2).
func systemdSocketUnit() []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=Worktree Manager coordinator socket

[Socket]
ListenStream=%%t/wt/sock
SocketMode=0700

[Install]
WantedBy=sockets.target
`))
}

// systemdEscapeExec quotes one ExecStart argument under systemd's quoting
// rules: double quotes with \ " $ % escaped. A path with a space (a home
// directory may have one) would otherwise be split into two arguments.
func systemdEscapeExec(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(p) + `"`
}

// installSystemdUnits writes the service and socket units and, outside a
// test prefix, registers and starts them: daemon-reload, then
// enable --now on both units — enable for the login start (WantedBy on
// both), --now for the immediate start that mirrors launchctl kickstart.
// The service start consumes the socket unit's descriptor, so the
// coordinator is listening as soon as the units are up.
func installSystemdUnits(prefix, wtdPath, tcpAddr, tcpToken string) (InstallSupervisorResult, error) {
	dir, err := systemdUserDir(prefix)
	if err != nil {
		return InstallSupervisorResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("creating the systemd user directory %s: %w", dir, err)
	}
	svc := filepath.Join(dir, SystemdServiceFilename)
	sock := filepath.Join(dir, SystemdSocketFilename)
	mode := os.FileMode(0o644)
	if tcpToken != "" {
		// The service unit carries the TCP token: 0600, so a machine's
		// other users cannot read it out of the unit file and connect over
		// the loopback surface (the security pass, phase 9). systemd
		// accepts non-world-readable unit files.
		mode = 0o600
	}
	if err := os.WriteFile(svc, systemdServiceUnit(wtdPath, tcpAddr, tcpToken), mode); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("writing %s: %w", svc, err)
	}
	if err := os.WriteFile(sock, systemdSocketUnit(), 0o644); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("writing %s: %w", sock, err)
	}
	if prefix != "" {
		note := "registration written under a test prefix (" + svc + " and its paired socket unit " + sock + "); no systemd state was touched"
		if tcpToken != "" {
			note += "; the service unit carries the loopback TCP token"
		}
		return InstallSupervisorResult{
			RegistrationPath: svc, Label: SystemdUnitLabel, Loaded: false,
			Note: note,
		}, nil
	}
	if err := systemctl("daemon-reload"); err != nil {
		return InstallSupervisorResult{RegistrationPath: svc, Label: SystemdUnitLabel},
			fmt.Errorf("reloading the systemd user manager: %w", err)
	}
	if err := systemctl("enable", "--now", SystemdServiceFilename, SystemdSocketFilename); err != nil {
		return InstallSupervisorResult{RegistrationPath: svc, Label: SystemdUnitLabel},
			fmt.Errorf("enabling and starting the coordinator's systemd units: %w", err)
	}
	return InstallSupervisorResult{
		RegistrationPath: svc, Label: SystemdUnitLabel, Loaded: true,
		Note: lingeringCaveatText(),
	}, nil
}

// systemdRunning asks systemd whether the socket unit is active — the
// supervisor fact `daemon status` needs in order to distinguish
// "registered but stopped" from "running but unreachable". The socket
// unit is the right unit to ask: while it is active, a connection is
// served (it activates the service on demand), which is what "the
// coordinator is running" means under socket activation. A test prefix
// never loaded anything, so the answer is false without consulting
// systemd; any failure to get an answer (systemctl missing, the unit
// inactive) also reports false — the launchd precedent, and safe: the
// state machine's fixes are harmless under a misreport.
func systemdRunning(prefix string) (bool, error) {
	if prefix != "" {
		return false, nil
	}
	return systemctl("is-active", "--quiet", SystemdSocketFilename) == nil, nil
}

// systemdLinger reports whether user lingering is enabled for the
// account. The lingering caveat is the point of the Linux section: a
// systemd user unit stops at logout unless lingering is enabled, and a
// coordinator that dies at logout is a coordinator nobody notices until
// the next login.
func systemdLinger() (bool, error) {
	name := os.Getenv("USER")
	if name == "" {
		u, err := user.Current()
		if err != nil {
			return false, err
		}
		name = u.Username
	}
	out, err := exec.Command("loginctl", "show-user", name, "--property=Linger", "--value").Output()
	if err != nil {
		return false, fmt.Errorf("loginctl show-user %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// lingeringCaveatText is the caveat text itself, naming the remedy.
func lingeringCaveatText() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	return fmt.Sprintf("a systemd user unit stops at logout unless lingering is enabled for the account — enable it with: loginctl enable-linger %s", name)
}

// lingeringCaveat reports the lingering caveat a reader of `daemon
// status` or `daemon install` needs on Linux: empty where it does not
// apply (test prefixes), the caveat naming the `loginctl enable-linger
// <user>` remedy when lingering is not confirmed. Where the probe cannot
// run at all (no loginctl — a container, say) the caveat is still
// reported, because an unverifiable lingering state is the same risk as
// a disabled one.
func lingeringCaveat(prefix string) string {
	if prefix != "" {
		return ""
	}
	enabled, err := systemdLinger()
	if err == nil && enabled {
		return ""
	}
	return lingeringCaveatText()
}

// ActivatedListener returns the listener systemd handed over through
// LISTEN_FDS/LISTEN_PID — the socket-activation handoff the .socket unit
// arranges. It returns nil, nil when this process was not activated (no
// LISTEN_FDS), so the foreground path is untouched. The fd is consumed
// and the variables unset, so a child process cannot inherit a listening
// socket it does not own.
func ActivatedListener() (net.Listener, error) {
	fds := os.Getenv("LISTEN_FDS")
	if fds == "" || fds == "0" {
		return nil, nil
	}
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, errors.New("LISTEN_FDS is set but LISTEN_PID does not name this process; refusing to consume another process's activated socket")
	}
	if fds != "1" {
		return nil, fmt.Errorf("socket activation handed over %s file descriptors, want exactly 1", fds)
	}
	f := os.NewFile(3, "systemd-activated-socket")
	if f == nil {
		return nil, errors.New("socket activation: file descriptor 3 is not open")
	}
	ln, err := net.FileListener(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("socket activation: wrapping descriptor 3: %w", err)
	}
	os.Unsetenv("LISTEN_FDS")
	os.Unsetenv("LISTEN_PID")
	return ln, nil
}
