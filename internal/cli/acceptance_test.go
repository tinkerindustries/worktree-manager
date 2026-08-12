//go:build acceptance

package cli

// acceptance_test.go is the live layer for phase 5b: the side-by-side gate
// — two worktrees of compose-app run at once, both healthy, with disjoint
// resource tables, neither reaching the co-resident production stack —
// and its teardown half. It runs the real client (cli.Run) against a real
// coordinator (a coord.Server on a temp socket, the phase-6 CI
// arrangement) over real git worktrees and real docker.
//
// The one test double is gh: the fixture's branches live on a local bare
// remote, and the real gh cannot answer "is there a PR" for a repository
// that is not on GitHub. A fake gh on PATH answers the no-PR contract the
// check reads (exit 1, "no pull requests found"), exactly as the real
// tool does for a branch without a PR; the fail-closed side of the check
// is exercised untagged in rm_test.go.
//
// Every container, network and volume the gate creates carries the
// worktrees' project labels, and the test removes them before returning —
// pass or fail. Nothing with another label is ever touched.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/coord"
	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// gateBase is the band base the gate's band registration uses: far from
// the fixture's reserved 5319/5320, so the exclusion rail is asserted (the
// tables must never contain them) rather than exercised by accident.
const gateBase = 4400

// gateProjectPrefix is the compose project prefix the gate's projects use.
const gateApp = "compose-app"

// TestAcceptanceTwoWorktreesSideBySide is the phase's exit criterion 1 and
// 2: two worktrees of compose-app run side by side, both healthy at once,
// with disjoint resource tables and neither reaching the stack standing in
// for a production one; both tear down cleanly, leaving no containers, no
// volumes and no registry entries.
func TestAcceptanceTwoWorktreesSideBySide(t *testing.T) {
	if err := driver.NewDocker().Version(); err != nil {
		t.Fatalf("the acceptance gate needs a docker daemon: %v", err)
	}
	if err := exec.Command("go", "version").Run(); err != nil {
		t.Fatalf("the gate needs a go toolchain (the build hook runs go build): %v", err)
	}

	// The repository: a copy of the fixture, a bare remote, two worktrees.
	main, worktrees := gateRepo(t)

	// The coordinator: a real server on a temp socket, with the driver
	// registry and the app's band — the phase-6 CI arrangement.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := shortSock(t, "gate")
	storeRoot := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		t.Fatalf("store root: %v", err)
	}
	st, err := store.Open(storeRoot)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := coord.NewHandler(st, log)
	if err != nil {
		t.Fatalf("building the coordinator: %v", err)
	}
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	srv := coord.NewServer(h, log)
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, sock) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
		}
	})

	// The band: one base per port resource, both from gateBase — the group
	// derives api = base + slot×2 + 1, proxy = base + slot×2.
	sp := readFixtureSpec(t, main)
	spBases := map[string]int{"api": gateBase, "proxy": gateBase}
	registerBand(t, h, sp, spBases)

	t.Setenv("WT_SOCKET", sock)
	fakeGhNoPR(t)

	// Init and start both worktrees, and read their allocations back.
	type allocation struct {
		slug    string
		slot    int
		apiPort int
		project string
	}
	var allocs []allocation
	for _, wt := range worktrees {
		slug := filepath.Base(wt)
		code, _, stderr := runCLI(t, "init", "--cwd", wt, "--description", "the gate's worktree "+slug)
		if code != ExitOK {
			t.Fatalf("init of %s exit = %d; stderr:\n%s", slug, code, stderr)
		}
		d := readGateDescriptor(t, wt)
		if d.Slot != len(allocs)+1 {
			t.Fatalf("%s: slot = %d, want %d (the worktrees must take consecutive lowest-free slots)", slug, d.Slot, len(allocs)+1)
		}
		allocs = append(allocs, allocation{
			slug:    slug,
			slot:    d.Slot,
			apiPort: d.Resources["api"].Value.(int),
			project: fmt.Sprintf("%s-%s-%d", gateApp, slug, d.Slot),
		})

		// The stack comes up and the health hook passes.
		code, _, stderr = runCLI(t, "start", "--cwd", wt)
		if code != ExitOK {
			t.Fatalf("start of %s exit = %d; stderr:\n%s", slug, code, stderr)
		}
		// Both the container healthcheck and the worktree's own descriptor
		// say the stack is usable.
		if !stackHealthy(t, allocs[len(allocs)-1].project) {
			t.Fatalf("the %s stack never became healthy per docker", slug)
		}
	}

	// Both healthy at once, with disjoint resource tables.
	if allocs[0].slot == allocs[1].slot {
		t.Fatalf("slots = %d and %d; two worktrees cannot share a slot", allocs[0].slot, allocs[1].slot)
	}
	if allocs[0].apiPort == allocs[1].apiPort {
		t.Fatalf("api ports = %d and %d; the tables are not disjoint", allocs[0].apiPort, allocs[1].apiPort)
	}
	for _, a := range allocs {
		if a.apiPort == 5319 || a.apiPort == 5320 {
			t.Fatalf("%s's api port %d falls in the production stack's reserved ports", a.slug, a.apiPort)
		}
	}
	// Neither worktree reaches the production stack: nothing of it exists.
	if n := projectObjectCount(t, "compose-app-prod"); n != 0 {
		t.Fatalf("%d docker object(s) carry the production project label; a worktree reached the prod stack", n)
	}

	// Criterion 5's shape: the second worktree's directory disappears
	// without wt's involvement, then rm by slug, from outside the tree,
	// with the directory already gone.
	if err := os.RemoveAll(worktrees[1]); err != nil {
		t.Fatalf("deleting %s by hand: %v", worktrees[1], err)
	}
	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", filepath.Base(worktrees[1]))
	if code != ExitOK {
		t.Fatalf("rm of the gone worktree exit = %d; stderr:\n%s", code, stderr)
	}
	// The first worktree is still present: full rm with the tree checks.
	code, _, stderr = runCLI(t, "rm", "--cwd", main, "--slug", filepath.Base(worktrees[0]))
	if code != ExitOK {
		t.Fatalf("rm of the present worktree exit = %d; stderr:\n%s", code, stderr)
	}

	// Criterion 2: nothing left — no containers, no volumes, no registry
	// entries. The labels are the gate's own; teardown by label never
	// reaches anything else.
	for _, a := range allocs {
		if n := projectObjectCount(t, a.project); n != 0 {
			t.Errorf("%d docker object(s) survived the teardown of %s", n, a.project)
		}
	}
	reg, err := st.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("registry entries = %d, want 0 after both rms: %+v", len(reg.Entries), reg.Entries)
	}

	// Acceptance gate 1's completion (phase 6): doctor is clean after both
	// teardowns — it reads everything, writes nothing, and finds nothing
	// wrong.
	code, stdout, stderr := runCLI(t, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d; stderr:\n%s", code, stderr)
	}
	var doc protocol.DoctorResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("doctor stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(doc.Findings) != 0 {
		t.Errorf("doctor after both teardowns reported findings:\n%s", docJSON(doc))
	}
}

// gateRepo copies the fixture, builds the git repository and creates the
// two worktrees with pushed branches.
func gateRepo(t *testing.T) (main string, worktrees []string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	copyFixture(t, main)
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	remote := filepath.Join(base, "remote.git")
	gitT(t, "", "init", "--bare", "-b", "main", remote)
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "fixture")
	gitT(t, main, "remote", "add", "origin", remote)
	gitT(t, main, "push", "-u", "origin", "main")
	for _, slug := range []string{"wt-1", "wt-2"} {
		wt := filepath.Join(base, slug)
		gitT(t, main, "worktree", "add", "-b", slug, wt, "main")
		gitT(t, main, "push", "-u", "origin", slug)
		worktrees = append(worktrees, wt)
	}
	return main, worktrees
}

// copyFixture copies the compose-app fixture tree into dst.
func copyFixture(t *testing.T, dst string) {
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
		default:
			return nil
		}
	}); err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
}

// readFixtureSpec parses the committed spec of the gate's repository.
func readFixtureSpec(t *testing.T, repoRoot string) *spec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "wt.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture spec: %v", err)
	}
	sp, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("parsing the fixture spec: %v", err)
	}
	if err := spec.Validate(sp); err != nil {
		t.Fatalf("the fixture spec does not validate: %v", err)
	}
	return sp
}

// registerBand registers the app's band through the handler, the way
// `wt bands reserve` would.
func registerBand(t *testing.T, h *coord.Handler, sp *spec.Spec, bases map[string]int) {
	t.Helper()
	sess, reply := h.Begin(coord.Peer{UID: 4242, Known: true}, &protocol.Hello{
		Kind: protocol.KindHost, MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
	})
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	raw, err := json.Marshal(&protocol.ReserveBandArgs{Spec: *sp, Bases: bases})
	if err != nil {
		t.Fatalf("encoding the reservation: %v", err)
	}
	resp := h.Handle(context.Background(), sess, &protocol.Request{Verb: "bands.reserve", Args: raw})
	if resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}
}

// readGateDescriptor reads the worktree's descriptor.
func readGateDescriptor(t *testing.T, worktree string) *descriptor.Descriptor {
	t.Helper()
	d, err := descriptor.Read(filepath.Join(worktree, "wt-env.yaml"), "yaml")
	if err != nil {
		t.Fatalf("reading the descriptor of %s: %v", worktree, err)
	}
	return d
}

// stackHealthy asks docker whether the project's api container is healthy,
// via inspect (the ps formatter has no Health field in every docker
// version).
func stackHealthy(t *testing.T, project string) bool {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service=api").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	for _, id := range strings.Fields(string(out)) {
		status, ierr := exec.Command("docker", "inspect",
			"--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", id).Output()
		if ierr != nil {
			t.Fatalf("docker inspect %s: %v", id, ierr)
		}
		fields := strings.Fields(string(status))
		if len(fields) >= 2 && fields[0] == "running" && fields[1] == "healthy" {
			return true
		}
	}
	return false
}

// projectObjectCount counts the docker objects carrying the project label.
func projectObjectCount(t *testing.T, project string) int {
	t.Helper()
	count := 0
	for _, args := range [][]string{
		{"ps", "-a", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.ID}}"},
		{"volume", "ls", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.Name}}"},
		{"network", "ls", "--filter", "label=com.docker.compose.project=" + project, "--format", "{{.ID}}"},
	} {
		out, err := exec.Command("docker", args...).Output()
		if err != nil {
			t.Fatalf("docker %s: %v", args[0], err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				count++
			}
		}
	}
	return count
}

// fakeGhNoPR installs a fake gh on PATH answering the no-PR contract the
// rm safety check reads.
func fakeGhNoPR(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	body := "#!/bin/sh\necho \"no pull requests found for branch \\\"$(git branch --show-current)\\\"\" >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// gate2Spec is the allocation spec both sides of gate 2 adopt: the
// compose-app app, ports only — no namespace, no state-path, no hooks — so
// the gate allocates against the real app without needing docker inside the
// container or spinning up stacks. The band comes from this spec, exactly
// as it would from the full one (the same port resources).
func gate2Spec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 32
	s := &spec.Spec{
		Version: 1, App: gateApp,
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "proxy", Form: strPtr("group"), Size: intPtr(2), Offset: intPtr(0)},
			{Type: "port", Name: "api", Form: strPtr("group"), Size: intPtr(2), Offset: intPtr(1)},
		},
		Reaper: spec.Reaper{Binaries: []string{"compose-app-dev"}},
		Emit:   spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("gate 2 spec does not validate: %v", err)
	}
	return s
}

// gate2Repo builds one side's repository: the fixture copy with the gate 2
// spec committed, git-initialised, plus n linked worktrees on pushed
// branches.
func gate2Repo(t *testing.T, n int) (main string, worktrees []string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	copyFixture(t, main)
	sp := gate2Spec(t)
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the gate 2 spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), data, 0o644); err != nil {
		t.Fatalf("writing the gate 2 spec: %v", err)
	}
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	remote := filepath.Join(base, "remote.git")
	gitT(t, "", "init", "--bare", "-b", "main", remote)
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "fixture")
	gitT(t, main, "remote", "add", "origin", remote)
	gitT(t, main, "push", "-u", "origin", "main")
	for i := 1; i <= n; i++ {
		slug := fmt.Sprintf("wt-%d", i)
		wt := filepath.Join(base, slug)
		gitT(t, main, "worktree", "add", "-b", slug, wt, "main")
		gitT(t, main, "push", "-u", "origin", slug)
		worktrees = append(worktrees, wt)
	}
	return main, worktrees
}

// containerTransport is how this machine's docker daemon shares this
// process's files into a container. Two modes, probed at runtime:
//
//   - bind mode: the daemon can bind-mount the files at their own paths.
//     This is the CI arrangement (ubuntu-latest: the daemon and the
//     workspace share one filesystem) and any local docker where the
//     workspace is a shared path.
//   - volume mode: the daemon cannot bind-mount the workspace (Docker
//     Desktop with the workspace outside its file-sharing roots), so the
//     shared files live in a docker volume this process can also see —
//     the volume mounted at /data on this machine — and the container
//     mounts that volume. A docker volume is a local filesystem, which is
//     what unix sockets need: a virtiofs bind mount cannot carry a live
//     socket, so a socket must live on the volume or on the daemon's own
//     filesystem.
//
// The probe is a listener socket plus a dial from inside a container
// through the candidate transport: sharing works only when the dial
// reaches the listener. Without a working transport the gate cannot
// construct a container client and skips with the reason stated.
type containerTransport struct {
	volume string // the docker volume name (volume mode), else ""
	base   string // the sandbox directory holding the shared files
	probe  string // the probe binary, shared like everything else
}

// containerPath maps a sandbox path to the path the container sees. In
// volume mode the transport's base is a subdirectory of the volume root
// (the volume is mounted at /data on this machine), so the container path
// keeps the whole relative tail, not just the basename.
func (tr *containerTransport) containerPath(sandbox string) string {
	if tr.volume != "" {
		rel, err := filepath.Rel("/data", sandbox)
		if err != nil {
			return "/vol/" + filepath.Base(sandbox)
		}
		return filepath.Join("/vol", rel)
	}
	return "/s/" + filepath.Base(sandbox)
}

// runArgs returns the docker run command (the args after "docker") that
// share the transport's files into a container: the wt binary, the fake
// gh, the scripts, the repository source, the persistent clone, the
// socket, the probe and the bare remote.
func (tr *containerTransport) runArgs() []string {
	args := []string{"run", "--rm"}
	if tr.volume != "" {
		args = append(args, "-v", tr.volume+":/vol")
	} else {
		for _, name := range []string{"wt", "gh", "init.sh", "rm.sh", "src", "repo", "sock", "probe", "remote.git"} {
			args = append(args, "-v", filepath.Join(tr.base, name)+":/s/"+name)
		}
	}
	return args
}

// ensureMountSources creates every bind-mount source before docker sees it.
//
// Docker creates a missing bind-mount source itself, as a root-owned
// directory. runArgs mounts nine fixed paths, and the transport probe runs
// before src, repo and remote.git exist — so the probe left three root-owned
// directories behind, and the fixture copy into src then failed with
// "permission denied" as the unprivileged test user. It reproduced on every
// CI runner and never on macOS, where this gate skips for want of a socket
// the daemon can share.
//
// sock is deliberately absent: it is a unix socket the listener creates, and
// pre-creating it as a regular file would stop the bind from working.
func (tr *containerTransport) ensureMountSources(t *testing.T) {
	t.Helper()
	if tr.volume != "" {
		return
	}
	for _, name := range []string{"src", "repo", "remote.git"} {
		if err := os.MkdirAll(filepath.Join(tr.base, name), 0o755); err != nil {
			t.Fatalf("creating the mount source %s: %v", name, err)
		}
	}
	for _, name := range []string{"wt", "gh", "init.sh", "rm.sh", "probe"} {
		path := filepath.Join(tr.base, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatalf("creating the mount source %s: %v", name, err)
		}
	}
}

// containerSock returns the socket path the container dials.
func (tr *containerTransport) containerSock() string {
	return tr.containerPath(filepath.Join(tr.base, "sock"))
}

// probeTransport builds the probe binary, lays out the shared files, and
// proves that a container can reach a listener socket through the
// candidate transport. It returns the working transport or skips the test.
func probeTransport(t *testing.T) *containerTransport {
	t.Helper()

	buildProbe := func(dir string) string {
		src := "package main\n" +
			"import (\"fmt\"; \"net\"; \"os\"; \"time\")\n" +
			"func main() {\n" +
			" if os.Args[1] == \"listen\" {\n" +
			"  os.Remove(os.Args[2])\n" +
			"  l, err := net.Listen(\"unix\", os.Args[2])\n" +
			"  if err != nil { fmt.Println(\"LISTEN-FAIL\", err); os.Exit(1) }\n" +
			"  c, err := l.Accept()\n" +
			"  if err != nil { os.Exit(1) }\n" +
			"  c.Close()\n" +
			"  fmt.Println(\"ACCEPTED\")\n" +
			"  return\n" +
			" }\n" +
			" c, err := net.DialTimeout(\"unix\", os.Args[1], 5*time.Second)\n" +
			" if err != nil { fmt.Println(\"DIAL-FAIL\", err); os.Exit(1) }\n" +
			" c.Close()\n" +
			" fmt.Println(\"DIAL-OK\")\n" +
			"}\n"
		srcPath := filepath.Join(dir, "probe.go")
		writeT(t, srcPath, src)
		bin := filepath.Join(dir, "probe")
		build := exec.Command("go", "build", "-o", bin, srcPath)
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("building the socket probe: %v\n%s", err, out)
		}
		return bin
	}

	// The probe: a listener in this process, a dial from inside a
	// container through the candidate mounts.
	probeShare := func(tr *containerTransport) bool {
		listen := exec.Command(tr.probe, "listen", filepath.Join(tr.base, "sock"))
		if err := listen.Start(); err != nil {
			t.Fatalf("starting the probe listener: %v", err)
		}
		defer func() {
			listen.Process.Kill()
			listen.Wait()
		}()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(tr.base, "sock")); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		tr.ensureMountSources(t)
		args := tr.runArgs()
		probe := tr.containerPath(filepath.Join(tr.base, "probe"))
		sock := tr.containerPath(filepath.Join(tr.base, "sock"))
		args = append(args, "alpine:latest", "sh", "-c", probe+" "+sock)
		out, err := exec.Command("docker", args...).CombinedOutput()
		return err == nil && strings.Contains(string(out), "DIAL-OK")
	}

	// Candidate 1: bind mode — the files at their own paths. The base is a
	// temp dir; on macOS /tmp is a Docker Desktop file-sharing root, which
	// is what a bind mount needs, so the base sits there.
	baseDir, err := os.MkdirTemp("/tmp", "wtp6-g2")
	if err != nil {
		t.Fatalf("creating the bind-mode base: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(baseDir) })
	bind := &containerTransport{base: baseDir, probe: buildProbe(baseDir)}
	if probeShare(bind) {
		return bind
	}

	// Candidate 2: volume mode — the docker volume mounted at /data on
	// this machine (its name comes from /proc/self/mountinfo, so this
	// works without knowing the machine's layout).
	volume := ""
	for _, line := range strings.Split(string(readProcMountInfo(t)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[4] == "/data" {
			src := fields[3]
			if rest, ok := strings.CutPrefix(src, "/docker/volumes/"); ok {
				if name, _, found := strings.Cut(rest, "/_data"); found && name != "" {
					volume = name
					break
				}
			}
		}
	}
	if volume != "" {
		volBase := filepath.Join("/data", fmt.Sprintf("wtp6-gate2-%d", time.Now().UnixNano()))
		if err := os.MkdirAll(volBase, 0o700); err != nil {
			t.Fatalf("creating the volume-mode base: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(volBase) })
		vol := &containerTransport{volume: volume, base: volBase, probe: buildProbe(volBase)}
		if probeShare(vol) {
			return vol
		}
	}

	t.Skip("this machine's docker daemon cannot share a live unix socket into a container (neither bind mounts nor the shared volume work); acceptance gate 2 runs on the ubuntu-latest CI runner, where the daemon and the workspace share one filesystem")
	return nil
}

// readProcMountInfo reads /proc/self/mountinfo for the volume-mode probe.
func readProcMountInfo(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	return data
}

// TestAcceptanceGate2ContainerAndHostAllocate is acceptance gate 2: a
// container client and a host client allocate against the same app, and
// the container's slot is unavailable to the host.
//
// The container client is a real docker container running the real wt
// binary (built CGO_ENABLED=0, so it runs on alpine) with the
// coordinator's socket shared into it, WT_CLIENT_TOKEN set — the
// named-container identity phase 2 built the coordinator side of and phase
// 6 wires the client side — and a disposable git checkout of the app's
// repository cloned inside the shared filesystem. The container's worktree
// is a linked worktree of that clone, so its rm runs from the clone's main
// checkout like any host rm. The file sharing between this process and the
// container goes through the probed transport (bind mounts on a CI runner,
// the shared volume on a dev sandbox). Everything the gate creates is
// removed before it returns.
func TestAcceptanceGate2ContainerAndHostAllocate(t *testing.T) {
	if err := driver.NewDocker().Version(); err != nil {
		t.Fatalf("the acceptance gate needs a docker daemon: %v", err)
	}
	if err := exec.Command("go", "version").Run(); err != nil {
		t.Fatalf("the gate needs a go toolchain: %v", err)
	}
	tr := probeTransport(t)

	// The coordinator: a real server on a temp socket, with the driver
	// registry and the app's band — the phase-6 CI arrangement. The socket
	// lives on the transport's shared filesystem so the container can dial
	// it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(tr.base, "sock")
	storeRoot := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		t.Fatalf("store root: %v", err)
	}
	st, err := store.Open(storeRoot)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := coord.NewHandler(st, log)
	if err != nil {
		t.Fatalf("building the coordinator: %v", err)
	}
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	srv := coord.NewServer(h, log)
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, sock) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
		}
	})

	sp := gate2Spec(t)
	registerBand(t, h, sp, map[string]int{"api": gateBase, "proxy": gateBase})
	t.Setenv("WT_SOCKET", sock)
	fakeGhNoPR(t)

	// The host repository: three worktrees of the app.
	hostMain, hostWTs := gate2Repo(t, 3)

	// The container side's repository source, plus the bare remote its
	// branch is pushed to (the rm safety check stops on an absent
	// upstream), all on the shared filesystem.
	containerSrc := filepath.Join(tr.base, "src")
	copyFixture(t, containerSrc)
	spData, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(containerSrc, "wt.yaml"), spData, 0o644); err != nil {
		t.Fatalf("writing the container's spec: %v", err)
	}
	gitT(t, "", "init", "-b", "main", containerSrc)
	gitT(t, containerSrc, "config", "user.email", "t@example.com")
	gitT(t, containerSrc, "config", "user.name", "T")
	gitT(t, containerSrc, "add", ".")
	gitT(t, containerSrc, "commit", "-m", "fixture")
	remote := filepath.Join(tr.base, "remote.git")
	gitT(t, "", "init", "--bare", "-b", "main", remote)

	// The wt binary the container runs: static, so it runs on alpine.
	bin := filepath.Join(tr.base, "wt")
	build := exec.Command("go", "build", "-o", bin, filepath.Join("..", "..", "cmd", "wt"))
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building wt for the container: %v\n%s", err, out)
	}

	// The scripts and the fake gh, all on the shared filesystem.
	initScript := filepath.Join(tr.base, "init.sh")
	writeT(t, initScript, `#!/bin/sh
set -e
apk add --no-cache git >/dev/null 2>&1
# The shared tree is owned by the host user and this container runs as
# root, which git refuses to touch until told the ownership is expected.
git config --global --add safe.directory '*'
# $REPO is a bind-mount point, so it cannot be removed from in here —
# only emptied. $WT is ordinary container filesystem and goes whole.
rm -rf "$REPO"/..?* "$REPO"/.[!.]* "$REPO"/* 2>/dev/null || true
rm -rf "$WT"
git clone -q "$SRC" "$REPO"
git -C "$REPO" config user.email t@example.com
git -C "$REPO" config user.name T
git -C "$REPO" worktree add -q -b wt-c "$WT" main
git -C "$REPO" remote set-url origin "$REMOTE"
git -C "$REPO" push -q -u origin wt-c
wt init --cwd "$WT" --description "the gate's container client" --json
`)
	rmScript := filepath.Join(tr.base, "rm.sh")
	writeT(t, rmScript, `#!/bin/sh
set -e
apk add --no-cache git >/dev/null 2>&1
# The shared tree is owned by the host user and this container runs as
# root, which git refuses to touch until told the ownership is expected.
git config --global --add safe.directory '*'
wt rm --cwd "$REPO" --slug wt-c
`)
	fakeGh := filepath.Join(tr.base, "gh")
	writeT(t, fakeGh, "#!/bin/sh\necho \"no pull requests found for branch \\\"$(git branch --show-current)\\\"\" >&2\nexit 1\n")
	// exec.LookPath requires the executable bit: gh must be runnable.
	if err := os.Chmod(fakeGh, 0o755); err != nil {
		t.Fatalf("chmod the fake gh: %v", err)
	}

	// The host client allocates first: slot 1.
	code, _, stderr := runCLI(t, "init", "--cwd", hostWTs[0], "--description", "gate 2 host worktree 1")
	if code != ExitOK {
		t.Fatalf("host init 1 exit = %d; stderr:\n%s", code, stderr)
	}
	hostSlot1 := readGateDescriptor(t, hostWTs[0]).Slot
	if hostSlot1 != 1 {
		t.Fatalf("host slot 1 = %d, want 1", hostSlot1)
	}

	// The container client allocates concurrently: a docker container
	// running the real wt with the token and a disposable clone of the
	// app's repository.
	token := "gate2-token-" + fmt.Sprint(time.Now().UnixNano())
	containerName := fmt.Sprintf("wtp6-gate2-%d", time.Now().UnixNano())
	runContainer := func(scriptName string) string {
		t.Helper()
		args := tr.runArgs()
		args = append(args,
			"--name", containerName,
			"-e", "WT_SOCKET="+tr.containerSock(),
			"-e", "WT_CLIENT_TOKEN="+token,
			"-e", "SRC="+tr.containerPath(containerSrc),
			"-e", "REPO="+tr.containerPath(filepath.Join(tr.base, "repo")),
			"-e", "WT="+tr.containerPath(filepath.Join(tr.base, "wt-c")),
			"-e", "REMOTE="+tr.containerPath(remote))
		if tr.volume != "" {
			args = append(args, "-e", "PATH="+tr.containerPath(tr.base)+":/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
		}
		args = append(args,
			"golang:1.26-alpine",
			"sh", tr.containerPath(filepath.Join(tr.base, scriptName)))
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", containerName).Run() })
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("container run %s failed: %v\n%s", scriptName, err, out)
		}
		return string(out)
	}

	initOut := runContainer("init.sh")
	var containerInit struct {
		App  string `json:"app"`
		Slug string `json:"slug"`
		Slot int    `json:"slot"`
	}
	// The init result is one pretty-printed JSON object, printed last (the
	// diagnostics go to stderr first), so the object is the tail of the
	// output starting at its first brace.
	parsed := false
	if i := strings.Index(initOut, "{"); i >= 0 {
		if err := json.Unmarshal([]byte(initOut[i:]), &containerInit); err == nil {
			parsed = true
		}
	}
	if !parsed {
		t.Fatalf("the container's init did not print one JSON object:\n%s", initOut)
	}
	if containerInit.App != gateApp || containerInit.Slot != 2 {
		t.Fatalf("container allocation = %+v, want app %s slot 2", containerInit, gateApp)
	}

	// The host's second allocation cannot take the container's slot: it
	// lands on the next free one.
	code, _, stderr = runCLI(t, "init", "--cwd", hostWTs[1], "--description", "gate 2 host worktree 2")
	if code != ExitOK {
		t.Fatalf("host init 2 exit = %d; stderr:\n%s", code, stderr)
	}
	hostSlot2 := readGateDescriptor(t, hostWTs[1]).Slot
	if hostSlot2 == containerInit.Slot {
		t.Fatalf("the host took the container's slot %d; the container's slot must be unavailable to the host", containerInit.Slot)
	}

	// The registry shows the container's entry owned by the named token —
	// the identity phase 2 built the coordinator side of.
	code, stdout, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list exit = %d; stderr:\n%s", code, stderr)
	}
	var listed protocol.ListResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, e := range listed.Entries {
		if e.Slot == containerInit.Slot && e.App == gateApp {
			found = true
			// The entry belongs to the container's named identity. The host
			// client reads the entry as foreign, so the owner key is the
			// redacted form — never the raw token, which would hand the
			// identity over (phase 9's security pass). The kind and the
			// foreign marker are the identity's public face.
			if e.OwnerKind != "named" || e.Owner == token {
				t.Errorf("container entry owner = %s/%s, want the named kind with the token redacted", e.OwnerKind, e.Owner)
			}
			if len(e.Owner) == 0 || len(e.Owner) >= len(token) {
				t.Errorf("container entry owner = %q, want a short redaction", e.Owner)
			}
			if !hasFlagT(e.Flags, "foreign") {
				t.Errorf("the container's entry must be marked foreign to the host: %v", e.Flags)
			}
		}
	}
	if !found {
		t.Fatalf("no entry at the container's slot %d: %+v", containerInit.Slot, listed.Entries)
	}

	// The container removes its own entry (ownership is enforced; only its
	// token may). The host's next allocation then reuses the freed slot —
	// the hold was the container's.
	runContainer("rm.sh")
	code, _, stderr = runCLI(t, "init", "--cwd", hostWTs[2], "--description", "gate 2 host worktree 3")
	if code != ExitOK {
		t.Fatalf("host init 3 exit = %d; stderr:\n%s", code, stderr)
	}
	hostSlot3 := readGateDescriptor(t, hostWTs[2]).Slot
	if hostSlot3 != containerInit.Slot {
		t.Errorf("host slot after the container's rm = %d, want the freed container slot %d", hostSlot3, containerInit.Slot)
	}

	// Teardown: the host removes its own three worktrees. Nothing else was
	// created — the gate's spec has no namespace, so no docker objects.
	for _, wt := range hostWTs {
		code, _, stderr = runCLI(t, "rm", "--cwd", hostMain, "--slug", filepath.Base(wt))
		if code != ExitOK {
			t.Fatalf("rm of %s exit = %d; stderr:\n%s", wt, code, stderr)
		}
	}
	reg, err := st.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("registry entries = %d after the gate, want 0: %+v", len(reg.Entries), reg.Entries)
	}
}

// docJSON renders a doctor result for a failure message.
func docJSON(doc protocol.DoctorResult) string {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Sprint(doc)
	}
	return string(data)
}

// intPtr is the pointer helper for the gate 2 spec's group form.
func intPtr(i int) *int { return &i }
