package cli

// fakegh_test.go holds the two pieces of PATH surgery the rm and cleanup
// tests need: a fake gh put on PATH, and the real gh taken off it. Both
// were duplicated per file and both got Windows wrong, so there is one of
// each here.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// ghNoPRScript is gh's no-pull-request contract: the message on stderr and
// exit 1.
const ghNoPRScript = `echo "no pull requests found for branch \"$(git branch --show-current)\"" >&2
exit 1`

// fakeGhOnPath writes a fake gh into a fresh directory and puts it first on
// PATH. The script's exit code and stderr mimic real gh's contract: exit 0
// with PR JSON means a PR exists, in whatever state the JSON names — gh
// answers the same way for OPEN, MERGED and CLOSED. Exit 1 with "no pull
// requests found" means none, exit 4 with an auth message means
// unauthenticated.
func fakeGhOnPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	writeFakeGh(t, dir, script)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := exec.LookPath("gh"); err != nil || filepath.Dir(got) != dir {
		t.Fatalf("the fake gh is not what PATH resolves: LookPath = %q, %v; want one in %s", got, err, dir)
	}
}

// writeFakeGh writes the fake gh into dir.
//
// The body is a /bin/sh script on every platform: what these tests encode
// is gh's exit codes and messages, and one body keeps them identical.
// Windows needs a second file beside it. exec.LookPath there only accepts
// a name whose extension is in PATHEXT, so a bare `gh` is skipped and the
// machine's real gh — unauthenticated on a CI runner — is found instead;
// gh.cmd is a name it does accept, and it hands the same script to the
// POSIX shell Git for Windows ships, forwarding the arguments and the exit
// code.
func writeFakeGh(t *testing.T, dir, script string) {
	t.Helper()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("writing the fake gh: %v", err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("the fake gh needs a POSIX shell (Git for Windows provides one): %v", err)
	}
	// %~dp0 ends in a backslash; sh wants forward slashes in the path it
	// is handed, so the substitution converts them.
	shim := "@echo off\r\n" +
		"setlocal\r\n" +
		"set \"WTFAKEGH=%~dp0gh\"\r\n" +
		"set \"WTFAKEGH=%WTFAKEGH:" + `\` + "=/%\"\r\n" +
		"sh \"%WTFAKEGH%\" %*\r\n"
	if err := os.WriteFile(filepath.Join(dir, "gh.cmd"), []byte(shim), 0o755); err != nil {
		t.Fatalf("writing the fake gh shim: %v", err)
	}
}

// neededTools are the tools the missing-gh tests run once gh is gone.
// git is required — the removal checks shell out to it; sh and env are
// conveniences a hook may reach for.
var neededTools = []string{"git", "sh", "env"}

// setPathWithoutGh takes gh off PATH, leaving the rest of the machine's
// tools where they are.
//
// It drops each directory gh resolves from rather than building a minimal
// PATH out of symlinks, which is what this used to do. That cannot work on
// Windows three times over: creating a symlink needs a privilege an
// ordinary user does not have, LookPath would skip the extensionless link
// anyway, and git.exe loads its DLLs from the directory it actually lives
// in.
//
// Dropping a directory can take more than gh with it. On a Linux runner
// gh and git are both /usr/bin, so the drop leaves no git either, which is
// what restoreTools puts back.
func setPathWithoutGh(t *testing.T) {
	t.Helper()

	// Dropping gh's directory from PATH is no longer enough on its own.
	// treecheck.Gh resolves through platform.LookHelper, which falls back
	// to the known install directories when PATH does not answer — and
	// Homebrew's /opt/homebrew/bin, where gh usually lives, is one of
	// them. Emptying the fallback list is how a test says "absent".
	t.Setenv(platform.HelperDirsEnv, "")

	// Resolve the tools before any dropping: once the directory is off
	// PATH, LookPath can no longer say where they were.
	found := map[string]string{}
	for _, tool := range neededTools {
		if p, err := exec.LookPath(tool); err == nil {
			found[tool] = p
		}
	}

	// A machine can have more than one gh on PATH — Ubuntu's /bin and
	// /usr/bin are one directory under two names — so drop and look again
	// until none answers. LookPath decides where gh is under the rules the
	// code under test uses, PATHEXT on Windows and the execute bit on
	// unix, so nothing here reimplements them. Each pass removes at least
	// one entry, so the entry count bounds the loop.
	for range len(filepath.SplitList(os.Getenv("PATH"))) + 1 {
		gh, err := exec.LookPath("gh")
		if err != nil {
			break
		}
		t.Setenv("PATH", withoutDir(os.Getenv("PATH"), filepath.Dir(gh)))
	}

	restoreTools(t, found)
}

// restoreTools puts back, in a directory of its own, each tool the
// gh-dropping took with it, and states what the test is standing on: git
// present, gh absent.
func restoreTools(t *testing.T, found map[string]string) {
	t.Helper()
	var shim string
	for _, tool := range neededTools {
		src, ok := found[tool]
		if !ok {
			continue // it was not on PATH to begin with
		}
		if _, err := exec.LookPath(tool); err == nil {
			continue // the drop left it where it was
		}
		if runtime.GOOS == "windows" {
			// None of the three ways of moving a tool works here — see
			// setPathWithoutGh. This arises only where gh shares a
			// directory with one of them, which is not how the Windows
			// installers lay either out.
			t.Skipf("gh shares its directory with %s, and Windows cannot relocate %s", tool, tool)
		}
		if shim == "" {
			shim = t.TempDir()
			t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		if err := os.Symlink(src, filepath.Join(shim, tool)); err != nil {
			t.Fatalf("linking %s into the test PATH: %v", tool, err)
		}
	}
	if p, err := exec.LookPath("gh"); err == nil {
		t.Fatalf("gh is still on PATH at %s; the test cannot prove the missing-gh path", p)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("the test PATH needs git: %v", err)
	}
}

// withoutDir drops every PATH entry naming dir, and every empty entry with
// them: an empty entry is the current directory, which no test wants a
// tool resolving out of.
func withoutDir(path, dir string) string {
	var kept []string
	for _, entry := range filepath.SplitList(path) {
		if entry == "" || sameDirName(entry, dir) {
			continue
		}
		kept = append(kept, entry)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// sameDirName compares two PATH entries by name — cleaned, and
// case-insensitively on Windows. Two names for one directory that this
// misses cost a second pass of the drop loop, not a wrong answer.
func sameDirName(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// TestSetPathWithoutGhKeepsGitWhereGhSharesItsDirectory is the Linux
// runner's shape: gh and git are both /usr/bin, so dropping gh's directory
// drops git too. The helper has to put git back.
func TestSetPathWithoutGhKeepsGitWhereGhSharesItsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Windows installer puts gh and git in one directory")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("this test needs a git to point at: %v", err)
	}

	// One directory holding both, and nothing else on PATH.
	shared := t.TempDir()
	writeFakeGh(t, shared, ghNoPRScript)
	if err := os.Symlink(git, filepath.Join(shared, "git")); err != nil {
		t.Fatalf("linking git into the shared directory: %v", err)
	}
	t.Setenv("PATH", shared)

	setPathWithoutGh(t)

	if p, err := exec.LookPath("gh"); err == nil {
		t.Errorf("gh is still on PATH at %s", p)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Errorf("git went with gh's directory: %v", err)
	}
}
