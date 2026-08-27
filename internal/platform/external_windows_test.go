//go:build windows

package platform

// external_windows_test.go pins the "path for an external tool"
// conversion: RealPath's output is the extended-length form, git rejects
// that form as an argument, and ExternalPath is the one way back.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExternalPathStripsTheExtendedPrefix: the four shapes, including the
// UNC one, and a path with no prefix left alone.
func TestExternalPathStripsTheExtendedPrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{`\\?\C:\Users\u\repo`, `C:\Users\u\repo`},
		{`\\?\UNC\server\share\repo`, `\\server\share\repo`},
		{`C:\Users\u\repo`, `C:\Users\u\repo`},
		{`relative\path`, `relative\path`},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ExternalPath(tt.in); got != tt.want {
			t.Errorf("ExternalPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestExternalPathIsWhatGitAccepts is the reason the conversion exists: a
// realised path handed to git as an argument fails, and the converted one
// works. It proves the failure as well as the fix, because a change to
// realPath that stopped producing the extended form would otherwise make
// this test pass for the wrong reason.
func TestExternalPathIsWhatGitAccepts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	real, err := RealPath(dir)
	if err != nil {
		t.Fatalf("RealPath(%s): %v", dir, err)
	}
	if !strings.HasPrefix(real, `\\?\`) {
		t.Skipf("RealPath returned %q, which is not the extended form; nothing to convert on this machine", real)
	}
	repo := filepath.Join(real, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) ([]byte, error) {
		c := exec.Command("git", args...)
		c.Dir = dir
		return c.CombinedOutput()
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
		{"commit", "-q", "--allow-empty", "-m", "initial"},
	} {
		if out, err := git(ExternalPath(repo), args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// `git worktree add <path>` is the operation the tool runs (its
	// remove twin is treecheck.WorktreeRemove). git rejects the realised
	// spelling as an argument: it turns the backslashes into forward
	// slashes and then cannot open //?/C:/...
	tree := filepath.Join(real, "wt1")
	if out, err := git(ExternalPath(repo), "worktree", "add", "-q", tree, "-b", "wt-rejected"); err == nil {
		t.Fatalf("git accepted the extended-length path %q; the conversion may no longer be needed\n%s", tree, out)
	}
	// It accepts the converted one.
	if out, err := git(ExternalPath(repo), "worktree", "add", "-q", ExternalPath(tree), "-b", "wt1"); err != nil {
		t.Fatalf("git worktree add %s: %v\n%s", ExternalPath(tree), err, out)
	}
}
