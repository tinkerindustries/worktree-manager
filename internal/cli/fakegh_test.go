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

// setPathWithoutGh takes gh off PATH, leaving everything else where it is.
//
// It drops each directory that holds a gh executable rather than building
// a minimal PATH out of symlinks, which is what this used to do. That
// cannot work on Windows three times over: creating a symlink needs a
// privilege an ordinary user does not have, LookPath would skip the
// extensionless link anyway, and git.exe loads its DLLs from the directory
// it actually lives in. Dropping directories keeps git, sh and the system
// directories intact and is the same operation on every platform.
func setPathWithoutGh(t *testing.T) {
	t.Helper()
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || dirHasGh(dir) {
			continue
		}
		kept = append(kept, dir)
	}
	t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
	if p, err := exec.LookPath("gh"); err == nil {
		t.Fatalf("gh is still on PATH at %s; the test cannot prove the missing-gh path", p)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("the test PATH needs git: %v", err)
	}
}

// dirHasGh reports whether dir holds something LookPath would resolve as
// gh: the bare name on unix, and a name carrying one of PATHEXT's
// extensions on Windows.
func dirHasGh(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if runtime.GOOS != "windows" {
			if name == "gh" {
				return true
			}
			continue
		}
		ext := filepath.Ext(name)
		if !strings.EqualFold(strings.TrimSuffix(name, ext), "gh") {
			continue
		}
		for _, pathExt := range filepath.SplitList(os.Getenv("PATHEXT")) {
			if strings.EqualFold(ext, pathExt) {
				return true
			}
		}
	}
	return false
}
