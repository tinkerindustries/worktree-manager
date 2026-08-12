package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSupervisorRegistrationPathPrefix: under a prefix the registration file
// is inert data on every platform — this is the seam that lets the daemon
// tests exercise registration and status without ever touching the machine's
// launchd (no test may install a LaunchAgent on the machine running it).
func TestSupervisorRegistrationPathPrefix(t *testing.T) {
	prefix := tempDir(t)
	got, err := SupervisorRegistrationPath(prefix)
	if err != nil {
		t.Fatalf("SupervisorRegistrationPath(prefix): %v", err)
	}
	if want := filepath.Join(prefix, LaunchAgentFilename); got != want {
		t.Errorf("SupervisorRegistrationPath = %q, want %q", got, want)
	}
}

// TestSupervisorRegistrationPathReal: without a prefix the real path is
// macOS's LaunchAgents directory and a refusal naming phase 8 elsewhere —
// Linux and Windows have no supervisor in this phase.
func TestSupervisorRegistrationPathReal(t *testing.T) {
	got, err := SupervisorRegistrationPath("")
	if err == ErrNoSupervisor {
		return // linux/windows: phase 8 owns the unit; the refusal is the contract
	}
	if err != nil {
		t.Fatalf("SupervisorRegistrationPath: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("Library", "LaunchAgents", LaunchAgentFilename)) {
		t.Errorf("real registration path = %q, want ~/Library/LaunchAgents/<label>.plist", got)
	}
}

// TestInstallSupervisorPrefix writes the LaunchAgent plist under a prefix and
// verifies its content: RunAtLoad and KeepAlive, no Sockets key — launchd
// socket activation needs launch_activate_socket, a C API, and this project
// is CGO_ENABLED=0 throughout.
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
		t.Error("install under a prefix reported loaded; a prefix must never touch launchd")
	}
	if res.PlistPath != filepath.Join(prefix, LaunchAgentFilename) {
		t.Errorf("plist path = %q", res.PlistPath)
	}
	data, err := os.ReadFile(res.PlistPath)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(data)
	for _, want := range []string{
		"<key>Label</key>",
		"<string>" + LaunchAgentLabel + "</string>",
		"<key>ProgramArguments</key>",
		"<string>" + wtd + "</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %s:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "Sockets") {
		t.Errorf("plist carries a Sockets key; launchd socket activation is C-API-only and this project is CGO_ENABLED=0:\n%s", plist)
	}
}

// TestInstallSupervisorNoSupervisorPlatform: on Linux and Windows a real
// (prefix-less) registration is phase 8 and must refuse, naming the
// foreground alternative. Skipped on darwin: there the prefix-less path is
// the real launchd, and no test may install a LaunchAgent on the machine
// running it.
func TestInstallSupervisorNoSupervisorPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin's prefix-less install is the real launchd; tests only use prefixes")
	}
	if _, err := InstallSupervisor(InstallSupervisorOpts{WtdPath: "/nonexistent/wtd"}); err == nil {
		t.Fatal("InstallSupervisor without a prefix succeeded on this platform")
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
