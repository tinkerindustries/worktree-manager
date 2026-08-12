package identity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adoptedRepo is a two-worktree repository that has adopted the tooling: a
// committed wt.yaml and, in the linked worktree, a descriptor recording a
// shared store.
type adoptedRepo struct {
	main        string
	wt1         string
	sharedStore string
	descriptor  string // the descriptor path inside wt1
}

func buildAdoptedRepo(t *testing.T) *adoptedRepo {
	t.Helper()
	root := t.TempDir()
	ar := &adoptedRepo{
		main:        filepath.Join(root, "repo"),
		wt1:         filepath.Join(root, "wt1"),
		sharedStore: filepath.Join(root, "shared", "db.sqlite"),
	}
	if err := os.MkdirAll(ar.main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, ar.main, "init", "-q", "-b", "main", ".")
	runGitIn(t, ar.main, "config", "user.email", "fixture@localhost")
	runGitIn(t, ar.main, "config", "user.name", "fixture")

	spec := `version: 1
app: guard-app
slots:
  max: 8
resources:
  - type: port
    name: api
emit:
  descriptor:
    filename: wt-env.yaml
    format: yaml
`
	if err := os.WriteFile(filepath.Join(ar.main, "wt.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ar.main, "CLAUDE.md"), []byte("parent copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, ar.main, "add", "wt.yaml", "CLAUDE.md")
	runGitIn(t, ar.main, "commit", "-q", "-m", "adopt")

	runGitIn(t, ar.main, "worktree", "add", "-q", ar.wt1, "-b", "wt1")

	desc := `version: 1
app: guard-app
slug: wt1
slot: 1
path: ` + ar.wt1 + `
standalone: false
description: ""
resources:
  api:
    type: port
    value: 4101
state: {}
shared:
  - name: ` + ar.sharedStore + `
    impact: writes are visible to every worktree and the main checkout
extras: {}
`
	ar.descriptor = filepath.Join(ar.wt1, "wt-env.yaml")
	if err := os.WriteFile(ar.descriptor, []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}
	return ar
}

// guardFor runs the guard engine against a request and returns the verdict.
func guardFor(t *testing.T, cwd string, toolName string, input map[string]any) *GuardVerdict {
	t.Helper()
	v, err := Guard(cwd, GuardRequest{ToolName: toolName, ToolInput: input})
	if err != nil {
		t.Fatalf("Guard(%s, %s): %v", cwd, toolName, err)
	}
	return v
}

// TestGuardDeniesWriteToPrimaryCheckout is exit criterion 2: a write to the
// primary checkout from a linked worktree is denied, and the denial names
// the correct root verbatim — the worktree's own root, so the model corrects
// itself and retries inside it.
func TestGuardDeniesWriteToPrimaryCheckout(t *testing.T) {
	ar := buildAdoptedRepo(t)
	writePath := filepath.Join(ar.main, "README.md")
	v := guardFor(t, ar.wt1, "Write", map[string]any{"file_path": writePath})
	if v.Allowed {
		t.Fatal("write to the primary checkout was allowed")
	}
	if !strings.Contains(v.Reason, writePath) || !strings.Contains(v.Reason, ar.wt1) {
		t.Errorf("denial does not name the path and the correct root verbatim: %q", v.Reason)
	}
	if v.WorktreeRoot != ar.wt1 {
		t.Errorf("verdict WorktreeRoot = %q, want %q", v.WorktreeRoot, ar.wt1)
	}
	if v.Outcome != "linked-worktree" {
		t.Errorf("verdict Outcome = %q, want linked-worktree", v.Outcome)
	}
}

// TestGuardDeniesReadCLAUDEOutside is exit criterion 3: reading the parent
// checkout's stale CLAUDE.md is denied, naming the worktree root.
func TestGuardDeniesReadCLAUDEOutside(t *testing.T) {
	ar := buildAdoptedRepo(t)
	parent := filepath.Join(ar.main, "CLAUDE.md")
	v := guardFor(t, ar.wt1, "Read", map[string]any{"file_path": parent})
	if v.Allowed {
		t.Fatal("read of the parent CLAUDE.md was allowed")
	}
	if !strings.Contains(v.Reason, parent) || !strings.Contains(v.Reason, ar.wt1) {
		t.Errorf("denial does not name the file and the correct root: %q", v.Reason)
	}
}

// TestGuardAllowsReadsInside: a read inside the worktree is allowed, and a
// read of a non-CLAUDE.md file outside the worktree is allowed too — the
// guard constrains only the stale parent copy (ARCHITECTURE.md §9.6).
func TestGuardAllowsReadsInside(t *testing.T) {
	ar := buildAdoptedRepo(t)
	if v := guardFor(t, ar.wt1, "Read", map[string]any{"file_path": filepath.Join(ar.wt1, "wt.yaml")}); !v.Allowed {
		t.Errorf("read inside the worktree denied: %q", v.Reason)
	}
	if v := guardFor(t, ar.wt1, "Read", map[string]any{"file_path": filepath.Join(ar.main, "wt.yaml")}); !v.Allowed {
		t.Errorf("read of a non-CLAUDE.md outside the worktree denied: %q", v.Reason)
	}
}

// TestGuardDeniesSharedStoreMutation: direct mutation of a live shared store
// named in the descriptor's shared block is denied, naming the store and its
// impact.
func TestGuardDeniesSharedStoreMutation(t *testing.T) {
	ar := buildAdoptedRepo(t)
	v := guardFor(t, ar.wt1, "Write", map[string]any{"file_path": ar.sharedStore})
	if v.Allowed {
		t.Fatal("write to the shared store was allowed")
	}
	if !strings.Contains(v.Reason, ar.sharedStore) || !strings.Contains(v.Reason, "shared") {
		t.Errorf("denial does not name the shared store: %q", v.Reason)
	}
	// A write inside a shared directory is a mutation of the store too.
	inside := filepath.Join(filepath.Dir(ar.sharedStore), "sub", "x.sqlite")
	v = guardFor(t, ar.wt1, "Write", map[string]any{"file_path": inside})
	if v.Allowed {
		t.Fatal("write inside the shared store directory was allowed")
	}
}

// TestGuardOneDenyPerCall: a write that violates containment and the shared
// block at once is denied once, with the shared-store reason — the more
// informative of the two — and never a second reason.
func TestGuardOneDenyPerCall(t *testing.T) {
	ar := buildAdoptedRepo(t)
	// The shared store sits outside the worktree, so both rails fire; the
	// verdict must carry exactly one reason.
	v := guardFor(t, ar.wt1, "Write", map[string]any{"file_path": ar.sharedStore})
	if v.Allowed {
		t.Fatal("write was allowed")
	}
	if strings.Count(v.Reason, "refused") != 1 {
		t.Errorf("verdict carries more than one deny reason: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "shared resource") {
		t.Errorf("the single reason is not the shared-store one: %q", v.Reason)
	}
}

// TestGuardFailsOpen is exit criterion 4: pipes, $(...), environment
// variables and globs are not parsed — the guard allows them, because a
// guard producing false denials trains the agent to work around it. Each
// call yields one verdict.
func TestGuardFailsOpen(t *testing.T) {
	ar := buildAdoptedRepo(t)
	commands := []string{
		"echo hi | tee out.txt",                // pipe
		"cat $(find . -name '*.go') > out.txt", // $()
		"echo $HOME > out.txt",                 // environment variable
		"cat *.md > out.txt",                   // glob
		"cat out.{md,txt}",                     // brace glob
		"cat out.txt > /repo/README.md 2>&1",   // redirection
	}
	for _, cmd := range commands {
		v := guardFor(t, ar.wt1, "Bash", map[string]any{"command": cmd})
		if !v.Allowed {
			t.Errorf("Bash %q denied: %q — shell constructs must fail open", cmd, v.Reason)
		}
	}
	// A file_path that itself carries glob metacharacters is ambiguous too.
	for _, p := range []string{"*.md", "/repo/*/x", "a$b"} {
		v := guardFor(t, ar.wt1, "Write", map[string]any{"file_path": p})
		if !v.Allowed {
			t.Errorf("Write %q denied: %q — ambiguous file_path must fail open", p, v.Reason)
		}
	}
	// A tool input with no file_path at all (Bash, Glob, WebFetch) is
	// allowed, with one verdict.
	v := guardFor(t, ar.wt1, "Bash", map[string]any{"command": "anything"})
	if !v.Allowed {
		t.Errorf("file_path-less call denied: %q", v.Reason)
	}
	// An unknown tool is never assumed to write.
	v = guardFor(t, ar.wt1, "SomeFutureTool", map[string]any{"file_path": filepath.Join(ar.main, "x")})
	if !v.Allowed {
		t.Errorf("unknown tool denied: %q — unknown tools fail open", v.Reason)
	}
}

// TestGuardAllowsInsideWrites: a write inside the worktree is allowed.
func TestGuardAllowsInsideWrites(t *testing.T) {
	ar := buildAdoptedRepo(t)
	v := guardFor(t, ar.wt1, "Write", map[string]any{"file_path": filepath.Join(ar.wt1, "new", "file.txt")})
	if !v.Allowed {
		t.Errorf("write inside the worktree denied: %q", v.Reason)
	}
}

// TestGuardRelativePath: a relative file_path resolves against the
// classification cwd, so a hook or a hand invocation lands where it means.
func TestGuardRelativePath(t *testing.T) {
	ar := buildAdoptedRepo(t)
	v := guardFor(t, ar.wt1, "Write", map[string]any{"file_path": "../repo/README.md"})
	if v.Allowed {
		t.Fatal("relative write into the primary checkout was allowed")
	}
	if !strings.Contains(v.Reason, ar.wt1) {
		t.Errorf("denial does not name the correct root: %q", v.Reason)
	}
	v = guardFor(t, ar.wt1, "Write", map[string]any{"file_path": "notes.md"})
	if !v.Allowed {
		t.Errorf("relative write inside the worktree denied: %q", v.Reason)
	}
}

// TestGuardPrimaryCheckout: in the primary checkout there is nothing to
// guard — slot 0 is unmanaged.
func TestGuardPrimaryCheckout(t *testing.T) {
	ar := buildAdoptedRepo(t)
	v := guardFor(t, ar.main, "Write", map[string]any{"file_path": filepath.Join(ar.main, "README.md")})
	if !v.Allowed {
		t.Errorf("write in the primary checkout denied: %q", v.Reason)
	}
	if v.Outcome != "primary-checkout" {
		t.Errorf("Outcome = %q, want primary-checkout", v.Outcome)
	}
}

// TestGuardNotARepository: outside any repository the guard allows; the
// hook is generated per adopted repo, so a not-a-repository call is manual
// use.
func TestGuardNotARepository(t *testing.T) {
	dir := t.TempDir()
	v := guardFor(t, dir, "Write", map[string]any{"file_path": filepath.Join(dir, "x")})
	if !v.Allowed {
		t.Errorf("not-a-repository call denied: %q", v.Reason)
	}
	if v.Outcome != "not-a-repository" {
		t.Errorf("Outcome = %q, want not-a-repository", v.Outcome)
	}
}

// TestGuardStandaloneInLinkedWorktree: WT_STANDALONE=1 in a linked worktree
// is refused by classification, and the guard surfaces that refusal.
func TestGuardStandaloneInLinkedWorktree(t *testing.T) {
	ar := buildAdoptedRepo(t)
	t.Setenv("WT_STANDALONE", "1")
	if _, err := Guard(ar.wt1, GuardRequest{ToolName: "Write", ToolInput: map[string]any{"file_path": filepath.Join(ar.wt1, "x")}}); err == nil {
		t.Fatal("Guard succeeded with WT_STANDALONE=1 in a linked worktree, want the refusal")
	}
}

// TestGuardDeletedWorktree: the guard reports a deleted worktree directory
// as itself rather than allowing or misclassifying.
func TestGuardDeletedWorktree(t *testing.T) {
	ar := buildAdoptedRepo(t)
	gone := filepath.Join(ar.wt1, "sub")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ar.wt1); err != nil {
		t.Fatal(err)
	}
	_, err := Guard(gone, GuardRequest{ToolName: "Write", ToolInput: map[string]any{"file_path": filepath.Join(gone, "x")}})
	var want *DeletedWorktreeError
	if err == nil {
		t.Fatal("Guard succeeded in a deleted worktree, want DeletedWorktreeError")
	}
	if !errors.As(err, &want) {
		t.Errorf("error = %T %v, want *DeletedWorktreeError", err, err)
	}
}
