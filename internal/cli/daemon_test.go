package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// shortSock is a socket path short enough to bind. sun_path holds 104 bytes
// on macOS including the terminator, and t.TempDir() spends about 56 of them
// on the per-user temp root under /var/folders before the test's own name is
// added — so a test with a long name fails to bind while a shorter one in the
// same package succeeds. The directory therefore comes from /tmp, which
// resolves to /private/tmp and costs 12.
func shortSock(t *testing.T, name string) string {
	t.Helper()
	root := "/tmp"
	if runtime.GOOS == "windows" {
		root = t.TempDir()
	}
	dir, err := os.MkdirTemp(root, "wt")
	if err != nil {
		t.Fatalf("creating a short temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	return filepath.Join(resolved, name)
}

// fakeVersionServer answers GET /version with the canned range — a
// stand-in for the coordinator the reachability probe and the dial helper
// talk to, without importing coordinator code into the client's tests.
// The dial's version check is the whole of the hello's replacement, so
// the fake needs nothing else.
func fakeVersionServer(t *testing.T, info api.VersionInfo) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+api.VersionPath, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(info)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

var errDial = errors.New("dial failed")

// TestDaemonStatusThreeStates is exit criterion 1: `wt daemon status`
// distinguishes not-registered, registered-but-stopped and
// running-but-unreachable, and names a different fix for each. The state
// machine runs here with injected observations, so all four states are
// reachable on every platform — the supervisor that would produce states
// two and three (launchd) exists only on macOS.
func TestDaemonStatusThreeStates(t *testing.T) {
	fixes := platform.DaemonFixesFor("/tmp/wt.sock")

	res := daemonStatusOf(false, false, errDial, fixes)
	if res.State != "not_registered" || res.Fix != fixes.NotRegistered {
		t.Errorf("not registered = %+v", res)
	}
	res = daemonStatusOf(true, false, errDial, fixes)
	if res.State != "registered_stopped" || res.Fix != fixes.RegisteredStopped {
		t.Errorf("registered stopped = %+v", res)
	}
	res = daemonStatusOf(true, true, errDial, fixes)
	if res.State != "running_unreachable" || res.Fix != fixes.RunningUnreachable {
		t.Errorf("running unreachable = %+v", res)
	}
	// A socket that answers is a working coordinator whatever the
	// supervisor says — a foreground coordinator on a platform with no
	// supervisor reports running, which is the truth.
	res = daemonStatusOf(false, false, nil, fixes)
	if res.State != "running" || res.Fix != "" || !res.Reachable {
		t.Errorf("running = %+v", res)
	}

	// The three broken states carry three different fixes.
	seen := map[string]bool{}
	for _, fix := range []string{fixes.NotRegistered, fixes.RegisteredStopped, fixes.RunningUnreachable} {
		if fix == "" {
			t.Error("a broken state has an empty fix")
		}
		if seen[fix] {
			t.Errorf("two broken states share the fix %q", fix)
		}
		seen[fix] = true
	}
}

// TestDaemonStatusViaRun drives all four states through the real verb — the
// same flag parsing, output shape and JSON object the binary produces —
// with the platform observations injected.
func TestDaemonStatusViaRun(t *testing.T) {
	orig := daemonDeps
	defer func() { daemonDeps = orig }()

	cases := []struct {
		name                 string
		registered, running  bool
		dialErr              error
		wantState, wantHuman string
		wantFix              bool
	}{
		{"not registered", false, false, errDial, "not_registered", "state: not registered", true},
		{"registered stopped", true, false, errDial, "registered_stopped", "state: registered but stopped", true},
		{"running unreachable", true, true, errDial, "running_unreachable", "state: running but unreachable", true},
		{"running", true, true, nil, "running", "state: running", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemonDeps = daemonStatusDeps{
				registered: func(string) (bool, error) { return tc.registered, nil },
				running:    func(string) (bool, error) { return tc.running, nil },
				reachable:  func() error { return tc.dialErr },
			}
			code, stdout, stderr := runCLI(t, "daemon", "status", "--json")
			if code != ExitOK {
				t.Fatalf("exit = %d, want 0 (status reports, it never fails); stderr: %s", code, stderr)
			}
			var res daemonStatusResult
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
				t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
			}
			if res.State != tc.wantState {
				t.Errorf("json state = %q, want %q", res.State, tc.wantState)
			}
			if (res.Fix != "") != tc.wantFix {
				t.Errorf("json fix = %q, want fix present=%v", res.Fix, tc.wantFix)
			}

			code, stdout, _ = runCLI(t, "daemon", "status")
			if code != ExitOK {
				t.Fatalf("human exit = %d, want 0", code)
			}
			if !strings.Contains(stdout, tc.wantHuman) {
				t.Errorf("human output lacks %q:\n%s", tc.wantHuman, stdout)
			}
		})
	}
}

// TestDaemonStatusEndToEndNotRegistered: real observations, a dead socket
// and an empty prefix — the state a machine with no coordinator and no
// registration reports, on every platform.
func TestDaemonStatusEndToEndNotRegistered(t *testing.T) {
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	prefix := shortSock(t, "p")
	code, stdout, stderr := runCLI(t, "daemon", "status", "--json", "--prefix", prefix)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	var res daemonStatusResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatal(err)
	}
	if res.State != "not_registered" {
		t.Errorf("state = %q, want not_registered", res.State)
	}
	if res.Fix == "" {
		t.Error("not_registered has no fix")
	}
}

// TestDaemonStatusEndToEndRegisteredStopped: a planted registration file
// under a prefix plus a dead socket is the registered-but-stopped state —
// the prefix makes the registration inert data a test can plant on any
// platform (no test may install a LaunchAgent on the machine running it).
func TestDaemonStatusEndToEndRegisteredStopped(t *testing.T) {
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	prefix := shortSock(t, "p")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(prefix, platform.SupervisorFilename(runtime.GOOS))
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runCLI(t, "daemon", "status", "--json", "--prefix", prefix)
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var res daemonStatusResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatal(err)
	}
	if res.State != "registered_stopped" || !res.Registered || res.Running {
		t.Errorf("state = %+v, want registered_stopped", res)
	}
	if res.Fix == "" || res.Fix == platform.DaemonFixesFor("").NotRegistered {
		t.Errorf("registered-stopped fix %q must differ from the not-registered fix", res.Fix)
	}
}

// TestDaemonStatusEndToEndRunning: a live endpoint answering /version is a
// running coordinator — the foreground mode the tests and CI gates use,
// with no supervisor involved.
func TestDaemonStatusEndToEndRunning(t *testing.T) {
	t.Setenv("WT_ENDPOINT", fakeVersionServer(t, api.VersionInfo{Min: api.VersionMin, Max: api.VersionMax}))
	code, stdout, stderr := runCLI(t, "daemon", "status", "--json", "--prefix", shortSock(t, "p"))
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	var res daemonStatusResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatal(err)
	}
	if res.State != "running" || !res.Reachable {
		t.Errorf("state = %+v, want running/reachable", res)
	}
	if res.Fix != "" {
		t.Errorf("a running coordinator has a fix: %q", res.Fix)
	}
}

// TestDaemonInstallPrefix is the no-test-installs-a-real-supervisor-unit
// rail: install with --prefix writes the registration file(s) into the
// prefix and loads nothing.
func TestDaemonInstallPrefix(t *testing.T) {
	prefix := shortSock(t, "p")
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", stub, "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	var res daemonInstallResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if res.Loaded {
		t.Error("a prefixed install reported loaded; it must never touch the supervisor")
	}
	wantPath := filepath.Join(prefix, platform.SupervisorFilename(runtime.GOOS))
	if res.RegistrationPath != wantPath {
		t.Errorf("registration path = %q, want %q", res.RegistrationPath, wantPath)
	}
	content := readRegistration(t, res.RegistrationPath)
	// What identifies the registration differs by supervisor. launchd and
	// systemd carry the label in the file; the Windows task's name is the
	// /TN argument schtasks is given, and the XML carries the action —
	// which is what a reader of the file has to be able to check.
	want := platform.LaunchAgentLabel
	if runtime.GOOS == "windows" {
		want = "<Command>" + stub + "</Command>"
	}
	if !strings.Contains(content, want) {
		t.Errorf("registration file lacks %s:\n%s", want, content)
	}

	code, stdout, _ = runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", stub)
	if code != ExitOK {
		t.Fatalf("human exit = %d", code)
	}
	if !strings.Contains(stdout, "installed:") || !strings.Contains(stdout, "loaded: no") {
		t.Errorf("human output:\n%s", stdout)
	}
}

// TestDaemonInstallRefusesWithoutSupervisor: a real (prefix-less)
// registration registers with the machine's supervisor, which no test may
// do on darwin, linux or windows — so the refusal path is only reachable
// on a platform this project does not target. The missing-wtd path is
// testable everywhere and exits 4 with the build remedy.
func TestDaemonInstallRefusesWithoutSupervisor(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		code, _, stderr := runCLI(t, "daemon", "install", "--wtd", filepath.Join(t.TempDir(), "missing-wtd"))
		if code != ExitUnavailable {
			t.Fatalf("exit = %d, want 4 for a missing coordinator binary; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "fix:") {
			t.Errorf("refusal has no remedy: %s", stderr)
		}
	default:
		code, _, stderr := runCLI(t, "daemon", "install", "--wtd", os.Args[0])
		if code != ExitUnavailable {
			t.Fatalf("exit = %d, want 4; stderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "foreground") {
			t.Errorf("refusal does not name the foreground alternative: %s", stderr)
		}
		if !strings.Contains(stderr, "fix:") {
			t.Errorf("refusal has no remedy: %s", stderr)
		}
	}
}

// TestDialCoordinatorDeadSocketExits5 is exit criterion 2's demonstration:
// a verb that reaches the coordinator and cannot exits 5 with the start
// command as its remedy, wired in the client's one dial-and-request helper.
func TestDialCoordinatorDeadSocketExits5(t *testing.T) {
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	sess, err := dialCoordinator()
	if sess != nil {
		sess.Close()
		t.Error("dial returned a session against a dead socket")
	}
	if err == nil {
		t.Fatal("dial against a dead socket succeeded")
	}
	if err.Code != ExitUnreachable {
		t.Errorf("code = %d, want 5 (coordinator unreachable)", err.Code)
	}
	if err.Msg == "" {
		t.Error("empty message")
	}
	// The remedy names the start command: `wt daemon install` on macOS,
	// the foreground wtd invocation elsewhere.
	if !strings.Contains(err.Remedy, "wtd") && !strings.Contains(err.Remedy, "install") {
		t.Errorf("remedy %q does not name the start command", err.Remedy)
	}
}

// TestCoordinatorDownLocalVerbsStillWork is exit criterion 2's other half:
// `show` and `guard` never dial, and `spec validate` and `spec explain` are
// pure functions of the spec and their arguments — all four keep working
// with the coordinator stopped (ARCHITECTURE.md §4.4, §10.1).
func TestCoordinatorDownLocalVerbsStillWork(t *testing.T) {
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")

	// guard in a directory that is not a repository fails locally —
	// never exit 5, which would mean it dialed.
	code, _, _ := runCLI(t, "guard", "--cwd", shortSock(t, "norepo"), "--path", "/x")
	if code == ExitUnreachable {
		t.Fatal("guard exited 5: it must never dial the coordinator")
	}

	// show, the same way.
	code, _, _ = runCLI(t, "show", "--cwd", shortSock(t, "norepo"))
	if code == ExitUnreachable {
		t.Fatal("show exited 5: it must never dial the coordinator")
	}

	// spec verbs are pure; validate still refuses a known-bad spec.
	path := filepath.Join("..", "..", "testdata", "specs", "cycle.yaml")
	code, _, _ = runCLI(t, "spec", "validate", path)
	if code == ExitUnreachable {
		t.Fatal("spec validate exited 5: it must never dial the coordinator")
	}
	if code != ExitFailure {
		t.Errorf("spec validate exit = %d, want 1 for a cycle spec", code)
	}
}

// TestVersionRefusalReachesTheExitCode: a coordinator one API version
// ahead refuses at dial time — the version check replaced the hello — with
// its own exit code and remedy naming the upgrade, and the client's helper
// hands both through unchanged. This is how a coordinator-side 3 reaches
// the process exit status.
func TestVersionRefusalReachesTheExitCode(t *testing.T) {
	t.Setenv("WT_ENDPOINT", fakeVersionServer(t, api.VersionInfo{Min: 2, Max: 2}))
	_, err := dialCoordinator()
	if err == nil {
		t.Fatal("a refused hello succeeded")
	}
	if err.Code != 3 {
		t.Errorf("code = %d, want 3 (refused)", err.Code)
	}
	if !strings.Contains(err.Msg, "upgrade the client") {
		t.Errorf("message does not name the upgrade: %s", err.Msg)
	}
	if !strings.Contains(err.Remedy, "upgrade wt") {
		t.Errorf("remedy does not name the install: %s", err.Remedy)
	}
}

// TestDaemonVerbUsage: daemon needs a subverb, and unknown subverbs are
// usage errors.
func TestDaemonVerbUsage(t *testing.T) {
	code, _, _ := runCLI(t, "daemon")
	if code != ExitUsage {
		t.Errorf("bare daemon exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "daemon", "bogus")
	if code != ExitUsage {
		t.Errorf("unknown daemon verb exit = %d, want 2", code)
	}
	code, stdout, _ := runCLI(t, "help")
	if !strings.Contains(stdout, "daemon status") || !strings.Contains(stdout, "daemon install") {
		t.Errorf("help lacks the daemon verbs:\n%s", stdout)
	}
	_ = code
}

// decodeUTF16LE decodes a UTF-16LE byte slice (BOM stripped) — the
// encoding schtasks requires for task XML, which the shared daemon tests
// read on the Windows job.
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

// readRegistration reads a supervisor registration file as text.
//
// The Windows one is the scheduled task's XML, which schtasks /Create /XML
// requires to be UTF-16 — read as a Go string it is a run of NULs between
// the characters, and every strings.Contains over it is false.
func readRegistration(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the registration %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return decodeUTF16LE(t, data)
	}
	return string(data)
}

// readWholeRegistration reads every file the platform's registration
// comprises, as one text. On launchd and the Task Scheduler that is the one
// registration file; on systemd it is the service unit *and* its paired
// socket unit, because the two together are the registration and which of
// them carries a given setting is systemd's business, not a caller's. The
// listen address is exactly such a setting: under socket activation the
// listener belongs to the socket unit, so a test asking "does the
// registration name this address" has to look at both.
func readWholeRegistration(t *testing.T, prefix string) string {
	t.Helper()
	whole := readRegistration(t, filepath.Join(prefix, registrationFilenameForThisPlatform()))
	if runtime.GOOS == "linux" {
		whole += readRegistration(t, filepath.Join(prefix, platform.SystemdSocketFilename))
	}
	return whole
}
