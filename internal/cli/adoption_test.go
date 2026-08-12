//go:build !acceptance

package cli

// adoption_test.go is the phase-7 adoption layer, deliberately untagged:
// plain-app uses no compose, so the whole skill flow — audit, policy,
// bands, spec, generate, patch, prove, import — runs under plain
// `go test ./...`, with no docker and no gh (the fake gh answers the
// no-PR contract, exactly as the acceptance gate does).
//
// Two tests map to the phase-7 exit criteria:
//
//   - TestAcceptancePlainAppAdoptedThroughTheSkill (criterion 1): the
//     adopted fixture goes through the skill, which proves state-path's
//     three seed modes and the default:shared resource end to end — two
//     worktrees side by side, both healthy, disjoint tables, torn down,
//     doctor clean.
//   - TestAcceptanceSkillEightPhasesOnSpeclessFixture (criterion 2): the
//     skill's eight phases run start to finish on a copy of plain-app
//     with the spec removed — the honest "no spec yet" case — ending with
//     two worktrees side by side.
//
// The test acts as the skill: it runs the primitives the skill documents
// (ports scan, bands suggest, bands reserve, spec validate, spec explain)
// and drives artefact.Render + Apply as the skill's generate phase.

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

	"github.com/mrgeoffrich/worktree-manager/internal/artefact"
	"github.com/mrgeoffrich/worktree-manager/internal/coord"
	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// adoptionBand is the base the tests reserve and the artefacts record.
// The fixture's canonical 8200 is not usable in every sandbox — a machine
// running the tests can hold other listeners (this sandbox runs its own
// service on 8080, and an interrupted run can leak a worktree's server on
// the low ranges) — so the tests choose a base from a range that is free
// and reserve it, exactly as the skill's phase 3 does: the developer
// picks the base, not the tool. The assertions therefore pin the
// base-plus-slot derivation, never a particular slot number.
const adoptionBand = 10000

// adoptionEnv is the whole stage: the coordinator, the repository copy,
// the shared sources, and the environment.
type adoptionEnv struct {
	t       *testing.T
	main    string // the main checkout
	home    string // the temp HOME holding the shared sources
	fixture string // the absolute fixture path (reads after chdir)
	sock    string
	st      *store.Store
	ctx     context.Context
}

// newAdoptionEnv builds the stage. withSpec controls whether the copy
// carries the committed spec: the criterion-2 fixture is plain-app with
// the spec AND the adopted surface removed.
func newAdoptionEnv(t *testing.T, withSpec bool) *adoptionEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The shared sources the seed modes read from: the db file the seeded
	// mode snapshots, the cache directory the empty mode ignores, and the
	// shared store the shared mode reaches unisolated.
	mkSharedSource(t, filepath.Join(home, ".plain-app", "db.sqlite"), "SHARED-DB-CONTENT-V1\n")
	mkSharedSource(t, filepath.Join(home, ".plain-app", "cache", "keep.txt"), "scratch\n")
	mkSharedSource(t, filepath.Join(home, ".plain-app", "shared", "db.sqlite"), "SHARED-STORE-V1\n")

	// The repository: the fixture copy, adopted or not.
	base := t.TempDir()
	main := filepath.Join(base, "main")
	copyFixtureTree(t, main, withSpec)
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "fixture")
	remote := filepath.Join(base, "remote.git")
	gitT(t, "", "init", "--bare", "-b", "main", remote)
	gitT(t, main, "remote", "add", "origin", remote)
	gitT(t, main, "push", "-u", "origin", "main")
	// The main checkout's .env: the first-init seed source (unmanaged
	// content only — the managed keys come from each worktree's own
	// allocation). Written after the push so it is never committed, and
	// the repo's .gitignore (committed with the adoption) keeps it out of
	// every worktree's porcelain.
	if err := os.WriteFile(filepath.Join(main, ".env"),
		[]byte("GITHUB_TOKEN=seed-token\n# unmanaged\n"), 0o644); err != nil {
		t.Fatalf("writing the main checkout .env: %v", err)
	}

	// The coordinator: a real server on a temp socket with the drivers
	// plain-app needs — no docker anywhere in this layer.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sock := shortSock(t, "adopt")
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
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.StatePath{}))
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
	t.Setenv("WT_SOCKET", sock)
	installFakeGh(t)
	// In a container the reaper is unavailable by design and the servers
	// must be killed manually; in a host run the reaper has already
	// stopped them. Either way nothing survives the test.
	t.Cleanup(func() {
		exec.Command("pkill", "-9", "-f", "plain-app-server").Run()
	})

	// The health hook's poll is sped up: the layer must not spend ten
	// seconds per retry when a server is a second away.
	healthPollInterval = 250 * time.Millisecond

	// Wait for the socket: the server goroutine may not have bound it yet.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("the coordinator never bound %s: %v", sock, err)
	}

	return &adoptionEnv{t: t, main: main, home: home, fixture: fixtureAbs(t), sock: sock, st: st, ctx: ctx}
}

// fixtureAbs is the absolute path of the plain-app fixture.
func fixtureAbs(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	if err != nil {
		t.Fatalf("resolving the fixture path: %v", err)
	}
	return abs
}

// copyFixtureTree copies plain-app into dst; withSpec=false removes the
// adopted surface — the spec, the generated artefacts, the decision record
// and the ignore file — leaving a repository that never adopted the
// tooling.
func copyFixtureTree(t *testing.T, dst string, withSpec bool) {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
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
		if fi.IsDir() {
			return os.MkdirAll(target, fi.Mode().Perm())
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, fi.Mode().Perm())
	}); err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
	if withSpec {
		return
	}
	for _, rel := range []string{
		"wt.yaml", "CLAUDE.md", ".gitignore",
		".claude/skills/worktree-create/SKILL.md",
		".claude/skills/worktree-remove/SKILL.md",
		".claude/hooks/wt-session-start.sh",
		".claude/hooks/wt-guard.sh",
		".claude/settings.json",
		"docs/wt.md", "docs/wt-decision-record.md",
	} {
		if err := os.RemoveAll(filepath.Join(dst, rel)); err != nil {
			t.Fatalf("removing %s: %v", rel, err)
		}
	}
}

// mkSharedSource writes one shared-source file, creating parents.
func mkSharedSource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// installFakeGh answers the no-PR contract the rm safety check reads.
func installFakeGh(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	body := "#!/bin/sh\necho \"no pull requests found for branch \\\"$(git branch --show-current)\\\"\" >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// worktree creates one linked worktree on a pushed branch.
func (e *adoptionEnv) worktree(slug string) string {
	e.t.Helper()
	wt := filepath.Join(filepath.Dir(e.main), slug)
	gitT(e.t, e.main, "worktree", "add", "-b", slug, wt, "main")
	gitT(e.t, e.main, "push", "-u", "origin", slug)
	return wt
}

// initWorktree runs the skill's phase-7 init and asserts success.
func (e *adoptionEnv) initWorktree(wt, slug string) {
	e.t.Helper()
	code, _, stderr := runCLI(e.t, "init", "--cwd", wt, "--description", "adoption worktree "+slug)
	if code != ExitOK {
		e.t.Fatalf("init of %s exit = %d; stderr:\n%s", slug, code, stderr)
	}
}

// startWorktree runs wt start (the bring-up hooks) and asserts success.
func (e *adoptionEnv) startWorktree(wt string) {
	e.t.Helper()
	code, _, stderr := runCLI(e.t, "start", "--cwd", wt)
	if code != ExitOK {
		e.t.Fatalf("start of %s exit = %d; stderr:\n%s", wt, code, stderr)
	}
}

// rmWorktree runs the skill's phase-7 teardown and returns the reap
// report, asserting the bounded-coverage contract: either the reaper
// signalled the worktree's own server (a host coordinator), or it says it
// could not and names the remedy.
func (e *adoptionEnv) rmWorktree(slug string) protocol.ReapReport {
	e.t.Helper()
	code, stdout, stderr := runCLI(e.t, "rm", "--cwd", e.main, "--slug", slug, "--json")
	if code != ExitOK {
		e.t.Fatalf("rm of %s exit = %d; stderr:\n%s", slug, code, stderr)
	}
	var res struct {
		Reap protocol.ReapReport `json:"reap"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		e.t.Fatalf("rm stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if !res.Reap.Available {
		// The design's container rule (04-lifecycle.md §6): a coordinator
		// inside a container cannot discover the host's processes, so it
		// says so and the remedy is manual. Asserted, never silently
		// passed.
		if !strings.Contains(res.Reap.Note, "kill any orphaned processes manually") &&
			!strings.Contains(res.Reap.Note, "container") {
			e.t.Errorf("the reap's unavailable note lacks the remedy:\n%+v", res.Reap)
		}
	}
	return res.Reap
}

// killOrphanServers stops whatever the reaper could not: in a container
// the reap is unavailable by design, and the design's remedy is manual
// killing. Only the fixture's own binary name is matched; a host run has
// nothing left to kill.
func (e *adoptionEnv) killOrphanServers() {
	e.t.Helper()
	out, err := exec.Command("pkill", "-f", "plain-app-server").CombinedOutput()
	_ = out
	if err == nil {
		// Give the TERM a moment, then KILL anything stubborn, then wait
		// for the processes to be gone.
		time.Sleep(300 * time.Millisecond)
		exec.Command("pkill", "-9", "-f", "plain-app-server").Run()
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !anyServerProcess() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	e.t.Errorf("the fixture's servers did not exit after the manual kill")
}

// anyServerProcess reports whether any plain-app-server process is alive.
func anyServerProcess() bool {
	err := exec.Command("pgrep", "-f", "plain-app-server").Run()
	return err == nil
}

// readDescriptor reads a worktree's descriptor.
func (e *adoptionEnv) readDescriptor(wt string) *descriptor.Descriptor {
	e.t.Helper()
	d, err := descriptor.Read(filepath.Join(wt, "wt-env.json"), "json")
	if err != nil {
		e.t.Fatalf("reading the descriptor of %s: %v", wt, err)
	}
	return d
}

// serverHealthy asks the worktree's server whether /healthz answers.
func (e *adoptionEnv) serverHealthy(wt string) bool {
	e.t.Helper()
	d := e.readDescriptor(wt)
	port, ok := d.Resources["api"].Value.(int)
	if !ok {
		e.t.Fatalf("api value of %s is %T, want int", wt, d.Resources["api"].Value)
	}
	out, err := exec.Command("sh", "-c",
		fmt.Sprintf("API_PORT=%d %s -healthcheck", port, filepath.Join(wt, "plain-app-server"))).CombinedOutput()
	if err != nil {
		return false
	}
	_ = out
	return true
}

// waitHealthy polls until the server answers or the deadline passes.
func (e *adoptionEnv) waitHealthy(wt string, deadline time.Time) bool {
	e.t.Helper()
	for time.Now().Before(deadline) {
		if e.serverHealthy(wt) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// TestAcceptancePlainAppAdoptedThroughTheSkill is exit criterion 1: the
// adopted fixture goes through the skill — bands, spec, generate, patch,
// prove — which proves state-path's three seed modes and the
// default:shared resource end to end, in two worktrees side by side.
func TestAcceptancePlainAppAdoptedThroughTheSkill(t *testing.T) {
	env := newAdoptionEnv(t, true)

	// Phase 1 — audit: what is listening is a fact, read through the
	// skill's primitive. Nothing here decides what the listeners mean.
	code, stdout, stderr := runCLI(t, "ports", "scan", "--json")
	if code != ExitOK {
		t.Fatalf("ports scan exit = %d; stderr: %s", code, stderr)
	}
	var scan protocol.PortsScanResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &scan); err != nil {
		t.Fatalf("ports scan stdout is not one JSON object: %v\n%s", err, stdout)
	}

	// Phase 3 — bands: suggest proposes where the base could sit; the
	// developer (this test) confirms the fixture's recorded base and
	// reserves it.
	chdir(t, env.main)
	code, stdout, stderr = runCLI(t, "bands", "suggest", "--json")
	if code != ExitOK {
		t.Fatalf("bands suggest exit = %d; stderr: %s", code, stderr)
	}
	var suggest protocol.SuggestBandResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &suggest); err != nil {
		t.Fatalf("bands suggest stdout: %v\n%s", err, stdout)
	}
	if len(suggest.Suggestions) != 1 || suggest.Suggestions[0].Resource != "api" {
		t.Fatalf("suggestions = %+v, want api", suggest.Suggestions)
	}
	code, _, stderr = runCLI(t, "bands", "reserve", "--base", fmt.Sprintf("api=%d", adoptionBand))
	if code != ExitOK {
		t.Fatalf("bands reserve exit = %d; stderr: %s", code, stderr)
	}

	// Phase 4 — spec: validate, then read the resource tables for two
	// slots and check them disjoint — the collision check before anything
	// is built.
	code, stdout, stderr = runCLI(t, "spec", "explain", "--slot", "1", "--slug", "wt-1",
		"--base", fmt.Sprintf("api=%d", adoptionBand), "--json")
	if code != ExitOK {
		t.Fatalf("spec explain 1 exit = %d; stderr: %s", code, stderr)
	}
	slot1 := explainTable(t, stdout)
	code, stdout, stderr = runCLI(t, "spec", "explain", "--slot", "2", "--slug", "wt-2",
		"--base", fmt.Sprintf("api=%d", adoptionBand), "--json")
	if code != ExitOK {
		t.Fatalf("spec explain 2 exit = %d; stderr: %s", code, stderr)
	}
	slot2 := explainTable(t, stdout)
	if slot1["api"] == slot2["api"] {
		t.Fatalf("slots 1 and 2 derive the same api port %v; the tables are not disjoint", slot1["api"])
	}

	// Phase 5 — generate, idempotently: the fixture already carries the
	// artefacts; regeneration must preserve a hand edit outside the block
	// (exit criterion 4, end to end) and refresh the block.
	skillPath := filepath.Join(env.main, filepath.FromSlash(artefact.SkillCreatePath))
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("the fixture's create skill is missing: %v", err)
	}
	edited := strings.Replace(string(data), "Never auto-stash, never auto-checkout, never\n  silently merge",
		"Never auto-stash, never auto-checkout, never\n  silently merge — and always ask before force-pushing", 1)
	if edited == string(data) {
		t.Fatal("the hand edit did not match the generated text")
	}
	if err := os.WriteFile(skillPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("writing the hand edit: %v", err)
	}
	sp := readFixtureSpec(t, env.main)
	files, err := artefact.Render(sp, map[string]int{"api": adoptionBand}, artefact.Options{GuardHook: true})
	if err != nil {
		t.Fatalf("rendering the artefacts: %v", err)
	}
	if err := artefact.Apply(env.main, files); err != nil {
		t.Fatalf("applying the artefacts: %v", err)
	}
	after, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("re-reading the skill: %v", err)
	}
	if !strings.Contains(string(after), "always ask before force-pushing") {
		t.Errorf("the hand edit did not survive regeneration")
	}
	gitT(t, env.main, "add", ".")
	gitT(t, env.main, "commit", "-m", "regenerate the artefacts")

	// Phase 6 — patch entry points: the serving entry point is in place.
	if _, err := os.Stat(filepath.Join(env.main, "bin", "server.go")); err != nil {
		t.Fatalf("bin/server.go is missing: %v", err)
	}

	// Phase 7 — prove it: two worktrees, both healthy at once, disjoint
	// tables, the seed modes verified end to end, then torn down with
	// doctor clean.
	wt1 := env.worktree("wt-1")
	wt2 := env.worktree("wt-2")
	env.initWorktree(wt1, "wt-1")
	env.initWorktree(wt2, "wt-2")
	env.startWorktree(wt1)
	env.startWorktree(wt2)

	d1 := env.readDescriptor(wt1)
	d2 := env.readDescriptor(wt2)
	if d1.Slot == d2.Slot {
		t.Fatalf("slots = %d and %d; two worktrees cannot share a slot", d1.Slot, d2.Slot)
	}
	api1 := d1.Resources["api"].Value.(int)
	api2 := d2.Resources["api"].Value.(int)
	if api1 == api2 {
		t.Fatalf("api ports = %d and %d; the tables are not disjoint", api1, api2)
	}
	// The derivation is base + slot: whatever slots the machine's other
	// listeners left free, the port is the base plus the slot, never
	// anything else.
	if api1 != adoptionBand+d1.Slot || api2 != adoptionBand+d2.Slot {
		t.Fatalf("api ports = %d (slot %d) and %d (slot %d), want base %d + slot",
			api1, d1.Slot, api2, d2.Slot, adoptionBand)
	}
	// Both healthy at the same time.
	deadline := time.Now().Add(30 * time.Second)
	if !env.waitHealthy(wt1, deadline) || !env.waitHealthy(wt2, deadline) {
		t.Fatalf("the two worktrees were not both healthy at once: wt-1 ok=%v wt-2 ok=%v",
			env.serverHealthy(wt1), env.serverHealthy(wt2))
	}

	// The seed modes, end to end:
	// - db: seeded — a snapshot of the shared source exists at the
	//   worktree's path with the source's content;
	// - cache: empty — the directory exists and the source's file was not
	//   copied;
	// - shared_db: shared by default — the SAME path in both worktrees,
	//   the shared store itself, and the descriptor says so.
	db1 := filepath.Join(env.home, ".plain-app", "worktrees", fmt.Sprintf("wt-1-%d", d1.Slot), "db.sqlite")
	dbContent, err := os.ReadFile(db1)
	if err != nil {
		t.Fatalf("the seeded db of wt-1 is missing: %v", err)
	}
	if string(dbContent) != "SHARED-DB-CONTENT-V1\n" {
		t.Errorf("the seeded db of wt-1 = %q, want the shared source's content", dbContent)
	}
	cache1 := filepath.Join(env.home, ".plain-app", "worktrees", fmt.Sprintf("wt-1-%d", d1.Slot), "cache")
	if fi, err := os.Stat(cache1); err != nil || !fi.IsDir() {
		t.Fatalf("the empty cache of wt-1 is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache1, "keep.txt")); err == nil {
		t.Errorf("the empty seed mode copied the source's file")
	}
	shared := filepath.Join(env.home, ".plain-app", "shared", "db.sqlite")
	if v, ok := d1.Resources["shared_db"].Value.(string); !ok || v != shared {
		t.Errorf("wt-1's shared_db = %v, want the shared store %s", d1.Resources["shared_db"].Value, shared)
	}
	if v, ok := d2.Resources["shared_db"].Value.(string); !ok || v != shared {
		t.Errorf("wt-2's shared_db = %v, want the same shared store %s", d2.Resources["shared_db"].Value, shared)
	}
	iso := d1.State["shared_db"]
	if iso == nil || iso.Isolated == nil || *iso.Isolated {
		t.Errorf("the descriptor's isolation state for shared_db = %+v, want isolated:false", iso)
	}
	sharedInBlock := false
	for _, s := range d1.Shared {
		if s.Name == shared && s.Impact != "" {
			sharedInBlock = true
		}
	}
	if !sharedInBlock {
		t.Errorf("the descriptor's shared block lacks shared_db with its blast radius: %+v", d1.Shared)
	}

	// The .env: seeded from the main checkout's unmanaged content
	// (GITHUB_TOKEN carries over), with the managed keys from this
	// worktree's own allocation.
	env1, err := os.ReadFile(filepath.Join(wt1, ".env"))
	if err != nil {
		t.Fatalf("reading wt-1's .env: %v", err)
	}
	envText := string(env1)
	if !strings.Contains(envText, "GITHUB_TOKEN=seed-token") {
		t.Errorf("the seeded .env lacks the unmanaged content: %s", envText)
	}
	if !strings.Contains(envText, fmt.Sprintf("API_PORT=%d", api1)) {
		t.Errorf("the .env's API_PORT is not wt-1's own allocation: %s", envText)
	}

	// The briefing renders from wt show --json values.
	code, stdout, stderr = runCLI(t, "show", "--json", "--cwd", wt1)
	if code != ExitOK {
		t.Fatalf("wt show exit = %d; stderr: %s", code, stderr)
	}
	briefing, err := artefact.Briefing(sp, []byte(stdout))
	if err != nil {
		t.Fatalf("the briefing refused to render with the descriptor present: %v", err)
	}
	for _, want := range []string{"wt-1", fmt.Sprintf("%d", api1), "shared_db", "--env"} {
		if !strings.Contains(briefing, want) {
			t.Errorf("the briefing lacks %q:\n%s", want, briefing)
		}
	}

	// Tear both down; the reap's bounded coverage is asserted (the
	// reaper's container rule makes manual killing the remedy here), and
	// doctor is clean afterwards.
	var reaps []protocol.ReapReport
	for _, slug := range []string{"wt-1", "wt-2"} {
		reaps = append(reaps, env.rmWorktree(slug))
	}
	reapWorked := false
	for _, r := range reaps {
		if r.Available && len(r.Signalled) > 0 {
			reapWorked = true
		}
	}
	if !reapWorked {
		env.killOrphanServers() // the design's remedy when the reaper is unavailable
	}
	if anyServerProcess() {
		t.Errorf("a worktree's server survived its rm")
	}
	code, stdout, stderr = runCLI(t, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d; stderr: %s", code, stderr)
	}
	var doc protocol.DoctorResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("doctor stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(doc.Findings) != 0 {
		t.Errorf("doctor after both rms reported findings:\n%s", docFindingsJSON(doc))
	}

	// Phase 8 — import: nothing pre-existed in this fixture; nothing to
	// import, and the bound is stated rather than silently skipped.
}

// TestAcceptanceSkillEightPhasesOnSpeclessFixture is exit criterion 2:
// the skill's eight phases run start to finish on a fixture that carries
// no spec yet, ending with two worktrees side by side.
func TestAcceptanceSkillEightPhasesOnSpeclessFixture(t *testing.T) {
	env := newAdoptionEnv(t, false)

	// The repository carries no spec: not adopted, and the verbs say so.
	if _, err := os.Stat(filepath.Join(env.main, "wt.yaml")); err == nil {
		t.Fatal("the specless fixture carries a spec")
	}
	chdir(t, env.main)
	code, _, stderr := runCLI(t, "spec", "validate")
	if code != ExitUnavailable {
		t.Fatalf("spec validate on the specless fixture exit = %d, want 4; stderr: %s", code, stderr)
	}

	// Phase 1 — audit: the scannable half. Committed default ports, $HOME
	// paths, the package-manager shape, what is listening.
	example, err := os.ReadFile(filepath.Join(env.main, ".env.example"))
	if err != nil {
		t.Fatalf("reading .env.example: %v", err)
	}
	if !strings.Contains(string(example), "API_PORT=8080") {
		t.Errorf("the audit found no committed default port: %s", example)
	}
	runSh, err := os.ReadFile(filepath.Join(env.main, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("reading bin/run.sh: %v", err)
	}
	if !strings.Contains(string(runSh), "API_PORT") {
		t.Errorf("the audit found no $HOME path source: %s", runSh)
	}
	code, _, stderr = runCLI(t, "ports", "scan", "--json")
	if code != ExitOK {
		t.Fatalf("ports scan exit = %d; stderr: %s", code, stderr)
	}

	// Phase 2 — policy: the decision record, committed beside the spec.
	record := filepath.Join(env.main, "docs", "wt-decision-record.md")
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		t.Fatalf("creating docs: %v", err)
	}
	if err := os.WriteFile(record, []byte(fixtureDecisionRecord(t, env.fixture)), 0o644); err != nil {
		t.Fatalf("writing the decision record: %v", err)
	}

	// Phase 3 — bands: the skill writes the spec as it drafts it (suggest
	// and reserve both read the spec for the required size), suggests
	// where the base could sit, the developer confirms, and the band is
	// reserved.
	sp := fixtureSpecFromDisk(t, env.fixture)
	draft, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the draft spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.main, "wt.yaml"), draft, 0o644); err != nil {
		t.Fatalf("writing the spec: %v", err)
	}
	code, stdout, stderr := runCLI(t, "bands", "suggest", "--json")
	if code != ExitOK {
		t.Fatalf("bands suggest exit = %d; stderr: %s", code, stderr)
	}
	var suggest protocol.SuggestBandResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &suggest); err != nil {
		t.Fatalf("bands suggest stdout: %v\n%s", err, stdout)
	}
	if len(suggest.Suggestions) != 1 || suggest.Suggestions[0].Resource != "api" {
		t.Fatalf("suggestions = %+v, want api", suggest.Suggestions)
	}
	code, _, stderr = runCLI(t, "bands", "reserve", "--base", fmt.Sprintf("api=%d", adoptionBand))
	if code != ExitOK {
		t.Fatalf("bands reserve exit = %d; stderr: %s", code, stderr)
	}

	// Phase 4 — spec: validate, then explain both slots and read the two
	// tables against the phase-1 inventory.
	code, _, stderr = runCLI(t, "spec", "validate")
	if code != ExitOK {
		t.Fatalf("spec validate exit = %d; stderr: %s", code, stderr)
	}
	code, stdout, stderr = runCLI(t, "spec", "explain", "--slot", "1", "--slug", "wt-1",
		"--base", fmt.Sprintf("api=%d", adoptionBand), "--json")
	if code != ExitOK {
		t.Fatalf("spec explain 1 exit = %d; stderr: %s", code, stderr)
	}
	slot1 := explainTable(t, stdout)
	code, stdout, stderr = runCLI(t, "spec", "explain", "--slot", "2", "--slug", "wt-2",
		"--base", fmt.Sprintf("api=%d", adoptionBand), "--json")
	if code != ExitOK {
		t.Fatalf("spec explain 2 exit = %d; stderr: %s", code, stderr)
	}
	slot2 := explainTable(t, stdout)
	if slot1["api"] == slot2["api"] {
		t.Fatalf("slots 1 and 2 derive the same api port %v; the tables are not disjoint", slot1["api"])
	}

	// Phase 5 — generate: the artefacts, the settings, the ignore line.
	files, err := artefact.Render(sp, map[string]int{"api": adoptionBand}, artefact.Options{GuardHook: true})
	if err != nil {
		t.Fatalf("rendering the artefacts: %v", err)
	}
	if err := artefact.Apply(env.main, files); err != nil {
		t.Fatalf("applying the artefacts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.main, ".gitignore"),
		[]byte("wt-env.json\n.env\nplain-app-server\nwt-server.log\n"), 0o644); err != nil {
		t.Fatalf("writing .gitignore: %v", err)
	}

	// Phase 6 — patch entry points: the serving entry point lands.
	serverSrc, err := os.ReadFile(filepath.Join(env.fixture, "bin", "server.go"))
	if err != nil {
		t.Fatalf("reading the fixture's server: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.main, "bin", "server.go"), serverSrc, 0o644); err != nil {
		t.Fatalf("writing bin/server.go: %v", err)
	}

	// The adopted surface is committed: from here the fixture is adopted
	// like any other.
	gitT(t, env.main, "add", ".")
	gitT(t, env.main, "commit", "-m", "adopt plain-app through the onboarding skill")

	// Phase 7 — prove it: two worktrees, both healthy at once, disjoint
	// tables, torn down, doctor clean.
	wt1 := env.worktree("wt-1")
	wt2 := env.worktree("wt-2")
	env.initWorktree(wt1, "wt-1")
	env.initWorktree(wt2, "wt-2")
	env.startWorktree(wt1)
	env.startWorktree(wt2)

	d1 := env.readDescriptor(wt1)
	d2 := env.readDescriptor(wt2)
	if d1.Slot == d2.Slot {
		t.Fatalf("slots = %d and %d; two worktrees cannot share a slot", d1.Slot, d2.Slot)
	}
	api1 := d1.Resources["api"].Value.(int)
	api2 := d2.Resources["api"].Value.(int)
	if api1 == api2 {
		t.Fatalf("api ports = %d and %d; the tables are not disjoint", api1, api2)
	}
	deadline := time.Now().Add(30 * time.Second)
	if !env.waitHealthy(wt1, deadline) || !env.waitHealthy(wt2, deadline) {
		t.Fatalf("the two worktrees were not both healthy at once")
	}
	// The seed modes hold end to end here too: the skill-written spec is
	// the fixture's spec, and the shared sources are the stage's.
	db1 := filepath.Join(env.home, ".plain-app", "worktrees", fmt.Sprintf("wt-1-%d", d1.Slot), "db.sqlite")
	if content, err := os.ReadFile(db1); err != nil || string(content) != "SHARED-DB-CONTENT-V1\n" {
		t.Errorf("the seeded db of wt-1 = %q err=%v, want the shared source's content", content, err)
	}

	for _, slug := range []string{"wt-1", "wt-2"} {
		reap := env.rmWorktree(slug)
		if reap.Available && len(reap.Signalled) == 0 && reap.Note != "" {
			t.Errorf("the reap reported a host limitation without the remedy:\n%+v", reap)
		}
	}
	if anyServerProcess() {
		env.killOrphanServers()
	}
	code, stdout, stderr = runCLI(t, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d; stderr: %s", code, stderr)
	}
	var doc protocol.DoctorResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("doctor stdout: %v", err)
	}
	if len(doc.Findings) != 0 {
		t.Errorf("doctor after both rms reported findings:\n%s", docFindingsJSON(doc))
	}

	// Phase 8 — import what already exists: this repository had no
	// worktrees and no per-repo implementation before adoption; nothing to
	// import, and the skip is stated rather than silent.
}

// explainTable decodes `wt spec explain --json` and returns the value per
// resource name.
func explainTable(t *testing.T, stdout string) map[string]string {
	t.Helper()
	var out struct {
		Resources map[string]spec.Resolved `json:"resources"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
		t.Fatalf("explain stdout is not one JSON object: %v\n%s", err, stdout)
	}
	table := map[string]string{}
	for name, r := range out.Resources {
		table[name] = fmt.Sprint(r.Value)
	}
	return table
}

// readFixtureSpec parses the committed spec of a repo copy.
func readFixtureSpec(t *testing.T, repoRoot string) *spec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "wt.yaml"))
	if err != nil {
		t.Fatalf("reading the spec: %v", err)
	}
	sp, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("parsing the spec: %v", err)
	}
	if err := spec.Validate(sp); err != nil {
		t.Fatalf("the spec does not validate: %v", err)
	}
	return sp
}

// fixtureSpecFromDisk parses the fixture's committed spec — the spec the
// skill writes during phase 4 of the specless flow.
func fixtureSpecFromDisk(t *testing.T, fixtureRoot string) *spec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureRoot, "wt.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture spec: %v", err)
	}
	sp, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("parsing the fixture spec: %v", err)
	}
	return sp
}

// fixtureDecisionRecord is the phase-2 output the skill writes.
func fixtureDecisionRecord(t *testing.T, fixtureRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureRoot, "docs", "wt-decision-record.md"))
	if err != nil {
		t.Fatalf("reading the fixture's decision record: %v", err)
	}
	return string(data)
}

// docFindingsJSON renders a doctor result for a failure message.
func docFindingsJSON(doc protocol.DoctorResult) string {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Sprint(doc)
	}
	return string(data)
}

// keep the managed import referenced (the block is the fixture's contract).
