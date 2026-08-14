package cli

// uninstall_test.go exercises `wt daemon uninstall`: the prefix rail (a
// test never touches the machine's supervisor), the live-entry refusal
// with its count and fix commands, --force as the only way past it, the
// store being left alone, and the JSON shape. The live-entry tests plant a
// real store — the client's one deliberate read of wt.db — and assert the
// verb refuses against it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// uninstallTestEnv builds a store root with the given number of live
// entries and a planted registration file under a prefix, and returns the
// WT_HOME value, the registration path and the database path. Every
// uninstall test sets WT_HOME so the verb reads this store and never the
// machine's real one.
func uninstallTestEnv(t *testing.T, entries int) (home, regPath, dbPath string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(home)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	for i := 0; i < entries; i++ {
		slug := "wt-" + string(rune('1'+i))
		if err := st.UpsertEntry(store.Entry{
			App: "compose-app", Slug: slug, Slot: i + 1,
			Owner: "me", OwnerKind: "host",
			Path: "/tmp/" + slug, PathVisible: true, State: "active",
			Resources: map[string]spec.Resolved{}, CreatedAt: "2026-01-01T00:00:00Z", LastSeen: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("planting entry %d: %v", i, err)
		}
	}
	prefix := filepath.Join(t.TempDir(), "reg")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	regPath = filepath.Join(prefix, platform.SupervisorFilename(runtime.GOOS))
	if err := os.WriteFile(regPath, []byte("registration"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, regPath, filepath.Join(home, dbFileName)
}

// TestDaemonUninstallPrefixRoundTrip: install under a prefix, then
// uninstall under the same prefix — the registration file comes back off
// the disk and the store directory is left alone. No supervisor is ever
// consulted.
func TestDaemonUninstallPrefixRoundTrip(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_HOME", home)
	prefix := filepath.Join(t.TempDir(), "reg")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", stub)
	if code != ExitOK {
		t.Fatalf("install exit = %d; stderr: %s", code, stderr)
	}
	regPath := filepath.Join(prefix, platform.SupervisorFilename(runtime.GOOS))
	if _, err := os.Stat(regPath); err != nil {
		t.Fatalf("registration was not written: %v", err)
	}

	code, stdout, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix)
	if code != ExitOK {
		t.Fatalf("uninstall exit = %d; stderr: %s", code, stderr)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("registration file still present after uninstall: %v", err)
	}
	if !strings.Contains(stdout, "uninstalled:") {
		t.Errorf("output lacks the uninstalled line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "left alone") {
		t.Errorf("output does not say the store was left alone:\n%s", stdout)
	}
	if !strings.Contains(stdout, home) {
		t.Errorf("output does not name the store path %q:\n%s", home, stdout)
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("the store was removed: %v", err)
	}
}

// TestDaemonUninstallRefusesLiveEntries is the task's refusal: three
// entries still allocated means uninstall refuses with exit 3, names how
// many and the commands that resolve them, and changes nothing.
func TestDaemonUninstallRefusesLiveEntries(t *testing.T) {
	home, regPath, dbPath := uninstallTestEnv(t, 3)
	t.Setenv("WT_HOME", home)
	prefix := filepath.Dir(regPath)

	code, stdout, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix)
	if code != ExitRefused {
		t.Fatalf("exit = %d, want 3; stdout: %s; stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "3 entries are still allocated") {
		t.Errorf("refusal does not name the count:\n%s", stderr)
	}
	if !strings.Contains(stderr, "wt list") || !strings.Contains(stderr, "wt rm") {
		t.Errorf("refusal does not name the resolving commands:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("refusal does not name --force as the override:\n%s", stderr)
	}
	if _, err := os.Stat(regPath); err != nil {
		t.Errorf("the refusal changed the registration: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("the refusal changed the store: %v", err)
	}
}

// TestDaemonUninstallForceOverridesTheRefusal: --force is the only way
// past a live registry, and even then the store is left alone.
func TestDaemonUninstallForceOverridesTheRefusal(t *testing.T) {
	home, regPath, dbPath := uninstallTestEnv(t, 2)
	t.Setenv("WT_HOME", home)
	prefix := filepath.Dir(regPath)

	code, stdout, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix, "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("registration file still present after forced uninstall: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("the store was removed by --force: %v", err)
	}
	if !strings.Contains(stdout, "left alone") {
		t.Errorf("output does not say the store was left alone:\n%s", stdout)
	}
}

// TestDaemonUninstallRefusesUnreadableRegistry: a registry that cannot be
// read is a refusal too — whether entries are live cannot be verified, and
// an uninstall that cannot verify may strand allocations. --force proceeds.
func TestDaemonUninstallRefusesUnreadableRegistry(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, dbFileName)
	if err := os.WriteFile(dbPath, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_HOME", home)
	prefix := filepath.Join(t.TempDir(), "reg")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(prefix, platform.SupervisorFilename(runtime.GOOS))
	if err := os.WriteFile(regPath, []byte("registration"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix)
	if code != ExitRefused {
		t.Fatalf("exit = %d, want 3; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, dbPath) {
		t.Errorf("refusal does not name the unreadable registry %q:\n%s", dbPath, stderr)
	}
	if _, err := os.Stat(regPath); err != nil {
		t.Errorf("the refusal changed the registration: %v", err)
	}

	code, _, _ = runCLI(t, "daemon", "uninstall", "--prefix", prefix, "--force")
	if code != ExitOK {
		t.Fatalf("forced uninstall over an unreadable registry: exit = %d, want 0", code)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("registration file still present after forced uninstall: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("the store was removed: %v", err)
	}
}

// TestDaemonUninstallNothingRegistered: uninstalling something never
// installed succeeds and says so — a second uninstall is a no-op.
func TestDaemonUninstallNothingRegistered(t *testing.T) {
	t.Setenv("WT_HOME", filepath.Join(t.TempDir(), "wt"))
	prefix := filepath.Join(t.TempDir(), "reg")
	code, stdout, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "registration: none") {
		t.Errorf("output does not report nothing was registered:\n%s", stdout)
	}
	if !strings.Contains(stdout, "left alone") {
		t.Errorf("output does not say the store was left alone:\n%s", stdout)
	}
}

// TestDaemonUninstallJSON: --json prints exactly one JSON object with the
// registration facts and the store-kept truth.
func TestDaemonUninstallJSON(t *testing.T) {
	home, regPath, _ := uninstallTestEnv(t, 0)
	t.Setenv("WT_HOME", home)
	prefix := filepath.Dir(regPath)

	code, stdout, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix, "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	var res daemonUninstallResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if !res.Removed || res.Stopped {
		t.Errorf("json removed=%v stopped=%v, want removed only", res.Removed, res.Stopped)
	}
	if res.RegistrationPath != regPath {
		t.Errorf("json registration path = %q, want %q", res.RegistrationPath, regPath)
	}
	if res.StorePath != home || !res.StoreKept {
		t.Errorf("json store = %+v, want path %q and kept", res.StorePath, home)
	}
}

// TestCountRegistryEntries: the count reads exactly the planted rows, and
// a store that does not exist counts zero — a machine nothing was ever
// allocated on has nothing to refuse over.
func TestCountRegistryEntries(t *testing.T) {
	t.Setenv("WT_HOME", filepath.Join(t.TempDir(), "wt"))
	n, err := countRegistryEntries()
	if err != nil {
		t.Fatalf("count without a store: %v", err)
	}
	if n != 0 {
		t.Errorf("count without a store = %d, want 0", n)
	}

	home, _, _ := uninstallTestEnv(t, 2)
	t.Setenv("WT_HOME", home)
	n, err = countRegistryEntries()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

// TestDaemonUninstallUsage: unknown flags and stray arguments are usage
// errors like every verb's.
func TestDaemonUninstallUsage(t *testing.T) {
	code, _, _ := runCLI(t, "daemon", "uninstall", "--bogus")
	if code != ExitUsage {
		t.Errorf("unknown flag exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "daemon", "uninstall", "stray")
	if code != ExitUsage {
		t.Errorf("positional argument exit = %d, want 2", code)
	}
	code, stdout, _ := runCLI(t, "help")
	if !strings.Contains(stdout, "daemon uninstall") {
		t.Errorf("help lacks the uninstall verb:\n%s", stdout)
	}
	_ = code
}
