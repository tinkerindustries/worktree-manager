package cli

// cleanup_test.go exercises `wt cleanup` (06-fleet.md §7) end to end
// against a real in-process coordinator and real git worktrees, with gh
// faked on PATH: the dry-run previewing exactly what the real run does,
// gh missing or unauthenticated cleaning nothing and exiting 4, the full
// rm safety checks applying even when the PR is merged, and the
// unverifiable skip. Exit criterion 2 of phase 8.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/coord"
	"github.com/tinkerindustries/worktree-manager/internal/driver"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// cleanupSpec is the cleanup fixture's committed spec: a port only — no
// hooks, no docker, and a port has no teardown, so the cleanup teardown is
// clean with no daemon, exactly like the reconcile end-to-end test.
func cleanupSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "cleanup-app",
		Slots:     spec.Slots{Max: &max},
		Resources: []spec.Resource{{Type: "port", Name: "api"}},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("spec: %v", err)
	}
	return s
}

// cleanupFixture builds a git repository with one linked worktree on a
// pushed branch with an upstream: clean tree, nothing unpushed, and the
// merged-PR question is the only gate left.
func cleanupFixture(t *testing.T, sp *spec.Spec) (main, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	remote := filepath.Join(base, "remote.git")
	gitT(t, "", "init", "--bare", remote)
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	writeT(t, filepath.Join(main, "file.txt"), "one\n")
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	writeT(t, filepath.Join(main, "wt.yaml"), string(data))
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "initial")
	gitT(t, main, "remote", "add", "origin", remote)
	gitT(t, main, "push", "-u", "origin", "main")
	worktree = filepath.Join(base, "wt-1")
	gitT(t, main, "worktree", "add", "-b", "wt-1", worktree, "main")
	writeT(t, filepath.Join(worktree, "feature.txt"), "feature\n")
	gitT(t, worktree, "add", ".")
	gitT(t, worktree, "commit", "-m", "feature")
	gitT(t, worktree, "push", "-u", "origin", "wt-1")
	return main, worktree
}

// cleanupCoord starts the real coordinator on a loopback port with the
// band registered, and returns the store root the client reads
// endpoint.json from.
func cleanupCoord(t *testing.T, sp *spec.Spec) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	storeRoot := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		t.Fatalf("store root: %v", err)
	}
	st := openTestStore(t, storeRoot)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := coord.NewHandler(st, log)
	if err != nil {
		t.Fatalf("building the coordinator: %v", err)
	}
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	srv := coord.NewServer(h, log)
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, "127.0.0.1:0") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		default:
		}
	})
	t.Setenv("WT_HOME", storeRoot)
	waitEndpoint(t, storeRoot)
	sess := &coord.Session{Version: api.VersionMax, Identity: coord.Identity{Kind: api.KindHost, Key: "4242"}}
	if resp := h.Handle(context.Background(), sess, &api.Request{
		Verb: "bands.reserve",
		Args: mustJSONT(&api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 7400}}),
	}); resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}
	return storeRoot
}

// mergedGh is the fake gh for the happy path: authenticated, and every PR
// merged.
func mergedGh(t *testing.T) {
	t.Helper()
	fakeGh(t, `if [ "$1" = "auth" ]; then exit 0; fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then echo '{"number":7,"state":"MERGED"}'; exit 0; fi
echo "gh: unexpected call: $*" >&2; exit 1`)
}

// stripGh gives the test a PATH with no gh on it, the "gh missing" shape.
func stripGh(t *testing.T) {
	t.Helper()
	setPathWithoutGh(t)
}

// cleanupRows decodes the --json output's cleanup rows.
func cleanupRows(t *testing.T, stdout string) []cleanupRow {
	t.Helper()
	var doc struct {
		DryRun  bool         `json:"dry_run"`
		Cleanup []cleanupRow `json:"cleanup"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("stdout is not the cleanup JSON: %v\n%s", err, stdout)
	}
	return doc.Cleanup
}

// TestRunCleanupDryRunPreviewsExactlyWhatTheRealRunDoes is exit criterion
// 2's preview half: the dry run names the same entry the real run cleans —
// merged PR, clean tree, pushed branch — and changes nothing; the real
// run then tears the entry down, removes the worktree, and leaves the
// registry empty.
func TestRunCleanupDryRunPreviewsExactlyWhatTheRealRunDoes(t *testing.T) {
	sp := cleanupSpec(t)
	main, worktree := cleanupFixture(t, sp)
	_ = cleanupCoord(t, sp)
	mergedGh(t)

	// Init through the real client, so the registry holds the entry.
	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the cleanup worktree")
	if code != ExitOK {
		t.Fatalf("init exit = %d; stderr: %s", code, stderr)
	}

	// The dry run: previews the cleanup and changes nothing.
	code, stdout, stderr := runCLI(t, "cleanup", "--cwd", main, "--dry-run", "--json")
	if code != ExitOK {
		t.Fatalf("cleanup --dry-run exit = %d; stderr: %s", code, stderr)
	}
	rows := cleanupRows(t, stdout)
	if len(rows) != 1 || rows[0].Slug != "wt-1" || rows[0].Action != "would-clean" {
		t.Fatalf("dry-run rows = %+v, want exactly wt-1 would-clean", rows)
	}
	for _, want := range []string{"merged", "tear down", "git worktree remove"} {
		if !strings.Contains(rows[0].Detail, want) {
			t.Errorf("the preview must name %q: %s", want, rows[0].Detail)
		}
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("the dry run must change nothing: the worktree is gone")
	}
	code, stdout, _ = runCLI(t, "list", "--json")
	if code != ExitOK || !strings.Contains(stdout, "wt-1") {
		t.Fatalf("the dry run must leave the entry: list = %s", stdout)
	}

	// The real run: the same entry, cleaned.
	code, stdout, stderr = runCLI(t, "cleanup", "--cwd", main, "--json")
	if code != ExitOK {
		t.Fatalf("cleanup exit = %d; stderr: %s", code, stderr)
	}
	rows = cleanupRows(t, stdout)
	if len(rows) != 1 || rows[0].Slug != "wt-1" || rows[0].Action != "cleaned" {
		t.Fatalf("real-run rows = %+v, want wt-1 cleaned", rows)
	}
	if !strings.Contains(rows[0].Detail, "git worktree remove ran") {
		t.Errorf("the cleaned row must report the git half: %s", rows[0].Detail)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the worktree directory must be gone after the real run")
	}
	code, stdout, _ = runCLI(t, "list", "--json")
	if code != ExitOK || strings.Contains(stdout, "wt-1") {
		t.Errorf("the registry must be empty after the real run: %s", stdout)
	}
}

// TestRunCleanupGhMissingCleansNothingExits4 is exit criterion 2's
// fail-closed half: with gh absent, cleanup cleans nothing and exits 4 —
// guessing at merge status is how a sweep deletes work (06-fleet.md
// §7.1).
func TestRunCleanupGhMissingCleansNothingExits4(t *testing.T) {
	sp := cleanupSpec(t)
	main, worktree := cleanupFixture(t, sp)
	_ = cleanupCoord(t, sp)
	stripGh(t)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the orphaned worktree")
	if code != ExitOK {
		t.Fatalf("init exit = %d; stderr: %s", code, stderr)
	}

	code, stdout, stderr := runCLI(t, "cleanup", "--cwd", main)
	if code != ExitUnavailable {
		t.Fatalf("cleanup with gh missing exit = %d, want 4; stdout: %s; stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "gh") {
		t.Errorf("the refusal must name gh: %s", stderr)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("cleanup with gh missing must clean nothing: the worktree is gone")
	}
	code, stdout, _ = runCLI(t, "list", "--json")
	if code != ExitOK || !strings.Contains(stdout, "wt-1") {
		t.Errorf("cleanup with gh missing must clean nothing: the entry is gone (%s)", stdout)
	}
}

// TestRunCleanupGhUnauthenticatedCleansNothingExits4: an unauthenticated
// gh is the same fail-closed gate as a missing one.
func TestRunCleanupGhUnauthenticatedCleansNothingExits4(t *testing.T) {
	sp := cleanupSpec(t)
	main, worktree := cleanupFixture(t, sp)
	_ = cleanupCoord(t, sp)
	fakeGh(t, `echo "gh: To get started with GitHub CLI, please run: gh auth login" >&2; exit 4`)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the orphaned worktree")
	if code != ExitOK {
		t.Fatalf("init exit = %d; stderr: %s", code, stderr)
	}

	code, _, stderr = runCLI(t, "cleanup", "--cwd", main)
	if code != ExitUnavailable {
		t.Fatalf("cleanup with gh unauthenticated exit = %d, want 4; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "authenticated") {
		t.Errorf("the refusal must say gh is not authenticated: %s", stderr)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("cleanup with gh unauthenticated must clean nothing: the worktree is gone")
	}
}

// TestRunCleanupSkipsAndAppliesFullChecksEvenWhenMerged is the rail
// table: a merged PR says the branch landed and says nothing about whether
// the tree is clean, so a dirty tree is skipped even with a merged PR; an
// open PR, an unpushed branch and an unverifiable entry are each skipped
// with the reason named. The fake coordinator serves the canned list; the
// decisions are the client's.
func TestRunCleanupSkipsAndAppliesFullChecksEvenWhenMerged(t *testing.T) {
	sp := cleanupSpec(t)

	// One real repo per shape, each with a real worktree on a pushed
	// branch, so git answers truthfully. The worktree directory is moved
	// to a path whose basename is the slug — the fake gh answers by cwd,
	// exactly as the real gh answers per repository.
	type shape struct {
		slug string
		dir  string
	}
	var shapes []shape
	mk := func(slug string) string {
		main, wt := cleanupFixture(t, sp)
		moved := filepath.Join(filepath.Dir(wt), slug)
		gitT(t, main, "worktree", "move", wt, moved)
		gitT(t, moved, "branch", "-m", slug)
		return moved
	}
	dirty := mk("wt-dirty")
	open := mk("wt-open")
	clean := mk("wt-clean")

	// wt-dirty: an uncommitted change on top of a merged PR's branch.
	writeT(t, filepath.Join(dirty, "uncommitted.txt"), "dirty\n")
	// wt-open: nothing uncommitted; the fake gh answers an open PR.
	shapes = []shape{{"wt-dirty", dirty}, {"wt-open", open}, {"wt-clean", clean}}

	// The fake gh: authenticated; per-cwd answers — merged for wt-dirty
	// and wt-clean, open for wt-open.
	fakeGh(t, `if [ "$1" = "auth" ]; then exit 0; fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  case "$(basename "$PWD")" in
    wt-open) echo '{"number":3,"state":"OPEN"}'; exit 0;;
    *) echo '{"number":7,"state":"MERGED"}'; exit 0;;
  esac
fi
echo "gh: unexpected call: $*" >&2; exit 1`)

	entries := []api.ListEntry{}
	for _, sh := range shapes {
		entries = append(entries, api.ListEntry{
			App: sp.App, Slug: sh.slug, Slot: 1, State: "active",
			Path: sh.dir, PathVisible: true,
			Owner: "4242", OwnerKind: "host",
		})
	}
	// An unverifiable entry: a container path the coordinator cannot stat.
	entries = append(entries, api.ListEntry{
		App: sp.App, Slug: "wt-uv", Slot: 2, State: "active",
		Path: "/container/wt-uv", PathVisible: false,
		Owner: "s99", OwnerKind: "ephemeral", Ephemeral: true,
		Flags: []string{"unverifiable", "foreign"},
	})
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"list": cannedT(&api.ListResult{Entries: entries}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "cleanup", "--cwd", clean, "--dry-run", "--json")
	if code != ExitOK {
		t.Fatalf("cleanup --dry-run exit = %d; stderr: %s", code, stderr)
	}
	rows := cleanupRows(t, stdout)
	bySlug := map[string]cleanupRow{}
	for _, r := range rows {
		bySlug[r.Slug] = r
	}

	if r := bySlug["wt-dirty"]; r.Action != "skipped" || !strings.Contains(r.Detail, "uncommitted") {
		t.Errorf("wt-dirty (merged PR, dirty tree) = %+v, want skipped naming the uncommitted changes — a merged PR says nothing about whether the tree is clean", r)
	}
	if r := bySlug["wt-open"]; r.Action != "skipped" || !strings.Contains(r.Detail, "not merged") {
		t.Errorf("wt-open = %+v, want skipped naming the open PR", r)
	}
	if r := bySlug["wt-clean"]; r.Action != "would-clean" {
		t.Errorf("wt-clean = %+v, want would-clean", r)
	}
	// The unverifiable entry never reaches the decision loop: the
	// candidate filter skips it, and the skip is stated on stderr —
	// cleanup never touches an entry the coordinator cannot stat.
	if !strings.Contains(stderr, "wt-uv") || !strings.Contains(stderr, "unverifiable") {
		t.Errorf("the unverifiable skip must be stated: %s", stderr)
	}

	// Nothing may have changed: the dry run touched no tree.
	for _, sh := range shapes {
		if _, err := os.Stat(sh.dir); err != nil {
			t.Errorf("the dry run must change nothing: %s is gone", sh.dir)
		}
	}
}

// TestRunCleanupSkipsUnpushedBranch: a branch with commits that never
// reached its upstream is skipped — an absent or behind upstream is itself
// a stop, exactly as in rm.
func TestRunCleanupSkipsUnpushedBranch(t *testing.T) {
	sp := cleanupSpec(t)
	_, worktree := cleanupFixture(t, sp)
	writeT(t, filepath.Join(worktree, "later.txt"), "later\n")
	gitT(t, worktree, "add", ".")
	gitT(t, worktree, "commit", "-m", "not pushed")

	entries := []api.ListEntry{{
		App: sp.App, Slug: "wt-1", Slot: 1, State: "active",
		Path: worktree, PathVisible: true, Owner: "4242", OwnerKind: "host",
	}}
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"list": cannedT(&api.ListResult{Entries: entries}),
	})
	t.Setenv("WT_ENDPOINT", ep)
	mergedGh(t)

	code, stdout, _ := runCLI(t, "cleanup", "--cwd", worktree, "--dry-run", "--json")
	if code != ExitOK {
		t.Fatalf("cleanup exit = %d", code)
	}
	rows := cleanupRows(t, stdout)
	if len(rows) != 1 || rows[0].Action != "skipped" || !strings.Contains(rows[0].Detail, "unpushed") {
		t.Fatalf("rows = %+v, want the unpushed skip", rows)
	}
}
