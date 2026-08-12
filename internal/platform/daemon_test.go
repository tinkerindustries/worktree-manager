package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
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
	if !strings.Contains(file, "com.mrgeoffrich.wtd") {
		t.Errorf("registration file lacks the label:\n%s", file)
	}
	switch runtime.GOOS {
	case "darwin":
		for _, want := range []string{
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
		if !strings.Contains(file, "Exec") {
			t.Errorf("task XML lacks the Exec action:\n%s", file)
		}
	}
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

// TestSystemdEscapeExec pins the ExecStart quoting: a path with a space or
// a dollar must survive systemd's argument splitting.
func TestSystemdEscapeExec(t *testing.T) {
	got := systemdEscapeExec(`/home/a b/wtd$1`)
	if !strings.Contains(got, `"`) {
		t.Errorf("exec path is not quoted: %q", got)
	}
	if strings.Contains(got, " ") && !strings.HasPrefix(got, `"`) {
		t.Errorf("a path with a space must be quoted: %q", got)
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

// TestActivatedListenerRefusesForeignHandoff: LISTEN_FDS naming another
// process, or more than one descriptor, is refused — the handoff is only
// valid for this process and for exactly the one socket the unit owns.
func TestActivatedListenerRefusesForeignHandoff(t *testing.T) {
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_PID", "999999")
	if _, err := ActivatedListener(); err == nil {
		t.Error("a handoff naming another pid succeeded")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	if _, err := ActivatedListener(); err == nil {
		t.Error("a two-descriptor handoff succeeded, want a refusal")
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

// TestEnsurePrivateDir: 0700 and writable, with the refusal naming the path
// when the root cannot be created (02-coordination.md §14: a clear error
// naming the path and the ownership problem, not a rename failure at the
// end of a long operation).
func TestEnsurePrivateDir(t *testing.T) {
	dir := filepath.Join(tempDir(t), "wt-home")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
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
