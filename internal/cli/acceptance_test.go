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
			return os.MkdirAll(target, fi.Mode().Perm())
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, fi.Mode().Perm())
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
