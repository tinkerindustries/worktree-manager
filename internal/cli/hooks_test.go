package cli

// hooks_test.go exercises the hook sequencer (04-lifecycle.md §5): command
// resolution over resources and sticky parameters, the run contract (cwd,
// environment, stderr, non-zero stops), --dry-run, health polling with the
// timeout tail dump, and the missing-seed-credentials warning.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// hookTestSpec is a spec whose hooks are trivial shell commands, so the
// sequencer is testable without docker or a real application.
func hookTestSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	timeout := "2m"
	s := &spec.Spec{
		Version: 1, App: "hook-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
		},
		Hooks: spec.Hooks{
			Install: &spec.Hook{Run: "echo install ran"},
			Prepull: &spec.Hook{Run: "echo prepull ran"},
			Build:   &spec.Hook{Run: "echo build ran", Timeout: timeout},
			Start:   &spec.Hook{Run: "echo start ran"},
			Seed: &spec.Hook{Run: "echo seed --profile {seed_profile}",
				Params: map[string]spec.Param{"seed_profile": {Sticky: true, Default: "dev"}}},
			Health: &spec.Hook{Run: "echo health ran"},
		},
		Emit: spec.Emit{
			Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"},
			Env: &spec.EnvEmit{Path: ".env", Keys: map[string]string{
				"API_PORT": "{api}",
			}},
		},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("hook test spec does not validate: %v", err)
	}
	return s
}

// hookRunnerFor builds a runner over a temp worktree.
func hookRunnerFor(t *testing.T, sp *spec.Spec) (*hookRunner, string) {
	t.Helper()
	worktree := t.TempDir()
	ctx := spec.Context{App: sp.App, Slug: "wt-1", Slot: 1, Home: "/home/test", Worktree: worktree}
	resolved := map[string]spec.Resolved{"api": {Type: "port", Value: 4321}}
	r := newHookRunner(sp, ctx, resolved, worktree, &strings.Builder{}, false)
	keys, err := resolvedEnvKeys(sp, ctx, resolved)
	if err != nil {
		t.Fatalf("resolving env keys: %v", err)
	}
	r.envKeys = keys
	return r, worktree
}

// TestHooksResolveResourcesAndParams: a hook's command resolves its
// resource references and its declared parameters together.
func TestHooksResolveResourcesAndParams(t *testing.T) {
	sp := hookTestSpec(t)
	r, _ := hookRunnerFor(t, sp)
	cmd, err := r.resolveCommand("seed", sp.Hooks.Seed)
	if err != nil {
		t.Fatalf("resolveCommand: %v", err)
	}
	want := "echo seed --profile dev"
	if cmd != want {
		t.Errorf("resolved command = %q, want %q (the resource and the param both substituted)", cmd, want)
	}
	if got := r.chosenValues()["seed_profile"]; got != "dev" {
		t.Errorf("chosen seed_profile = %q, want the default dev (chosen on first run)", got)
	}
}

// TestStickyParamExplicitOverridesPersistedWithWarning: an explicit
// --param that contradicts the persisted choice is used, with a warning
// naming the surprise (04-lifecycle.md §5.2).
func TestStickyParamExplicitOverridesPersistedWithWarning(t *testing.T) {
	sp := hookTestSpec(t)
	r, _ := hookRunnerFor(t, sp)
	r.setPersisted(map[string]string{"seed_profile": "prod"})
	r.setExplicit(map[string]string{"seed_profile": "dev"})
	var out strings.Builder
	r.stderr = &out
	cmd, err := r.resolveCommand("seed", sp.Hooks.Seed)
	if err != nil {
		t.Fatalf("resolveCommand: %v", err)
	}
	if !strings.Contains(cmd, "--profile dev") {
		t.Errorf("command = %q, want the explicit value", cmd)
	}
	if !strings.Contains(out.String(), "overrides the persisted choice") {
		t.Errorf("stderr = %q, want the override warning", out.String())
	}
}

// TestMissingSeedParamWarnsAndSkips: a seed hook whose sticky parameter has
// no value anywhere is a warning naming the remedy, not a failure — the
// worktree is still useful without a seeded instance (B10.4).
func TestMissingSeedParamWarnsAndSkips(t *testing.T) {
	sp := hookTestSpec(t)
	sp.Hooks.Seed.Params["seed_profile"] = spec.Param{Sticky: true} // no default
	r, _ := hookRunnerFor(t, sp)
	var out strings.Builder
	r.stderr = &out
	cmd, err := r.resolveCommand("seed", sp.Hooks.Seed)
	if err != nil {
		t.Fatalf("resolveCommand must not fail on missing seed credentials: %v", err)
	}
	if cmd != "" {
		t.Errorf("command = %q, want empty (the seed hook is skipped)", cmd)
	}
	if !strings.Contains(out.String(), "seed parameter") || !strings.Contains(out.String(), "--param seed_profile=") {
		t.Errorf("stderr = %q, want the warning naming the remedy", out.String())
	}
}

// TestMissingParamOnOtherHookFails: the same missing parameter on a
// non-seed hook is a hard failure naming the hook and the flag.
func TestMissingParamOnOtherHookFails(t *testing.T) {
	sp := hookTestSpec(t)
	sp.Hooks.Build.Params = map[string]spec.Param{"toolchain": {}}
	sp.Hooks.Build.Run = "echo {toolchain}"
	r, _ := hookRunnerFor(t, sp)
	_, err := r.resolveCommand("build", sp.Hooks.Build)
	if err == nil {
		t.Fatal("resolveCommand succeeded; the missing parameter must fail the hook")
	}
	if !strings.Contains(err.Error(), "--param toolchain=") {
		t.Errorf("error = %q, want the remedy naming the flag", err.Error())
	}
}

// TestHookRunsInWorktreeWithEnvAndStreamsToStderr: the run contract — cwd
// is the worktree root, the delivery environment is exported, and output
// streams to stderr.
func TestHookRunsInWorktreeWithEnvAndStreamsToStderr(t *testing.T) {
	sp := hookTestSpec(t)
	r, worktree := hookRunnerFor(t, sp)
	sp.Hooks.Install.Run = "pwd; echo \"API_PORT=$API_PORT\""
	var out strings.Builder
	r.stderr = &out
	if err := r.runHook("install", sp.Hooks.Install); err != nil {
		t.Fatalf("runHook: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, worktree) {
		t.Errorf("output = %q, want the worktree root as cwd", text)
	}
	if !strings.Contains(text, "API_PORT=4321") {
		t.Errorf("output = %q, want the delivery environment exported (API_PORT=4321)", text)
	}
}

// TestHookNonZeroExitStopsTheSequence: a hook that exits non-zero stops the
// sequence with the exit code named.
func TestHookNonZeroExitStopsTheSequence(t *testing.T) {
	sp := hookTestSpec(t)
	sp.Hooks.Prepull.Run = "echo failing; exit 3"
	r, _ := hookRunnerFor(t, sp)
	var out strings.Builder
	r.stderr = &out
	err := r.runHook("prepull", sp.Hooks.Prepull)
	if err == nil {
		t.Fatal("runHook succeeded; a non-zero exit must stop the sequence")
	}
	if !strings.Contains(err.Error(), "exit code 3") {
		t.Errorf("error = %q, want the exit code named", err.Error())
	}
	if !strings.Contains(out.String(), "failing") {
		t.Errorf("output = %q, want the hook's output surfaced verbatim", out.String())
	}
}

// TestHookDryRunPrintsTheResolvedCommand: --dry-run prints the resolved
// command and runs nothing.
func TestHookDryRunPrintsTheResolvedCommand(t *testing.T) {
	sp := hookTestSpec(t)
	r, worktree := hookRunnerFor(t, sp)
	r.dryRun = true
	sp.Hooks.Build.Run = "touch dry-run-must-not-exist; echo {api}"
	var out strings.Builder
	r.stderr = &out
	if err := r.runHook("build", sp.Hooks.Build); err != nil {
		t.Fatalf("runHook: %v", err)
	}
	if !strings.Contains(out.String(), "would run: touch dry-run-must-not-exist; echo 4321") {
		t.Errorf("output = %q, want the resolved command printed", out.String())
	}
	if _, err := os.Stat(filepath.Join(worktree, "dry-run-must-not-exist")); err == nil {
		t.Error("--dry-run executed the hook")
	}
}

// TestHookTimeoutKillsTheProcessGroup: a hook that outlives its timeout is
// terminated, and the failure names the timeout.
func TestHookTimeoutKillsTheProcessGroup(t *testing.T) {
	sp := hookTestSpec(t)
	sp.Hooks.Build.Run = "sleep 30 & wait"
	sp.Hooks.Build.Timeout = "500ms"
	r, _ := hookRunnerFor(t, sp)
	err := r.runHook("build", sp.Hooks.Build)
	if err == nil {
		t.Fatal("runHook succeeded; the timeout must fail the hook")
	}
	if !strings.Contains(err.Error(), "timed out after 500ms") {
		t.Errorf("error = %q, want the timeout named", err.Error())
	}
}

// TestHealthPollsAndDumpsTailOnTimeout: health polls with progress and, on
// timeout, dumps the last lines of the hook's output (B7.4).
func TestHealthPollsAndDumpsTailOnTimeout(t *testing.T) {
	old := healthPollInterval
	healthPollInterval = 50 * time.Millisecond
	defer func() { healthPollInterval = old }()

	sp := hookTestSpec(t)
	sp.Hooks.Health.Run = "echo health-attempt-output; exit 1"
	sp.Hooks.Health.Timeout = "400ms"
	r, _ := hookRunnerFor(t, sp)
	var out strings.Builder
	r.stderr = &out
	err := r.runHealth(sp.Hooks.Health)
	if err == nil {
		t.Fatal("runHealth succeeded; the stack never became usable")
	}
	if !strings.Contains(err.Error(), "timed out after 400ms") {
		t.Errorf("error = %q, want the timeout named", err.Error())
	}
	text := out.String()
	if !strings.Contains(text, "attempt 1 failed") {
		t.Errorf("output = %q, want the progress log", text)
	}
	if !strings.Contains(text, "last lines of the health check output") || !strings.Contains(text, "health-attempt-output") {
		t.Errorf("output = %q, want the tail dump on timeout", text)
	}
}

// TestHealthSucceedsWhenTheStackComesUp: a health hook that fails once and
// then succeeds returns nil.
func TestHealthSucceedsWhenTheStackComesUp(t *testing.T) {
	old := healthPollInterval
	healthPollInterval = 50 * time.Millisecond
	defer func() { healthPollInterval = old }()

	sp := hookTestSpec(t)
	sp.Hooks.Health.Run = "test -f healthy-flag || (touch healthy-flag && exit 1)"
	sp.Hooks.Health.Timeout = "5s"
	r, worktree := hookRunnerFor(t, sp)
	defer os.Remove(filepath.Join(worktree, "healthy-flag"))
	if err := r.runHealth(sp.Hooks.Health); err != nil {
		t.Fatalf("runHealth: %v", err)
	}
}

// TestRunAllStopsAtFirstFailure: runAll runs hooks in order and stops at
// the first non-zero exit, returning the hook's name.
func TestRunAllStopsAtFirstFailure(t *testing.T) {
	sp := hookTestSpec(t)
	sp.Hooks.Prepull.Run = "exit 1"
	r, _ := hookRunnerFor(t, sp)
	failed, err := r.runAll([]string{"install", "prepull", "build"}, &sp.Hooks)
	if err == nil {
		t.Fatal("runAll succeeded; the sequence must stop at the failure")
	}
	if failed != "prepull" {
		t.Errorf("failed = %q, want prepull", failed)
	}
}
