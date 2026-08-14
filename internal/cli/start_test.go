package cli

// start_test.go exercises `wt start`: the start/seed/health hooks from the
// descriptor, with no allocation, no emission and no coordinator.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// startFixture builds an adopted repo with one worktree and writes a
// descriptor into it, so start has everything it needs.
func startFixture(t *testing.T) (worktree string, sp *spec.Spec) {
	t.Helper()
	sp = lifecycleSpec(t)
	_, worktree = lifecycleFixture(t, sp)
	d := &descriptor.Descriptor{
		Version: descriptor.Version, App: sp.App, Slug: "wt-1", Slot: 1,
		Path: worktree, Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 4201},
			"db":  {Type: "state-path", Value: "/home/test/.lifecycle-app/wt-1-1/db.sqlite"},
		},
		Extras: map[string]any{},
	}
	if err := descriptor.Write(filepath.Join(worktree, "wt-env.yaml"), "yaml", d); err != nil {
		t.Fatalf("writing the descriptor: %v", err)
	}
	return worktree, sp
}

// TestStartRunsTheBringUpHooksFromTheDescriptor: start runs start, seed
// and health, in order, with cwd at the worktree root.
func TestStartRunsTheBringUpHooksFromTheDescriptor(t *testing.T) {
	worktree, sp := startFixture(t)
	sp.Hooks.Start.Run = "touch started-marker"
	sp.Hooks.Seed.Run = "touch seeded-marker"
	writeT(t, filepath.Join(worktree, "wt.yaml"), string(mustYAML(t, sp)))

	code, stdout, stderr := runCLI(t, "start", "--cwd", worktree)
	if code != ExitOK {
		t.Fatalf("start exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "started lifecycle-app/wt-1") {
		t.Errorf("stdout = %q, want the started statement", stdout)
	}
	for _, f := range []string{"started-marker", "seeded-marker"} {
		if _, err := os.Stat(filepath.Join(worktree, f)); err != nil {
			t.Errorf("%s not created: the hook did not run in the worktree", f)
		}
	}
}

// TestStartRefusesWithoutDescriptor: a linked worktree with no descriptor
// is exit 4 naming wt init — the same refusal the generated reader makes.
func TestStartRefusesWithoutDescriptor(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	code, _, stderr := runCLI(t, "start", "--cwd", worktree)
	if code != ExitUnavailable {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "wt init") {
		t.Errorf("stderr = %q, want wt init named", stderr)
	}
}

// TestStartNeverContactsTheCoordinator: start works with the coordinator
// unreachable — it dials nothing (ARCHITECTURE.md §6.1: start's
// coordinator column is "no").
func TestStartNeverContactsTheCoordinator(t *testing.T) {
	worktree, _ := startFixture(t)
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	code, _, stderr := runCLI(t, "start", "--cwd", worktree)
	if code != ExitOK {
		t.Fatalf("start exit = %d with no coordinator; stderr: %s", code, stderr)
	}
}

// TestStartHealthFailureSaysWhatFailed: a failing health hook stops start
// with exit 1 and names the failure; the worktree stays allocated and
// usable (nothing is released — start never touches the entry).
func TestStartHealthFailureSaysWhatFailed(t *testing.T) {
	old := healthPollInterval
	healthPollInterval = 50 * time.Millisecond
	defer func() { healthPollInterval = old }()

	worktree, sp := startFixture(t)
	sp.Hooks.Health = &spec.Hook{Run: "echo not-ready; exit 1", Timeout: "300ms"}
	writeT(t, filepath.Join(worktree, "wt.yaml"), string(mustYAML(t, sp)))

	code, _, stderr := runCLI(t, "start", "--cwd", worktree)
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, "timed out after 300ms") {
		t.Errorf("stderr = %q, want the timeout and the failure named", stderr)
	}
}

// TestStartDryRunPrintsTheResolvedCommands: --dry-run prints the resolved
// commands and runs nothing.
func TestStartDryRunPrintsTheResolvedCommands(t *testing.T) {
	worktree, sp := startFixture(t)
	sp.Hooks.Start.Run = "touch must-not-exist"
	writeT(t, filepath.Join(worktree, "wt.yaml"), string(mustYAML(t, sp)))

	code, _, stderr := runCLI(t, "start", "--cwd", worktree, "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "would run: touch must-not-exist") {
		t.Errorf("stderr = %q, want the resolved commands", stderr)
	}
	if _, err := os.Stat(filepath.Join(worktree, "must-not-exist")); err == nil {
		t.Error("--dry-run executed the hook")
	}
}

// TestStartPersistsChosenStickyParams: a sticky parameter chosen on first
// run is written back into the descriptor, so later runs reuse it.
func TestStartPersistsChosenStickyParams(t *testing.T) {
	worktree, sp := startFixture(t)
	sp.Hooks.Seed = &spec.Hook{
		Run:    "echo seeding {seed_profile}",
		Params: map[string]spec.Param{"seed_profile": {Sticky: true, Default: "dev"}},
	}
	writeT(t, filepath.Join(worktree, "wt.yaml"), string(mustYAML(t, sp)))

	code, _, stderr := runCLI(t, "start", "--cwd", worktree)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	d, err := descriptor.Read(filepath.Join(worktree, "wt-env.yaml"), "yaml")
	if err != nil {
		t.Fatalf("reading the descriptor: %v", err)
	}
	if got := stickyParamsOf(d)["seed_profile"]; got != "dev" {
		t.Errorf("persisted seed_profile = %q, want dev", got)
	}
}

// TestStartPersistedParamsMergeAcrossRuns: a run that chooses one new
// sticky parameter must not drop the choices earlier runs persisted — two
// runs with different params leave both in the descriptor.
func TestStartPersistedParamsMergeAcrossRuns(t *testing.T) {
	worktree, sp := startFixture(t)
	sp.Hooks.Seed = &spec.Hook{
		Run: "echo seeding {seed_profile} on {stack}",
		Params: map[string]spec.Param{
			"seed_profile": {Sticky: true, Default: "dev"},
			"stack":        {Sticky: true, Default: "blue"},
		},
	}
	writeT(t, filepath.Join(worktree, "wt.yaml"), string(mustYAML(t, sp)))

	// First run: the defaults are chosen and persisted.
	if code, _, stderr := runCLI(t, "start", "--cwd", worktree); code != ExitOK {
		t.Fatalf("first start exit = %d; stderr: %s", code, stderr)
	}
	// Second run: an explicit override chooses a different value for one
	// parameter; the other must survive.
	if code, _, stderr := runCLI(t, "start", "--cwd", worktree, "--param", "stack=green"); code != ExitOK {
		t.Fatalf("second start exit = %d; stderr: %s", code, stderr)
	}
	d, err := descriptor.Read(filepath.Join(worktree, "wt-env.yaml"), "yaml")
	if err != nil {
		t.Fatalf("reading the descriptor: %v", err)
	}
	got := stickyParamsOf(d)
	if got["seed_profile"] != "dev" {
		t.Errorf("seed_profile = %q, want dev (persisted by the first run)", got["seed_profile"])
	}
	if got["stack"] != "green" {
		t.Errorf("stack = %q, want green (the second run's choice)", got["stack"])
	}
}
