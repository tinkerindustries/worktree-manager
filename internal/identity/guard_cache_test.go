package identity

// guard_cache_test.go pins the phase-7 decision on the guard hook's
// per-session classification cache (plan.md §9.2, 07-agent-surface.md
// §6.3): a cached classification is reused while the worktree lives, and
// a worktree removed mid-session drops the cache, reclassifies, and the
// session hears the one-time note.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guardCacheRepo builds a repository with one linked worktree, returns
// the worktree path.
func guardCacheRepo(t *testing.T) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	gitT(t, main, "commit", "--allow-empty", "-m", "fixture")
	wt = filepath.Join(base, "brisk-otter")
	gitT(t, main, "worktree", "add", "-b", "brisk-otter", wt, "main")
	return main, wt
}

// TestGuardCacheReusesThenDropsOnRemoval: the first call classifies and
// writes the cache, the second call reuses it (the cache file is not
// rewritten), and removing the worktree mid-session drops the cache — the
// next call reclassifies and returns the one-time note.
func TestGuardCacheReusesThenDropsOnRemoval(t *testing.T) {
	_, wt := guardCacheRepo(t)
	cacheDir := filepath.Join(t.TempDir(), "guard-cache")

	cls, note, err := ClassifyCached(wt, false, cacheDir)
	if err != nil {
		t.Fatalf("first classification: %v", err)
	}
	if cls.Outcome != LinkedWorktree {
		t.Fatalf("outcome = %v, want linked-worktree", cls.Outcome)
	}
	if note != "" {
		t.Errorf("the first call returned a note: %q", note)
	}
	entries := cacheFiles(t, cacheDir)
	if len(entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(entries))
	}
	entryPath := filepath.Join(cacheDir, entries[0])

	// The second call is a hit: the entry is reused, not rewritten.
	before, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("stat the cache entry: %v", err)
	}
	cls2, note2, err := ClassifyCached(wt, false, cacheDir)
	if err != nil {
		t.Fatalf("second classification: %v", err)
	}
	if cls2.Outcome != LinkedWorktree || cls2.WorktreeRoot != cls.WorktreeRoot {
		t.Errorf("second classification = %+v, want the cached one", cls2)
	}
	if note2 != "" {
		t.Errorf("a hit returned a note: %q", note2)
	}
	after, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("stat the cache entry again: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("a cache hit rewrote the entry (mtime %v → %v); the git subprocesses should be skipped", before.ModTime(), after.ModTime())
	}

	// The worktree is removed mid-session: the next call drops the cache,
	// reclassifies, and says so once.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("removing the worktree: %v", err)
	}
	_, note3, err := ClassifyCached(wt, false, cacheDir)
	if err == nil {
		t.Fatal("classifying a removed worktree's cwd succeeded")
	}
	if !strings.Contains(note3, "removed or replaced mid-session") {
		t.Errorf("the removal note is missing: %q", note3)
	}
	// The stale entry is gone; the next successful classification writes a
	// fresh one.
	if n := cacheFiles(t, cacheDir); len(n) != 0 {
		t.Errorf("the stale cache entry survived the invalidation: %v", n)
	}
}

// TestGuardCachedVerdictCarriesTheCache: the GuardCached path honours the
// cache and returns the note alongside the verdict.
func TestGuardCachedVerdictCarriesTheCache(t *testing.T) {
	_, wt := guardCacheRepo(t)
	cacheDir := filepath.Join(t.TempDir(), "guard-cache")
	req := GuardRequest{ToolName: "Write", ToolInput: map[string]any{"file_path": filepath.Join(wt, "x")}}

	v, note, err := GuardCached(wt, cacheDir, req)
	if err != nil {
		t.Fatalf("GuardCached: %v", err)
	}
	if !v.Allowed {
		t.Errorf("a write inside the worktree was denied: %+v", v)
	}
	if note != "" {
		t.Errorf("the first call returned a note: %q", note)
	}

	// A hit produces the same verdict with no note.
	v2, note2, err := GuardCached(wt, cacheDir, req)
	if err != nil {
		t.Fatalf("second GuardCached: %v", err)
	}
	if v2.Allowed != v.Allowed || v2.WorktreeRoot != v.WorktreeRoot {
		t.Errorf("cached verdict = %+v, want %+v", v2, v)
	}
	if note2 != "" {
		t.Errorf("a hit returned a note: %q", note2)
	}
}

// cacheFiles lists the cache directory's entry files.
func cacheFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading the cache dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// gitT runs one git command.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := execGit(dir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
}

// execGit builds one git command.
func execGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd
}
