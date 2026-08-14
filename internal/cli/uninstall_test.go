package cli

// uninstall_test.go exercises `wt daemon uninstall`: the prefix rail (a
// test never touches the machine's supervisor), the live-entry refusal
// with its count and fix commands, --force as the only way past it, the
// store being left alone, and the JSON shape. The entry count comes from
// the coordinator, so the live-entry tests stand up a fake one serving
// `list`: the client never reads wt.db, and a test that seeded a real
// database would be asserting a boundary violation rather than the
// behaviour.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// uninstallTestEnv stands up a fake coordinator reporting the given number
// of live entries, plus a planted registration file under a prefix, and
// returns the WT_HOME value and the registration path. Every
// uninstall test sets WT_HOME so the verb reads this store and never the
// machine's real one.
func uninstallTestEnv(t *testing.T, entries int) (home, regPath string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// The entry count comes from the coordinator, not from wt.db: the
	// client never reads the store. A fake coordinator serving `list` is
	// therefore what stands in for a machine with allocations on it.
	rows := make([]api.ListEntry, entries)
	for i := range rows {
		rows[i] = api.ListEntry{
			App: "compose-app", Slug: "wt-" + strconv.Itoa(i+1), Slot: i + 1, State: "active",
		}
	}
	endpoint := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		api.VerbList: canned(mustResult(t, api.ListResult{Entries: rows})),
	})
	t.Setenv("WT_ENDPOINT", endpoint)

	prefix := filepath.Join(t.TempDir(), "reg")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	regPath = filepath.Join(prefix, platform.SupervisorFilename(runtime.GOOS))
	if err := os.WriteFile(regPath, []byte("registration"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, regPath
}

// emptyCoordinator is a fake reporting no allocations, for the tests whose
// subject is not the refusal. Without one they fall through to the
// compiled-in default endpoint and pass or fail depending on whether a real
// coordinator happens to be running on the machine.
func emptyCoordinator(t *testing.T) string {
	t.Helper()
	return fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		api.VerbList: canned(mustResult(t, api.ListResult{Entries: []api.ListEntry{}})),
	})
}

// mustResult wraps a verb result as the response the fake returns.
func mustResult(t *testing.T, v any) *api.Response {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding the canned result: %v", err)
	}
	return &api.Response{Result: raw}
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
	t.Setenv("WT_ENDPOINT", emptyCoordinator(t))
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
	home, regPath := uninstallTestEnv(t, 3)
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
}

// TestDaemonUninstallForceOverridesTheRefusal: --force is the only way
// past a live registry, and even then the store is left alone.
func TestDaemonUninstallForceOverridesTheRefusal(t *testing.T) {
	home, regPath := uninstallTestEnv(t, 2)
	t.Setenv("WT_HOME", home)
	prefix := filepath.Dir(regPath)

	code, stdout, stderr := runCLI(t, "daemon", "uninstall", "--prefix", prefix, "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if _, err := os.Stat(regPath); !os.IsNotExist(err) {
		t.Errorf("registration file still present after forced uninstall: %v", err)
	}
	if !strings.Contains(stdout, "left alone") {
		t.Errorf("output does not say the store was left alone:\n%s", stdout)
	}
}

// TestDaemonUninstallRefusesWhenTheCoordinatorIsUnreachable is the
// deliberate consequence of counting entries by asking the coordinator
// rather than by reading wt.db: with the coordinator down the client
// cannot know what is allocated, so it refuses instead of guessing. An
// uninstall that cannot verify is an uninstall that may strand
// allocations. --force is the documented way past it.
func TestDaemonUninstallRefusesWhenTheCoordinatorIsUnreachable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_HOME", home)
	// A port nothing is listening on: the client's dial fails, which is
	// what a stopped or missing coordinator looks like.
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
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
	if !strings.Contains(stderr, "--force") {
		t.Errorf("refusal does not name --force as the way past it:\n%s", stderr)
	}
	if !strings.Contains(stderr, "still allocated") {
		t.Errorf("refusal does not say what could not be determined:\n%s", stderr)
	}
	if _, err := os.Stat(regPath); err != nil {
		t.Errorf("the refusal changed the registration: %v", err)
	}

	code, _, _ = runCLI(t, "daemon", "uninstall", "--prefix", prefix, "--force")
	if code != ExitOK {
		t.Fatalf("forced uninstall with the coordinator unreachable: exit = %d, want 0", code)
	}
}

// TestDaemonUninstallNothingRegistered: uninstalling something never
// installed succeeds and says so — a second uninstall is a no-op.
func TestDaemonUninstallNothingRegistered(t *testing.T) {
	t.Setenv("WT_HOME", filepath.Join(t.TempDir(), "wt"))
	t.Setenv("WT_ENDPOINT", emptyCoordinator(t))
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
	home, regPath := uninstallTestEnv(t, 0)
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

// TestCountRegistryEntries: the count is whatever the coordinator's list
// reports, and an unreachable coordinator is an error rather than a zero —
// counting zero when the truth is unknown is exactly the mistake the
// refusal exists to prevent.
func TestCountRegistryEntries(t *testing.T) {
	home, _ := uninstallTestEnv(t, 2)
	t.Setenv("WT_HOME", home)
	n, err := countRegistryEntries()
	if err != nil {
		t.Fatalf("count: %+v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}

	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	if _, err := countRegistryEntries(); err == nil {
		t.Error("an unreachable coordinator counted successfully; it must be an error, never a zero")
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
