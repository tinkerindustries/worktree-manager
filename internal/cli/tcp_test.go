package cli

// tcp_test.go is the loopback TCP surface's end-to-end proof (phase 9,
// docs/ARCHITECTURE.md §4.1): the real wtd binary, started in the
// foreground with --tcp/--tcp-token, serving the real client (cli.Run)
// over tcp://127.0.0.1:<port> with WT_CLIENT_TOKEN set — the transport for
// hosts where a socket cannot be shared into a container (Docker Desktop's
// virtiofs). It needs no docker: the fixture's spec is ports-only.
//
// The assertions cover the whole surface: a full init/list/rm lifecycle
// over TCP, the same refusal for a wrong token and a missing one (exit 3,
// the coordinator's own refusal), the token's absence from the install
// transcript, and wtd's own refusal to start a TCP listener without a
// token or off loopback.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// tcpTestToken is the token the test's coordinator is configured with.
const tcpTestToken = "tcp-e2e-token-0123456789abcdef"

// buildWtd builds the real coordinator binary into a temp dir.
func buildWtd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "wtd")
	build := exec.Command("go", "build", "-o", bin, filepath.Join("..", "..", "cmd", "wtd"))
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building wtd: %v\n%s", err, out)
	}
	return bin
}

// startWtdTCP starts the real coordinator with the TCP surface enabled and
// returns the TCP address the client dials (parsed from the coordinator's
// own log, so the test never races a guessed port), the unix socket path,
// and the stop function.
func startWtdTCP(t *testing.T) (tcpAddr, sockPath string, stop func()) {
	t.Helper()
	bin := buildWtd(t)
	sock := shortSock(t, "tcp")
	storeRoot := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command(bin, "--socket", sock, "--tcp", "127.0.0.1:0", "--tcp-token", tcpTestToken)
	cmd.Env = append(os.Environ(), "WT_HOME="+storeRoot)
	var logBuf strings.Builder
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting wtd: %v", err)
	}
	stop = func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
	t.Cleanup(stop)

	// Wait for the TCP listener's bound address, which wtd logs once the
	// listener is open (ServeWithTCP logs "TCP listener open" with the
	// real address — 127.0.0.1:0 cannot be guessed, so the log is the
	// race-free source of the port).
	deadline := time.Now().Add(10 * time.Second)
	addr := ""
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(logBuf.String(), "\n") {
			if i := strings.Index(line, "msg=\"TCP listener open\""); i >= 0 {
				if j := strings.Index(line[i:], "addr="); j >= 0 {
					candidate := strings.TrimSpace(line[i+j+len("addr="):])
					candidate = strings.Trim(candidate, "\"")
					if strings.HasPrefix(candidate, "127.0.0.1:") {
						addr = candidate
						break
					}
				}
			}
		}
		if addr != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("wtd never logged its TCP address; log:\n%s", logBuf.String())
	}
	return "tcp://" + addr, sock, stop
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
			return os.MkdirAll(target, fi.Mode().Perm())
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, fi.Mode().Perm())
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
// contract the rm safety check reads (untagged twin of the acceptance
// layer's fakeGhNoPR).
func fakeGhAnswersNoPR(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	body := "#!/bin/sh\necho \"no pull requests found for branch \\\"$(git branch --show-current)\\\"\" >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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

// TestLoopbackTCPEndToEndAgainstRealWtd is the phase-9 verification of the
// TCP path end to end: a real wtd process, the real wire, the real client.
func TestLoopbackTCPEndToEndAgainstRealWtd(t *testing.T) {
	tcpAddr, sockPath, _ := startWtdTCP(t)
	main, worktree := tcpRepo(t)
	t.Setenv("WT_SOCKET", tcpAddr)
	t.Setenv("WT_CLIENT_TOKEN", tcpTestToken)
	fakeGhAnswersNoPR(t)

	// A full lifecycle over TCP: the named client allocates, materialises
	// (ports-only: no docker), activates and emits through the real wire.
	// The band is registered first — bands.reserve is host-client-only (it
	// changes machine-global policy), so it runs over the socket as the
	// host client while the process cwd stands in the repository (no test
	// in this package runs in parallel).
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(main); err != nil {
		t.Fatalf("chdir %s: %v", main, err)
	}
	defer os.Chdir(oldwd)
	t.Setenv("WT_SOCKET", sockPath)
	os.Unsetenv("WT_CLIENT_TOKEN")
	code, _, stderr := runCLI(t, "bands", "reserve", "--base", "api=10000", "--base", "proxy=10000")
	if code != ExitOK {
		t.Fatalf("bands reserve exit = %d; stderr:\n%s", code, stderr)
	}
	t.Setenv("WT_SOCKET", tcpAddr)
	t.Setenv("WT_CLIENT_TOKEN", tcpTestToken)

	code, _, stderr = runCLI(t, "init", "--cwd", worktree, "--description", "the TCP test's worktree")
	if code != ExitOK {
		t.Fatalf("init over TCP exit = %d; stderr:\n%s", code, stderr)
	}
	d := readTcpDescriptor(t, worktree)
	if d.Slot != 1 {
		t.Errorf("slot over TCP = %d, want 1", d.Slot)
	}

	// The registry is readable over TCP: the client's own entry, owned by
	// the token it presented.
	code, stdout, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list over TCP exit = %d; stderr:\n%s", code, stderr)
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

	// Teardown over TCP: rm runs the full sequence (fake gh answers the
	// no-PR contract) and the registry ends empty.
	code, _, stderr = runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("rm over TCP exit = %d; stderr:\n%s", code, stderr)
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
// the listener is not an oracle — with exit code 3 carried through the
// wire.
func TestLoopbackTCPRejectsWrongAndMissingToken(t *testing.T) {
	addr, _, _ := startWtdTCP(t)
	t.Setenv("WT_SOCKET", addr)

	for _, tc := range []struct {
		name string
		env  string
	}{
		{"wrong token", "wrong-token-0000000000000000"},
		{"missing token", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
		})
	}
}

// TestWtdRefusesMisconfiguredTCP: a TCP listener without a token, a token
// without a listener, a short token and a non-loopback address are all
// refused at startup — the surface is opt-in and loopback-only.
func TestWtdRefusesMisconfiguredTCP(t *testing.T) {
	bin := buildWtd(t)
	cases := [][]string{
		{"--socket", shortSock(t, "t1"), "--tcp", "127.0.0.1:0"},
		{"--socket", shortSock(t, "t2"), "--tcp-token", tcpTestToken},
		{"--socket", shortSock(t, "t3"), "--tcp", "127.0.0.1:0", "--tcp-token", "short"},
		{"--socket", shortSock(t, "t4"), "--tcp", "0.0.0.0:7331", "--tcp-token", tcpTestToken},
	}
	for _, args := range cases {
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err == nil {
			t.Errorf("wtd %v started, want a refusal", args)
			continue
		}
		if !strings.Contains(string(out), "tcp") && !strings.Contains(string(out), "TCP") && !strings.Contains(string(out), "loopback") {
			t.Errorf("wtd %v refused without a TCP reason: %s", args, out)
		}
	}
}

// TestDaemonInstallTCPWritesTokenIntoRegistration: `wt daemon install
// --tcp/--tcp-token` under a prefix writes the registration carrying the
// listener configuration, without ever echoing the token to the user.
func TestDaemonInstallTCPWritesTokenIntoRegistration(t *testing.T) {
	prefix := t.TempDir()
	// A stand-in wtd binary: the install only stats it.
	wtd := filepath.Join(prefix, "wtd")
	if err := os.WriteFile(wtd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the stand-in wtd: %v", err)
	}

	code, stdout, stderr := runCLI(t, "daemon", "install", "--prefix", prefix, "--wtd", wtd,
		"--tcp", "127.0.0.1:7331", "--tcp-token", tcpTestToken)
	if code != ExitOK {
		t.Fatalf("daemon install exit = %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, tcpTestToken) {
		t.Errorf("the install transcript echoes the token:\n%s", stdout)
	}
	if !strings.Contains(stdout, "loopback TCP: 127.0.0.1:7331") {
		t.Errorf("the install transcript must name the TCP address:\n%s", stdout)
	}
	// The registration file carries the token (the file is 0600 on unix
	// when it does).
	data, err := os.ReadFile(filepath.Join(prefix, registrationFilenameForThisPlatform()))
	if err != nil {
		t.Fatalf("reading the registration: %v", err)
	}
	if !strings.Contains(string(data), "--tcp") || !strings.Contains(string(data), tcpTestToken) {
		t.Errorf("the registration does not carry the TCP configuration:\n%s", data)
	}
	if fi, err := os.Stat(filepath.Join(prefix, registrationFilenameForThisPlatform())); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("the registration carrying the token is %o, want 0600", fi.Mode().Perm())
		}
	}
}

// registrationFilenameForThisPlatform maps the test's platform to the
// registration filename under a prefix: the plist on macOS, the systemd
// service unit elsewhere (the Windows task XML is a windows-only test).
func registrationFilenameForThisPlatform() string {
	if runtime.GOOS == "darwin" {
		return "com.mrgeoffrich.wtd.plist"
	}
	return "com.mrgeoffrich.wtd.service"
}
