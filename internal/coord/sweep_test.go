package coord

// sweep_test.go exercises the coordinator's scheduled cleanup sweep
// (06-fleet.md §7.2): gh unavailable cleans nothing and logs the skip, a
// merged-and-clean entry owned by the coordinator's own host user is
// cleaned (teardown plus git worktree remove), and every skip — foreign
// owner, unverifiable path, dirty tree, unmerged PR — is logged with its
// reason. The sweep never touches another client's entries: that is the
// "more conservative than the interactive verb" half.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/driver"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
	"github.com/tinkerindustries/worktree-manager/internal/store"
)

// sweepSpec is the sweep fixture's committed spec: a port only, so the
// teardown is clean with no daemon.
func sweepSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "sweep-app",
		Slots:     spec.Slots{Max: &max},
		Resources: []spec.Resource{{Type: "port", Name: "api"}},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("spec: %v", err)
	}
	return s
}

// sweepRepo builds a git repository with one linked worktree on a pushed
// branch with an upstream, and the spec committed — the walk-up source the
// sweep's teardown needs.
func sweepRepo(t *testing.T, sp *spec.Spec) (main, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	remote := filepath.Join(base, "remote.git")
	gitTC(t, "", "init", "--bare", remote)
	gitTC(t, "", "init", "-b", "main", main)
	gitTC(t, main, "config", "user.email", "t@example.com")
	gitTC(t, main, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(main, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTC(t, main, "add", ".")
	gitTC(t, main, "commit", "-m", "initial")
	gitTC(t, main, "remote", "add", "origin", remote)
	gitTC(t, main, "push", "-u", "origin", "main")
	worktree = filepath.Join(base, "wt-1")
	gitTC(t, main, "worktree", "add", "-b", "wt-1", worktree, "main")
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTC(t, worktree, "add", ".")
	gitTC(t, worktree, "commit", "-m", "feature")
	gitTC(t, worktree, "push", "-u", "origin", "wt-1")
	return main, worktree
}

// gitTC is the coord tests' git runner.
func gitTC(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", args, strings.TrimSpace(string(out)))
	}
}

// sweepHarness builds the harness with a capturing log and the driver
// registry installed, plus the band registered for the app.
func sweepHarness(t *testing.T, sp *spec.Spec) (*Harness, *bytes.Buffer) {
	t.Helper()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	root := filepath.Join(tempRoot(t), "wt")
	st, err := store.Open(root)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	// The store owns an open SQLite handle; leaving it open leaks it on
	// every platform and, on Windows, stops t.TempDir() removing its own
	// directory. NewHarness does this for the harnesses it builds; this
	// one builds its own, for the capturing log.
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store at %s: %v", root, err)
		}
	})
	h, err := NewHandler(st, log)
	if err != nil {
		t.Fatalf("building the handler: %v", err)
	}
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}))
	harness := &Harness{Store: st, H: h}
	sess, cerr := harness.ConnectPeer(os.Getuid(), api.KindHost, "")
	if cerr != nil {
		t.Fatalf("connect refused: %+v", cerr)
	}
	if resp := h.Handle(context.Background(), sess, &api.Request{
		Verb: "bands.reserve",
		Args: mustJSON(&api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 7500}}),
	}); resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}
	return harness, &logBuf
}

// fakeSweepGh answers auth status and merged PRs; the merged answer can be
// switched per test.
func fakeSweepGh(t *testing.T, h *Harness, merged func() string) {
	t.Helper()
	h.H.Gh = func(dir string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
			return []byte("logged in"), nil
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
			return []byte(merged()), nil
		}
		return nil, fmt.Errorf("fake gh: unexpected call %v", args)
	}
}

// sweepEntry allocates one entry for the sweep app.
func sweepEntry(t *testing.T, h *Harness, sp *spec.Spec, slug, path string) {
	t.Helper()
	resp := h.Request(context.Background(), mustSession(t, h), verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: slug, Path: path,
		DescriptorPath: filepath.Join(path, "wt-env.yaml"), Description: "a sweep worktree",
	})
	if resp.Error != nil {
		t.Fatalf("allocating %s: %v", slug, resp.Error)
	}
}

// mustSession connects the host user and returns the session.
func mustSession(t *testing.T, h *Harness) *Session {
	t.Helper()
	sess, cerr := h.ConnectPeer(os.Getuid(), api.KindHost, "")
	if cerr != nil {
		t.Fatalf("connect refused: %+v", cerr)
	}
	return sess
}

// TestSweepCleanupGhUnavailableCleansNothing: a sweep without gh (missing
// or unauthenticated) cleans nothing and logs the skip — the resident
// process's version of the interactive verb's exit 4.
func TestSweepCleanupGhUnavailableCleansNothing(t *testing.T) {
	sp := sweepSpec(t)
	_, worktree := sweepRepo(t, sp)
	h, logBuf := sweepHarness(t, sp)
	sweepEntry(t, h, sp, "wt-1", worktree)
	h.H.Gh = func(string, ...string) ([]byte, error) {
		return []byte("gh: not logged in"), fmt.Errorf("gh auth status failed")
	}

	cleaned, rerr := h.H.SweepCleanup()
	if rerr != nil {
		t.Fatalf("SweepCleanup: %v", rerr)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0 with gh unavailable", cleaned)
	}
	if _, serr := os.Stat(worktree); serr != nil {
		t.Fatalf("the sweep must clean nothing with gh unavailable")
	}
	if !strings.Contains(logBuf.String(), "gh is unavailable") {
		t.Errorf("the skip must be logged: %s", logBuf.String())
	}
}

// TestSweepCleanupCleansMergedCleanOwnEntry: the sweep's happy path — the
// coordinator's own host user's entry, merged PR, clean pushed branch —
// is torn down by handle and the worktree removed, exactly like the
// interactive verb but without a client.
func TestSweepCleanupCleansMergedCleanOwnEntry(t *testing.T) {
	sp := sweepSpec(t)
	_, worktree := sweepRepo(t, sp)
	h, logBuf := sweepHarness(t, sp)
	sweepEntry(t, h, sp, "wt-1", worktree)
	fakeSweepGh(t, h, func() string { return `{"number":7,"state":"MERGED"}` })

	cleaned, rerr := h.H.SweepCleanup()
	if rerr != nil {
		t.Fatalf("SweepCleanup: %v", rerr)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned = %d, want 1", cleaned)
	}
	if _, serr := os.Stat(worktree); !os.IsNotExist(serr) {
		t.Errorf("the worktree must be removed: %s still exists", worktree)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("the registry must be empty after the sweep: %+v", reg.Entries)
	}
	if !strings.Contains(logBuf.String(), "cleaned an entry") {
		t.Errorf("the cleaned decision must be logged: %s", logBuf.String())
	}
}

// TestSweepCleanupSkipsEverySkipLogged: the sweep's rails — foreign
// owner, unverifiable path, dirty tree, unmerged PR — each skipped with
// the reason logged. The interactive verb would clean a reclaimable
// entry; the sweep never adopts a view.
func TestSweepCleanupSkipsEverySkipLogged(t *testing.T) {
	sp := sweepSpec(t)
	_, worktree := sweepRepo(t, sp)
	h, logBuf := sweepHarness(t, sp)

	// The entry of another host client (a different uid).
	other, _ := h.ConnectPeer(4242, api.KindHost, "")
	if otherReply := h.Request(context.Background(), other, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: "wt-foreign", Path: worktree,
		DescriptorPath: filepath.Join(worktree, "wt-env.yaml"), Description: "someone else's",
	}); otherReply.Error != nil {
		t.Fatalf("allocating the foreign entry: %v", otherReply.Error)
	}
	// The unverifiable entry: a path the coordinator cannot stat.
	sweepEntry(t, h, sp, "wt-uv", "/container/wt-uv")
	// The dirty entry: an uncommitted change on top of a merged PR.
	_, dirtyTree := sweepRepo(t, sp)
	sweepEntry(t, h, sp, "wt-dirty", dirtyTree)
	if err := os.WriteFile(filepath.Join(dirtyTree, "uncommitted.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The unmerged entry.
	_, openTree := sweepRepo(t, sp)
	sweepEntry(t, h, sp, "wt-open", openTree)

	fakeSweepGh(t, h, func() string { return `{"number":9,"state":"OPEN"}` })
	// The dirty tree's PR would be merged if gh were asked per branch; the
	// fake answers the same for every tree, which still exercises the
	// dirty skip — the merged-PR question never gets that far.
	cleaned, rerr := h.H.SweepCleanup()
	if rerr != nil {
		t.Fatalf("SweepCleanup: %v", rerr)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0: every candidate must be skipped", cleaned)
	}
	log := logBuf.String()
	for _, want := range []string{"owned by another client", "unverifiable", "uncommitted", "not merged"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log must state the %q skip:\n%s", want, log)
		}
	}
	// Nothing was destroyed: the foreign and dirty and open trees survive.
	for _, p := range []string{worktree, dirtyTree, openTree} {
		if _, serr := os.Stat(p); serr != nil {
			t.Errorf("the sweep must not touch %s", p)
		}
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 4 {
		t.Errorf("all four entries must survive the skip sweep: %+v", reg.Entries)
	}
}

// TestSweepCleanupForeignEphemeralNeverAdopted: an aged-out ephemeral
// entry is reclaimable for the interactive verb; the sweep never adopts a
// view and leaves it alone, logging the skip.
func TestSweepCleanupForeignEphemeralNeverAdopted(t *testing.T) {
	sp := sweepSpec(t)
	_, worktree := sweepRepo(t, sp)
	h, logBuf := sweepHarness(t, sp)
	eph, err := h.Connect(api.KindEphemeral, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	if resp := h.Request(context.Background(), eph, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: "wt-eph", Path: worktree,
		DescriptorPath: filepath.Join(worktree, "wt-env.yaml"), Description: "a dead container's",
	}); resp.Error != nil {
		t.Fatalf("allocating the ephemeral entry: %v", resp.Error)
	}
	// Age the owner out: reclamation would take this entry by handle.
	h.H.ReclaimInterval = 0
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range reg.Entries {
		e.LastSeen = "2000-01-01T00:00:00Z"
		if err := h.Store.UpsertEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	fakeSweepGh(t, h, func() string { return `{"number":7,"state":"MERGED"}` })

	cleaned, rerr := h.H.SweepCleanup()
	if rerr != nil {
		t.Fatalf("SweepCleanup: %v", rerr)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0: the sweep never adopts a view", cleaned)
	}
	if !strings.Contains(logBuf.String(), "owned by another client") {
		t.Errorf("the ephemeral skip must be logged: %s", logBuf.String())
	}
	if _, serr := os.Stat(worktree); serr != nil {
		t.Errorf("the ephemeral entry's tree must survive the sweep")
	}
}
