package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// TestSupervisorRegistrationPathPrefix: under a prefix the registration file
// is inert data on every platform — this is the seam that lets the daemon
// tests exercise registration and status without ever touching the machine's
// supervisor (no test may install a real supervisor unit on the machine
// running it). The filename is the platform's own, so a prefixed install
// plants the file a prefixed status check looks for.
func TestSupervisorRegistrationPathPrefix(t *testing.T) {
	prefix := tempDir(t)
	got, err := SupervisorRegistrationPath(prefix)
	if err != nil {
		t.Fatalf("SupervisorRegistrationPath(prefix): %v", err)
	}
	if want := filepath.Join(prefix, SupervisorFilename(runtime.GOOS)); got != want {
		t.Errorf("SupervisorRegistrationPath = %q, want %q", got, want)
	}
}

// TestSupervisorRegistrationPathReal: without a prefix the real path is the
// platform's supervisor location: macOS's LaunchAgents directory, Linux's
// systemd user directory (the service unit of the pair), Windows's task
// XML directory. A platform with no supervisor refuses.
func TestSupervisorRegistrationPathReal(t *testing.T) {
	got, err := SupervisorRegistrationPath("")
	if err == ErrNoSupervisor {
		return // untargeted platform: the refusal is the contract
	}
	if err != nil {
		t.Fatalf("SupervisorRegistrationPath: %v", err)
	}
	switch runtime.GOOS {
	case "darwin":
		if !strings.HasSuffix(got, filepath.Join("Library", "LaunchAgents", LaunchAgentFilename)) {
			t.Errorf("real registration path = %q, want ~/Library/LaunchAgents/<label>.plist", got)
		}
	case "linux":
		if !strings.HasSuffix(got, filepath.Join("systemd", "user", SystemdServiceFilename)) {
			t.Errorf("real registration path = %q, want ~/.config/systemd/user/<label>.service", got)
		}
	case "windows":
		if !strings.HasSuffix(got, WindowsTaskFilename) {
			t.Errorf("real registration path = %q, want the task XML under %%LOCALAPPDATA%%", got)
		}
	}
}

// TestInstallSupervisorPrefix writes the registration file(s) under a prefix
// and verifies their content. On macOS the launchd plist carries RunAtLoad
// and KeepAlive and no Sockets key — launchd socket activation needs
// launch_activate_socket, a C API, and this project is CGO_ENABLED=0
// throughout. On Linux the systemd service unit carries ExecStart with
// --activate and Restart=always, and the paired .socket unit owns the
// listener (SocketMode 0700). On Windows the task XML is written (the
// schtasks registration itself is never run under a prefix).
func TestInstallSupervisorPrefix(t *testing.T) {
	prefix := tempDir(t)
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := InstallSupervisor(InstallSupervisorOpts{Prefix: prefix, WtdPath: wtd})
	if err != nil {
		t.Fatalf("InstallSupervisor: %v", err)
	}
	if res.Loaded {
		t.Error("install under a prefix reported loaded; a prefix must never touch the supervisor")
	}
	if res.RegistrationPath != filepath.Join(prefix, SupervisorFilename(runtime.GOOS)) {
		t.Errorf("registration path = %q", res.RegistrationPath)
	}
	data, err := os.ReadFile(res.RegistrationPath)
	if err != nil {
		t.Fatal(err)
	}
	file := string(data)
	// The label is in the file on launchd (Label) and systemd (the unit
	// names its socket), and is asserted in those branches. On Windows it
	// is not in the file at all: the task's name is the /TN argument
	// schtasks is given, and the XML holds the action alone — which is
	// also why the file is UTF-16 and cannot be searched as a Go string.
	switch runtime.GOOS {
	case "darwin":
		for _, want := range []string{
			LaunchAgentLabel,
			"<key>Label</key>",
			"<string>" + LaunchAgentLabel + "</string>",
			"<key>ProgramArguments</key>",
			"<string>" + wtd + "</string>",
			"<key>RunAtLoad</key>",
			"<true/>",
			"<key>KeepAlive</key>",
		} {
			if !strings.Contains(file, want) {
				t.Errorf("plist lacks %s:\n%s", want, file)
			}
		}
		if strings.Contains(file, "Sockets") {
			t.Errorf("plist carries a Sockets key; launchd socket activation is C-API-only and this project is CGO_ENABLED=0:\n%s", file)
		}
	case "linux":
		for _, want := range []string{
			"[Unit]",
			LaunchAgentLabel,
			"Requires=" + SystemdSocketFilename,
			"After=" + SystemdSocketFilename,
			`ExecStart="` + wtd + `" --activate`,
			"Restart=always",
			"WantedBy=default.target",
		} {
			if !strings.Contains(file, want) {
				t.Errorf("service unit lacks %s:\n%s", want, file)
			}
		}
		sockData, err := os.ReadFile(filepath.Join(prefix, SystemdSocketFilename))
		if err != nil {
			t.Fatalf("the paired socket unit was not written: %v", err)
		}
		sock := string(sockData)
		for _, want := range []string{
			"ListenStream=%t/wt/sock",
			"SocketMode=0700",
			"WantedBy=sockets.target",
		} {
			if !strings.Contains(sock, want) {
				t.Errorf("socket unit lacks %s:\n%s", want, sock)
			}
		}
	case "windows":
		// The task XML is written UTF-16 (schtasks /Create /XML requires
		// it), so decode before asserting.
		task := decodeUTF16LE(t, data)
		for _, want := range []string{
			"<LogonTrigger>",
			"<Command>" + wtd + "</Command>",
			"<RestartOnFailure>",
			"<LogonType>InteractiveToken</LogonType>",
		} {
			if !strings.Contains(task, want) {
				t.Errorf("task XML lacks %s:\n%s", want, task)
			}
		}
	}
}

// decodeUTF16LE decodes a UTF-16LE byte slice (BOM stripped) for the
// windows branch of the shared tests.
func decodeUTF16LE(t *testing.T, data []byte) string {
	t.Helper()
	if len(data)%2 != 0 {
		t.Fatalf("UTF-16 data has an odd length: %d", len(data))
	}
	u := make([]uint16, len(data)/2)
	for i := range u {
		u[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	if len(u) > 0 && u[0] == 0xFEFF {
		u = u[1:]
	}
	return string(utf16.Decode(u))
}

// TestInstallSupervisorRealRegistrationNeverRunsInTests: a real
// (prefix-less) install registers with the machine's supervisor, which no
// test may do on any platform — darwin (launchctl bootstrap), linux
// (systemctl enable), windows (schtasks /Create). The test exists so a
// future platform that forgets the rule fails loudly here instead of in
// review.
func TestInstallSupervisorRealRegistrationNeverRunsInTests(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		t.Skipf("a prefix-less install on %s is the real supervisor; tests only use prefixes", runtime.GOOS)
	}
	if _, err := InstallSupervisor(InstallSupervisorOpts{WtdPath: "/nonexistent/wtd"}); err == nil {
		t.Fatal("InstallSupervisor without a prefix succeeded on this platform")
	}
}

// TestInstallSupervisorNeedsWtdPath: the registration file execs the
// coordinator binary, so an install without it refuses.
func TestInstallSupervisorNeedsWtdPath(t *testing.T) {
	prefix := tempDir(t)
	if _, err := InstallSupervisor(InstallSupervisorOpts{Prefix: prefix}); err == nil {
		t.Fatal("InstallSupervisor without a wtd path succeeded")
	}
}

// TestUninstallSupervisorPrefixRemovesRegistration: install under a prefix,
// then uninstall under the same prefix — the registration file(s) come
// back off the disk and nothing is reported as stopped (a test prefix
// never loaded anything). On Linux the paired socket unit must go too.
func TestUninstallSupervisorPrefixRemovesRegistration(t *testing.T) {
	prefix := tempDir(t)
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSupervisor(InstallSupervisorOpts{Prefix: prefix, WtdPath: wtd}); err != nil {
		t.Fatalf("install: %v", err)
	}
	regPath := filepath.Join(prefix, SupervisorFilename(runtime.GOOS))
	if _, err := os.Stat(regPath); err != nil {
		t.Fatalf("registration was not written: %v", err)
	}
	res, err := UninstallSupervisor(prefix)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !res.Removed {
		t.Error("uninstall reported nothing removed; the registration file existed")
	}
	if res.Stopped {
		t.Error("a prefixed uninstall reported stopped; a test prefix must never touch the supervisor")
	}
	if _, err := os.Stat(regPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("registration file still present after uninstall: %v", err)
	}
	if runtime.GOOS == "linux" {
		sockPath := filepath.Join(prefix, SystemdSocketFilename)
		if _, err := os.Stat(sockPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the paired socket unit still present after uninstall: %v", err)
		}
	}
}

// TestUninstallSupervisorPrefixNothingRegistered: uninstalling something
// never installed succeeds and reports nothing was removed — a second
// uninstall is a no-op, not an error.
func TestUninstallSupervisorPrefixNothingRegistered(t *testing.T) {
	res, err := UninstallSupervisor(tempDir(t))
	if err != nil {
		t.Fatalf("uninstall of nothing: %v", err)
	}
	if res.Removed || res.Stopped {
		t.Errorf("uninstall of nothing reported removed=%v stopped=%v", res.Removed, res.Stopped)
	}
}

// TestUninstallSupervisorRealNeverRunsInTests: a real (prefix-less)
// uninstall stops the machine's supervisor registration, which no test may
// do on any platform — darwin (launchctl bootout), linux (systemctl
// disable), windows (schtasks /Delete). The test exists so a future
// platform that forgets the rule fails loudly here instead of in review.
func TestUninstallSupervisorRealNeverRunsInTests(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		t.Skipf("a prefix-less uninstall on %s is the real supervisor; tests only use prefixes", runtime.GOOS)
	}
	if _, err := UninstallSupervisor(""); err == nil {
		t.Fatal("UninstallSupervisor without a prefix succeeded on this platform")
	}
}

// TestLingeringCaveatUnderPrefix: a test prefix never probes loginctl —
// the caveat is empty so CI (a container without systemd) does not see it.
func TestLingeringCaveatUnderPrefix(t *testing.T) {
	if got := LingeringCaveat(tempDir(t)); got != "" {
		t.Errorf("LingeringCaveat under a prefix = %q, want empty", got)
	}
}

// TestActivatedListenerNotActivated: without LISTEN_FDS the process was not
// socket-activated, and ActivatedListener reports that (nil, nil) — the
// foreground path.
func TestActivatedListenerNotActivated(t *testing.T) {
	t.Setenv("LISTEN_FDS", "")
	t.Setenv("LISTEN_PID", "")
	ln, err := ActivatedListener()
	if err != nil {
		t.Fatalf("ActivatedListener: %v", err)
	}
	if ln != nil {
		t.Error("ActivatedListener returned a listener with no LISTEN_FDS")
	}
}

func TestCoordinatorStartCommandNamesACommand(t *testing.T) {
	cmd := CoordinatorStartCommand("/tmp/wt.sock")
	if cmd == "" {
		t.Fatal("the start command is empty; the exit-5 remedy must name a command")
	}
}

// TestDaemonFixesDiffer is exit criterion 1's core: the three broken states
// name a different fix each, so a reader is never offered the same remedy
// twice for two different situations.
func TestDaemonFixesDiffer(t *testing.T) {
	f := DaemonFixesFor("/tmp/wt.sock")
	fixes := []string{f.NotRegistered, f.RegisteredStopped, f.RunningUnreachable}
	for i, a := range fixes {
		if a == "" {
			t.Errorf("fix %d is empty", i)
		}
		for j, b := range fixes {
			if i != j && a == b {
				t.Errorf("fix %d and %d are identical: %q", i, j, a)
			}
		}
	}
}

func TestSupervisorRunningUnderPrefixIsFalse(t *testing.T) {
	running, err := SupervisorRunning(tempDir(t))
	if err != nil {
		t.Fatalf("SupervisorRunning(prefix): %v", err)
	}
	if running {
		t.Error("a prefixed registration reports running; a test prefix never loads anything")
	}
}

// TestEnsurePrivateDir: 0700 and writable on unix (on Windows the ACL is
// the permission model and is asserted in TestEnsurePrivateDirWindowsACL),
// with the refusal naming the path when the root cannot be created
// (02-coordination.md §14: a clear error naming the path and the
// ownership problem, not a rename failure at the end of a long
// operation).
func TestEnsurePrivateDir(t *testing.T) {
	dir := filepath.Join(tempDir(t), "wt-home")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("store dir mode = %v, want 0700", fi.Mode().Perm())
	}
	// A file where a directory must be cannot become a store root; the error
	// must name the path.
	blocker := filepath.Join(tempDir(t), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "wt-home")
	err = EnsurePrivateDir(bad)
	if err == nil {
		t.Fatalf("EnsurePrivateDir(%s) succeeded over a file", bad)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("refusal does not name the path %q: %v", bad, err)
	}
}

// TestLaunchdPlistStructure covers launchdPlist, which had no test at all.
// It pins the keys the agent depends on and the argument-per-element
// shape: each flag value is its own <string>, which is why a container
// token needs no escaping beyond the XML text rules.
func TestLaunchdPlistStructure(t *testing.T) {
	got := string(launchdPlist("/opt/wt/bin/wtd", "127.0.0.1:7833", "", nil))

	for _, want := range []string{
		"<key>Label</key>",
		"<string>" + LaunchAgentLabel + "</string>",
		"<key>ProgramArguments</key>",
		"<string>/opt/wt/bin/wtd</string>",
		"<string>--addr</string>",
		"<string>127.0.0.1:7833</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist does not contain %q:\n%s", want, got)
		}
	}
	// An empty token must not be emitted: an empty --container-token
	// admits no container while looking like it does.
	if strings.Contains(got, "--container-token") {
		t.Errorf("plist carries --container-token with no token set:\n%s", got)
	}
}

// TestLaunchdPlistEscapesArguments proves the XML text rules are applied
// to every argument, so a token or an allowed host carrying & or < cannot
// produce a plist launchd refuses to parse.
func TestLaunchdPlistEscapesArguments(t *testing.T) {
	got := string(launchdPlist("/opt/wt/bin/wtd", "", "a&b<c", []string{"h>st"}))

	for _, want := range []string{
		"<string>a&amp;b&lt;c</string>",
		"<string>--allow-host</string>",
		"<string>h&gt;st</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--addr") {
		t.Errorf("plist carries --addr with no address set:\n%s", got)
	}
}

// TestLaunchdPlistSetsNoEnvironment records the deliberate absence of an
// EnvironmentVariables key. The docker-on-PATH bug was fixed by resolving
// helper binaries at call time (platform.LookHelper), not by baking a PATH
// into the agent — a plist PATH would be a second source of truth, fixed
// at install time, that the call-time list would drift from. If this test
// is changed, change helper.go's rationale with it.
func TestLaunchdPlistSetsNoEnvironment(t *testing.T) {
	got := string(launchdPlist("/opt/wt/bin/wtd", "", "", nil))
	if strings.Contains(got, "EnvironmentVariables") {
		t.Errorf("plist sets EnvironmentVariables; helper resolution is LookHelper's job:\n%s", got)
	}
}
