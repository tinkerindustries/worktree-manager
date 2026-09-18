package cli

// tcp_test.go is the HTTP wire's end-to-end proof (phase R1): the real
// wtd binary, started in the foreground with --addr/--container-token,
// serving the real client (cli.Run) over http://127.0.0.1:<port> — the
// whole wire is loopback HTTP now, so this is the ordinary path, not an
// opt-in surface. It needs no docker: the fixture's spec is ports-only.
//
// The assertions cover the whole surface: a full init/list/rm lifecycle
// over HTTP, the same refusal for a wrong token and a missing one (exit
// 3, the coordinator's own refusal, carried in the response body), and
// wtd's own refusals at startup: a short container token and an off-
// loopback bind without --allow-remote.

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/descriptor"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// tcpTestToken is the token the test's coordinator is configured with.
const tcpTestToken = "tcp-e2e-token-0123456789abcdef"

// buildWtd builds the real coordinator binary into a temp dir (the .exe
// extension is required on Windows for the process to start).
func buildWtd(t *testing.T) string {
	t.Helper()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := filepath.Join(t.TempDir(), "wtd"+exe)
	build := exec.Command("go", "build", "-o", bin, filepath.Join("..", "..", "cmd", "wtd"))
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building wtd: %v\n%s", err, out)
	}
	return bin
}

// startWtdTCP starts the real coordinator with the container token
// enabled and returns the endpoint the client dials (read from the
// endpoint.json the coordinator writes into WT_HOME — the race-free
// source of the bound port) and the stop function.
func startWtdTCP(t *testing.T) (endpoint string, stop func()) {
	t.Helper()
	bin := buildWtd(t)
	storeRoot := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command(bin, "--addr", "127.0.0.1:0", "--container-token", tcpTestToken)
	cmd.Env = append(os.Environ(), "WT_HOME="+storeRoot)
	// The client resolves the endpoint and the host token from
	// endpoint.json under WT_HOME, exactly as a host client does.
	t.Setenv("WT_HOME", storeRoot)
	logBuf := &syncBuffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting wtd: %v", err)
	}
	stop = func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
	t.Cleanup(stop)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ep, err := api.ReadEndpoint(api.EndpointPath(storeRoot)); err == nil {
			return ep.BaseURL, stop
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("wtd never wrote the endpoint file; log:\n%s", logBuf.String())
	return "", stop
}

// copyFixtureTreeRecursive copies the compose-app fixture tree into dst
// (untagged twin of the acceptance layer's copyFixture, which lives behind
// the acceptance build tag).
func copyFixtureTreeRecursive(t *testing.T, dst string) {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	if err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(target, 0o755)
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return writeFixtureFile(target, data, fi.Mode())
		}
		return nil
	}); err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
}

// tcpIntPtr is the pointer helper for the TCP test's spec (intPtr and
// strPtr exist untagged in init_test.go and behind the acceptance tag).
func tcpIntPtr(i int) *int { return &i }

// tcpRepo builds the fixture repository with a ports-only spec (no docker
// anywhere in this layer) and one worktree on a pushed branch.
func tcpRepo(t *testing.T) (main, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	copyFixtureTreeRecursive(t, main)
	max := 32
	sp := &spec.Spec{
		Version: 1, App: "compose-app",
		Slots: spec.Slots{Max: tcpIntPtr(max)},
		Resources: []spec.Resource{
			{Type: "port", Name: "proxy", Form: strPtr("group"), Size: tcpIntPtr(2), Offset: tcpIntPtr(0)},
			{Type: "port", Name: "api", Form: strPtr("group"), Size: tcpIntPtr(2), Offset: tcpIntPtr(1)},
		},
		Reaper: spec.Reaper{Binaries: []string{"compose-app-dev"}},
		Emit:   spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(sp); err != nil {
		t.Fatalf("the TCP test spec does not validate: %v", err)
	}
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), data, 0o644); err != nil {
		t.Fatalf("writing the spec: %v", err)
	}
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "fixture")
	remote := filepath.Join(base, "remote.git")
	gitT(t, "", "init", "--bare", "-b", "main", remote)
	gitT(t, main, "remote", "add", "origin", remote)
	gitT(t, main, "push", "-u", "origin", "main")
	worktree = filepath.Join(base, "wt-1")
	gitT(t, main, "worktree", "add", "-b", "wt-1", worktree, "main")
	gitT(t, main, "push", "-u", "origin", "wt-1")
	return main, worktree
}

// fakeGhAnswersNoPR installs a fake gh on PATH answering the no-PR
// contract the rm safety check reads.
func fakeGhAnswersNoPR(t *testing.T) {
	t.Helper()
	fakeGhOnPath(t, ghNoPRScript)
}

// readTcpDescriptor reads the worktree's descriptor (untagged twin of the
// acceptance layer's readGateDescriptor).
func readTcpDescriptor(t *testing.T, worktree string) *descriptor.Descriptor {
	t.Helper()
	d, err := descriptor.Read(filepath.Join(worktree, "wt-env.yaml"), "yaml")
	if err != nil {
		t.Fatalf("reading the descriptor of %s: %v", worktree, err)
	}
	return d
}

// TestWtdWritesItsOwnLogFile: wtd.exe is built without a console subsystem
// on Windows (dist/build.sh) so the Task Scheduler logon task raises no
// window, which means stderr has nothing to reach when there is no
// inherited console. <store root>/logs/wtd.log is what a person actually
// has to look at in that case, so it must carry the same startup account
// stderr does, on every platform.
func TestWtdWritesItsOwnLogFile(t *testing.T) {
	_, stop := startWtdTCP(t)
	defer stop()

	// startWtdTCP points WT_HOME at the coordinator's store root and sets
	// it in this process's environment too (the client resolves the same
	// endpoint.json from it), so it names the log's directory as well.
	logPath := filepath.Join(os.Getenv("WT_HOME"), "logs", "wtd.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	if !strings.Contains(string(data), "wtd starting") {
		t.Errorf("the log file does not carry the startup line:\n%s", data)
	}
}

// TestLoopbackTCPEndToEndAgainstRealWtd is the R1 verification of the
// HTTP wire end to end: a real wtd process, the real wire, the real
// client.
func TestLoopbackTCPEndToEndAgainstRealWtd(t *testing.T) {
	endpoint, _ := startWtdTCP(t)
	main, worktree := tcpRepo(t)
	t.Setenv("WT_ENDPOINT", endpoint)
	t.Setenv("WT_CLIENT_TOKEN", tcpTestToken)
	fakeGhAnswersNoPR(t)

	// A full lifecycle over HTTP: the named client allocates, materialises
	// (ports-only: no docker), activates and emits through the real wire.
	// The band is registered first — bands.reserve is host-client-only (it
	// changes machine-global policy), so it runs as the host client (no
	// WT_CLIENT_TOKEN) while the process cwd stands in the repository (no
	// test in this package runs in parallel).
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(main); err != nil {
		t.Fatalf("chdir %s: %v", main, err)
	}
	defer os.Chdir(oldwd)
	os.Unsetenv("WT_CLIENT_TOKEN")
	code, _, stderr := runCLI(t, "bands", "reserve", "--base", "api=10000", "--base", "proxy=10000")
	if code != ExitOK {
		t.Fatalf("bands reserve exit = %d; stderr:\n%s", code, stderr)
	}
	t.Setenv("WT_CLIENT_TOKEN", tcpTestToken)

	code, _, stderr = runCLI(t, "init", "--cwd", worktree, "--description", "the TCP test's worktree")
	if code != ExitOK {
		t.Fatalf("init over HTTP exit = %d; stderr:\n%s", code, stderr)
	}
	d := readTcpDescriptor(t, worktree)
	if d.Slot != 1 {
		t.Errorf("slot over HTTP = %d, want 1", d.Slot)
	}

	// The registry is readable over HTTP: the client's own entry, owned by
	// the token it presented.
	code, stdout, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list over HTTP exit = %d; stderr:\n%s", code, stderr)
	}
	var listed struct {
		Entries []struct {
			App       string `json:"app"`
			Slug      string `json:"slug"`
			Slot      int    `json:"slot"`
			Owner     string `json:"owner"`
			OwnerKind string `json:"owner_kind"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &listed); err != nil {
		t.Fatalf("list over TCP is not one JSON object: %v\n%s", err, stdout)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Slot != 1 {
		t.Fatalf("entries over TCP = %+v", listed.Entries)
	}
	if listed.Entries[0].Owner != tcpTestToken || listed.Entries[0].OwnerKind != "named" {
		t.Errorf("owner over TCP = %s/%s, want the presented token", listed.Entries[0].OwnerKind, listed.Entries[0].Owner)
	}

	// Teardown over HTTP: rm runs the full sequence (fake gh answers the
	// no-PR contract) and the registry ends empty.
	code, _, stderr = runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("rm over HTTP exit = %d; stderr:\n%s", code, stderr)
	}
	code, stdout, _ = runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list after rm exit = %d", code)
	}
	if !strings.Contains(stdout, `"entries": []`) && !strings.Contains(stdout, `"entries":[]`) {
		t.Errorf("registry after rm over TCP = %s, want empty", stdout)
	}
}

// TestLoopbackTCPRejectsWrongAndMissingToken: the same refusal for both —
// the API is not an oracle — with exit code 3 carried in the response
// body and reaching the process exit status unchanged. A wrong bearer is
// WT_CLIENT_TOKEN with a wrong value; a missing one is a host client
// whose WT_HOME holds no endpoint.json (so it has no host token to
// present).
func TestLoopbackTCPRejectsWrongAndMissingToken(t *testing.T) {
	endpoint, _ := startWtdTCP(t)

	// The missing-token case must not see the real store's endpoint.json.
	t.Setenv("WT_HOME", t.TempDir())

	var wrongMsg, missingMsg string
	for _, tc := range []struct {
		name string
		env  string
	}{
		{"wrong token", "wrong-token-0000000000000000"},
		{"missing token", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WT_ENDPOINT", endpoint)
			if tc.env == "" {
				os.Unsetenv("WT_CLIENT_TOKEN")
			} else {
				t.Setenv("WT_CLIENT_TOKEN", tc.env)
			}
			code, _, stderr := runCLI(t, "list", "--json")
			if code != ExitRefused {
				t.Fatalf("list exit = %d, want 3 (refused); stderr:\n%s", code, stderr)
			}
			// The refusal must not leak either the configured token or the
			// attempted one.
			if strings.Contains(stderr, tcpTestToken) || (tc.env != "" && strings.Contains(stderr, tc.env)) {
				t.Errorf("the refusal leaks the token: %s", stderr)
			}
			if tc.env == "" {
				missingMsg = stderr
			} else {
				wrongMsg = stderr
			}
		})
	}
	if wrongMsg != missingMsg {
		t.Errorf("the refusals must not distinguish wrong from missing:\nwrong:   %smissing: %s", wrongMsg, missingMsg)
	}
}

// TestWtdRefusesMisconfiguredListener: a short container token and an
// off-loopback bind without --allow-remote are refused at startup — the
// coordinator is loopback-only by default and containers are opt-in.
func TestWtdRefusesMisconfiguredListener(t *testing.T) {
	bin := buildWtd(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"short container token", []string{"--container-token", "short"}, "16 characters"},
		{"off-loopback without --allow-remote", []string{"--addr", "0.0.0.0:7331"}, "loopback"},
		{"wildcard bind", []string{"--addr", ":7331"}, "loopback"},
	}
	for _, tc := range cases {
		out, err := exec.Command(bin, tc.args...).CombinedOutput()
		if err == nil {
			t.Errorf("wtd %v started, want a refusal", tc.args)
			continue
		}
		if !strings.Contains(string(out), tc.want) {
			t.Errorf("wtd %v refused without naming %q: %s", tc.args, tc.want, out)
		}
	}
}

// TestDaemonInstallTCPWritesTokenIntoRegistration: `wt daemon install
// --tcp/--tcp-token` under a prefix writes the registration carrying the
// listener configuration, without ever echoing the token to the user.
func TestDaemonInstallWritesContainerTokenIntoRegistration(t *testing.T) {
	prefix := t.TempDir()
	// A stand-in wtd binary: the install only stats it.
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in wtd: %v", err)
	}

	code, stdout, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", wtd,
		"--addr", "127.0.0.1:7331", "--container-token", tcpTestToken)
	if code != ExitOK {
		t.Fatalf("daemon install exit = %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, tcpTestToken) {
		t.Errorf("the install transcript echoes the token:\n%s", stdout)
	}
	if !strings.Contains(stdout, "127.0.0.1:7331") {
		t.Errorf("the install transcript must name the listen address:\n%s", stdout)
	}
	// The registration file carries the token (the file is 0600 on unix
	// when it does).
	reg := readRegistration(t, filepath.Join(prefix, registrationFilenameForThisPlatform()))
	if !strings.Contains(reg, "--container-token") || !strings.Contains(reg, tcpTestToken) {
		t.Errorf("the registration does not carry the container token:\n%s", reg)
	}
	// The address is named too, wherever this platform's registration keeps
	// it — the flag on launchd and the Task Scheduler, the socket unit's
	// ListenStream under systemd socket activation.
	if whole := readWholeRegistration(t, prefix); !strings.Contains(whole, "127.0.0.1:7331") {
		t.Errorf("the registration does not carry the listen address:\n%s", whole)
	}
	// The 0600 rail is unix: Windows file modes are ACL-shaped, and the
	// task XML's secrecy comes from the profile ACL, not a mode bit.
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(prefix, registrationFilenameForThisPlatform())); err == nil {
			if fi.Mode().Perm()&0o077 != 0 {
				t.Errorf("the registration carrying the token is %o, want 0600", fi.Mode().Perm())
			}
		}
	}
}

// registrationFilenameForThisPlatform maps the test's platform to the
// registration filename under a prefix: the plist on macOS, the task XML
// on Windows, the systemd service unit on Linux.
func registrationFilenameForThisPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "com.mrgeoffrich.wtd.plist"
	case "windows":
		return "com.mrgeoffrich.wtd.xml"
	default:
		return "com.mrgeoffrich.wtd.service"
	}
}

// syncBuffer is a bytes.Buffer a reader and exec's output copiers may both
// touch.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDaemonInstallCustomAddrNeedsNoContainerToken is the R3 regression.
// Before the validation split, the address and the token were checked as a
// pair — a hangover from when the TCP listener was opt-in — so installing a
// registration on a custom port without also inventing a container token
// was impossible. That is the ordinary case for the second user on a
// machine, whose coordinator cannot have the default port.
func TestDaemonInstallCustomAddrNeedsNoContainerToken(t *testing.T) {
	prefix := t.TempDir()
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in wtd: %v", err)
	}

	code, stdout, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", wtd,
		"--addr", "127.0.0.1:9001")
	if code != ExitOK {
		t.Fatalf("daemon install --addr with no container token exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "127.0.0.1:9001") {
		t.Errorf("the transcript must name the address it registered:\n%s", stdout)
	}
	if !strings.Contains(stdout, "not admitted") {
		t.Errorf("the transcript must say containers are not admitted when no token was given:\n%s", stdout)
	}

	if whole := readWholeRegistration(t, prefix); !strings.Contains(whole, "127.0.0.1:9001") {
		t.Errorf("the registration does not carry the address:\n%s", whole)
	}
	reg := readRegistration(t, filepath.Join(prefix, registrationFilenameForThisPlatform()))
	if strings.Contains(reg, "--container-token") {
		t.Errorf("the registration carries --container-token though none was configured:\n%s", reg)
	}
}

// TestDaemonInstallPinsAFreePortWhenTheDefaultIsHeld: with no --addr the
// install probes and pins a concrete port, so a second user's registration
// names a port their coordinator can actually take, and says so rather than
// silently choosing.
func TestDaemonInstallPinsAFreePortWhenTheDefaultIsHeld(t *testing.T) {
	prefix := t.TempDir()
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in wtd: %v", err)
	}

	code, stdout, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", wtd)
	if code != ExitOK {
		t.Fatalf("daemon install exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "listening on:") {
		t.Errorf("the transcript must always name the address it registered:\n%s", stdout)
	}
	// The transcript's address is the one the registration must name: what
	// makes the pin a pin is that the two agree, whichever file of this
	// platform's registration carries it.
	_, rest, _ := strings.Cut(stdout, "listening on:")
	pinned := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	if pinned == "" {
		t.Fatalf("the transcript names no address after 'listening on:':\n%s", stdout)
	}
	if whole := readWholeRegistration(t, prefix); !strings.Contains(whole, pinned) {
		t.Errorf("the registration must pin the concrete address %s rather than leaving it to the default:\n%s", pinned, whole)
	}
}

// TestDaemonInstallKeepsTheAddressItAlreadyHad is the re-install regression.
// The free-port probe exists so a second user on a machine gets a port they
// can bind; it must not move an existing coordinator. At probe time the
// running daemon still holds its own port, so a naive probe reads it as
// taken, steps past it, and the reload then frees the port the new
// registration has just stopped naming — walking the coordinator onto a new
// port on every upgrade and breaking every container configured with the
// old WT_ENDPOINT.
func TestDaemonInstallKeepsTheAddressItAlreadyHad(t *testing.T) {
	prefix := t.TempDir()
	home := t.TempDir()
	t.Setenv("WT_HOME", home)
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in wtd: %v", err)
	}

	// A coordinator that has run before leaves an endpoint file naming the
	// address it bound. Hold that port too, which is what makes the naive
	// probe move: the running daemon is the occupant.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer ln.Close()
	held := ln.Addr().String()
	if werr := api.WriteEndpoint(api.EndpointPath(home), &api.Endpoint{
		SchemaVersion: 1, BaseURL: "http://" + held, Token: strings.Repeat("a", 64),
	}); werr != nil {
		t.Fatalf("seeding the endpoint file: %v", werr)
	}

	code, stdout, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", wtd)
	if code != ExitOK {
		t.Fatalf("daemon install exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, held) {
		t.Errorf("re-install moved the coordinator off %s:\n%s", held, stdout)
	}
	if whole := readWholeRegistration(t, prefix); !strings.Contains(whole, held) {
		t.Errorf("the registration does not keep the existing address %s:\n%s", held, whole)
	}
}
