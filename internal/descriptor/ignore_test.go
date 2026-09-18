package descriptor

// ignore_test.go is the real-repository layer for the ignore rule
// (05-delivery.md §2.2): the fixtures are built with real git in
// t.TempDir(), because the rule exists for a mechanism — a tracked
// .gitignore shared through the branch in a linked worktree — that only a
// real repository exercises.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// tempDir is t.TempDir() with symlinks already resolved, so path
// expectations built on it survive macOS's /var → /private/var.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := platform.RealPath(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	// A fixture root is handed to git, so it must be in the spelling an
	// external tool accepts. RealPath's Windows output is the
	// extended-length form, which git rejects as an argument; ExternalPath
	// is the way back and the identity on unix, where the symlink
	// resolution above is the whole point.
	return platform.ExternalPath(dir)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepoWithIgnore creates a repository with a committed .gitignore
// holding ignoreContent ("" for none). Returns the repo path and its
// common git dir.
func initRepoWithIgnore(t *testing.T, ignoreContent string) (repo, common string) {
	t.Helper()
	repo = filepath.Join(tempDir(t), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main", ".")
	gitIn(t, repo, "config", "user.email", "fixture@localhost")
	gitIn(t, repo, "config", "user.name", "fixture")
	if ignoreContent != "" {
		if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignoreContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")
	common = filepath.Join(repo, ".git")
	return repo, common
}

func readExclude(t *testing.T, common string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	if err != nil {
		t.Fatalf("reading info/exclude: %v", err)
	}
	return string(data)
}

// TestEnsureIgnoredWritesInfoExclude is exit criterion 7's first half: the
// line goes to $GIT_COMMON_DIR/info/exclude, and git honours it.
func TestEnsureIgnoredWritesInfoExclude(t *testing.T) {
	repo, common := initRepoWithIgnore(t, "")
	res, err := EnsureIgnored(repo, common, "wt-env.yaml")
	if err != nil {
		t.Fatalf("EnsureIgnored: %v", err)
	}
	if res.Location != "exclude" || !res.Wrote {
		t.Errorf("result = %+v, want location exclude, wrote true", res)
	}
	if !strings.Contains(readExclude(t, common), "wt-env.yaml\n") {
		t.Errorf("info/exclude lacks the line:\n%s", readExclude(t, common))
	}
	// git honours it: a descriptor at the root is ignored.
	if err := os.WriteFile(filepath.Join(repo, "wt-env.yaml"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := gitIn(t, repo, "check-ignore", "wt-env.yaml")
	if !strings.HasSuffix(out, "wt-env.yaml") {
		t.Errorf("git check-ignore = %q, want the path (the line must be honoured)", out)
	}
	if status := gitIn(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("repo is dirty after the ignore write: %s", status)
	}
}

// TestEnsureIgnoredIsIdempotent is exit criterion 7's idempotence half: a
// second call writes nothing.
func TestEnsureIgnoredIsIdempotent(t *testing.T) {
	repo, common := initRepoWithIgnore(t, "")
	if _, err := EnsureIgnored(repo, common, "wt-env.yaml"); err != nil {
		t.Fatal(err)
	}
	before := readExclude(t, common)
	res, err := EnsureIgnored(repo, common, "wt-env.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if res.Wrote {
		t.Errorf("second call wrote again: %+v", res)
	}
	if after := readExclude(t, common); after != before {
		t.Errorf("info/exclude changed on the second call:\nbefore: %q\nafter:  %q", before, after)
	}
}

// TestEnsureIgnoredLeavesTrackedGitignoreUntouched is exit criterion 7's
// tracked-file half: with a committed .gitignore, the write goes to
// info/exclude and the tracked file — and the working tree — stay clean.
// This is the mechanism the rule exists for: in a linked worktree a dirty
// .gitignore is a dirty tree another tool just created clean.
func TestEnsureIgnoredLeavesTrackedGitignoreUntouched(t *testing.T) {
	repo, common := initRepoWithIgnore(t, "# repo ignores\n*.log\n")
	before, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := EnsureIgnored(repo, common, "wt-env.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if res.Location != "exclude" {
		t.Errorf("result = %+v, want location exclude (the tracked .gitignore must not be touched)", res)
	}
	after, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf(".gitignore changed:\nbefore: %q\nafter:  %q", before, after)
	}
	if status := gitIn(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("working tree is dirty after the write: %s", status)
	}
}

// TestEnsureIgnoredSkipsWhenAdoptionCommittedTheLine is exit criterion 7's
// detection half: when adoption already committed the line to .gitignore,
// the write is skipped and info/exclude stays untouched — detected rather
// than written twice.
func TestEnsureIgnoredSkipsWhenAdoptionCommittedTheLine(t *testing.T) {
	repo, common := initRepoWithIgnore(t, "wt-env.yaml\n")
	res, err := EnsureIgnored(repo, common, "wt-env.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if res.Location != "gitignore" || res.Wrote {
		t.Errorf("result = %+v, want location gitignore, wrote false", res)
	}
	if exclude := readExclude(t, common); strings.Contains(exclude, "wt-env.yaml") {
		t.Errorf("info/exclude was written anyway:\n%s", exclude)
	}
}

// TestEnsureIgnoredOneWriteCoversLinkedWorktrees: info/exclude is read from
// the common directory, so one write made from one linked worktree covers
// every other linked worktree of the same repo — and the tracked
// .gitignore of the worktree that just got created clean stays clean.
func TestEnsureIgnoredOneWriteCoversLinkedWorktrees(t *testing.T) {
	repo, common := initRepoWithIgnore(t, "")
	wt1 := filepath.Join(filepath.Dir(repo), "wt1")
	wt2 := filepath.Join(filepath.Dir(repo), "wt2")
	gitIn(t, repo, "worktree", "add", "-q", wt1, "-b", "w1")
	gitIn(t, repo, "worktree", "add", "-q", wt2, "-b", "w2")

	res, err := EnsureIgnored(wt1, common, "wt-env.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wrote {
		t.Fatalf("first write from %s did not write: %+v", wt1, res)
	}
	// The second worktree honours the line without any write of its own.
	if err := os.WriteFile(filepath.Join(wt2, "wt-env.yaml"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := gitIn(t, wt2, "check-ignore", "wt-env.yaml")
	if !strings.HasSuffix(out, "wt-env.yaml") {
		t.Errorf("git check-ignore from the second worktree = %q, want the path", out)
	}
	if status := gitIn(t, wt2, "status", "--porcelain"); status != "" {
		t.Errorf("the second worktree is dirty: %s", status)
	}
}
