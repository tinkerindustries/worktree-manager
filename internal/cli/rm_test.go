package cli

// rm_test.go exercises `wt rm`: the three tree-reading safety checks with
// their exit codes, the by-slug targeting with the directory already gone,
// the partial-state paths, and the destruction rails (never the tree the
// caller stands in, never --force). gh is faked on PATH — the real remote
// service is not part of the unit layer.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// rmFixture builds an adopted repo with one linked worktree whose branch
// has an upstream (a bare remote) and no unpushed commits, so the safety
// checks pass. It returns the main checkout, the worktree root and the
// bare remote.
func rmFixture(t *testing.T) (main, worktree, remote string) {
	t.Helper()
	sp := lifecycleSpec(t)
	base := t.TempDir()
	main = filepath.Join(base, "main")
	remote = filepath.Join(base, "remote.git")
	gitT(t, "", "init", "--bare", "-b", "main", remote)
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
	gitT(t, main, "push", "-u", "origin", "wt-1")
	return main, worktree, remote
}

// rmCoord is the canned coordinator for rm: entry found at the given
// path, clean teardown.
func rmCoord(t *testing.T, worktree string, handlers map[string]func(*protocol.Request) *protocol.Response) string {
	t.Helper()
	sock := shortSock(t, "rm")
	all := map[string]func(*protocol.Request) *protocol.Response{}
	for _, verb := range []string{"rm"} {
		all[verb] = func(req *protocol.Request) *protocol.Response {
			if h, ok := handlers[req.Verb]; ok {
				return h(req)
			}
			var args protocol.RmArgs
			json.Unmarshal(req.Args, &args)
			return &protocol.Response{Result: mustJSONT(protocol.RmResult{
				EntryFound: true,
				Path:       worktree,
				Resources:  []string{"api", "db"},
				Removed:    !args.DryRun,
			})}
		}
	}
	fakeCoordServer(t, sock, all)
	return sock
}

// fakeGh writes a gh executable on PATH. The script's exit code and stderr
// mimic real gh's contract: exit 0 with PR JSON means an open PR, exit 1
// with "no pull requests found" means none, exit 4 with an auth message
// means unauthenticated.
func fakeGh(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRmStopsOnUncommittedChanges: a dirty tree stops rm with exit 3.
func TestRmStopsOnUncommittedChanges(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo '{"number":0}'`)
	writeT(t, filepath.Join(worktree, "dirty.txt"), "uncommitted\n")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "uncommitted changes") {
		t.Errorf("stderr = %q, want the dirty-tree refusal", stderr)
	}
}

// TestRmStopsOnUnpushedCommits: commits ahead of the upstream stop rm with
// exit 3, naming them.
func TestRmStopsOnUnpushedCommits(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo '{"number":0}'`)
	writeT(t, filepath.Join(worktree, "extra.txt"), "extra\n")
	gitT(t, worktree, "add", ".")
	gitT(t, worktree, "commit", "-m", "unpushed work")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "unpushed") {
		t.Errorf("stderr = %q, want the unpushed-commits refusal", stderr)
	}
}

// TestRmStopsOnAbsentUpstream: a branch with no upstream is itself a stop,
// not a pass (B11.10).
func TestRmStopsOnAbsentUpstream(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo '{"number":0}'`)
	// Detach the upstream: the branch forgets origin/wt-1.
	gitT(t, worktree, "branch", "--unset-upstream")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "no upstream") {
		t.Errorf("stderr = %q, want the absent-upstream refusal", stderr)
	}
}

// TestRmStopsOnOpenPR: an open PR stops rm with exit 3 — the PR points at
// the branch, and the remote branch is never deleted.
func TestRmStopsOnOpenPR(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo '{"number":42,"state":"OPEN"}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "PR #42") {
		t.Errorf("stderr = %q, want the open PR named", stderr)
	}
}

// TestRmGhMissingExitsFour is exit criterion 6: a safety check that cannot
// run fails closed — with gh absent from PATH, rm stops with exit 4 naming
// the check.
func TestRmGhMissingExitsFour(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	// A PATH without gh: keep git and sh, drop everything else's gh.
	keep := []string{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, "gh")); err != nil {
			keep = append(keep, dir)
		}
	}
	t.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitUnavailable {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "gh") || !strings.Contains(stderr, "open-PR check") {
		t.Errorf("stderr = %q, want the check that could not run named", stderr)
	}
}

// TestRmGhUnauthenticatedExitsFour: an unauthenticated gh is the same
// fail-closed exit 4, naming the remedy.
func TestRmGhUnauthenticatedExitsFour(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "gh: To get started with GitHub CLI, please run: gh auth login" >&2; exit 4`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitUnavailable {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "not authenticated") {
		t.Errorf("stderr = %q, want the authentication failure named", stderr)
	}
}

// TestRmNoPRPasses: gh answering "no pull requests found" is a pass, and
// rm completes.
func TestRmNoPRPasses(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "no pull requests found for branch \"wt-1\"" >&2; exit 1`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	// Teardown first, git worktree remove second: the directory is gone.
	if _, err := os.Stat(worktree); err == nil {
		t.Error("the worktree directory survived the rm")
	}
}

// TestRmRefusesStandingInTheTarget: the caller's own tree is never
// deleted (B11.11) — exit 3.
func TestRmRefusesStandingInTheTarget(t *testing.T) {
	_, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "no pull requests found" >&2; exit 1`)

	code, _, stderr := runCLI(t, "rm", "--cwd", worktree)
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "standing inside") {
		t.Errorf("stderr = %q, want the standing-in refusal", stderr)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Error("the worktree was removed despite the refusal")
	}
}

// TestRmBySlugWithDirectoryDeleted is exit criterion 5: rm from outside
// the tree, by slug, with the directory already deleted — the teardown is
// registry-and-label-only, the tree-reading checks are skipped with the
// bound stated, and rm exits 0.
func TestRmBySlugWithDirectoryDeleted(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "no pull requests found" >&2; exit 1`)
	// Delete the directory by hand, the way a tool that never calls wt rm
	// leaves it.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("removing the worktree: %v", err)
	}

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "already gone") {
		t.Errorf("stderr = %q, want the registry-and-label-only bound stated", stderr)
	}
}

// TestRmDryRunPreviewsAndChangesNothing: --dry-run reports what would be
// reaped and torn down, and removes neither the entry nor the tree.
func TestRmDryRunPreviewsAndChangesNothing(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := rmCoord(t, worktree, nil)
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "no pull requests found" >&2; exit 1`)

	code, stdout, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing was changed") {
		t.Errorf("stdout = %q, want the dry-run statement", stdout)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Error("--dry-run removed the worktree")
	}
}

// TestRmDirectoryPresentNoEntry: with no registry entry, rm is a `git
// worktree remove` and nothing to deallocate (04-lifecycle.md §7.3).
func TestRmDirectoryPresentNoEntry(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	sock := shortSock(t, "rm-noentry")
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"rm": canned(&protocol.Response{Result: mustJSONT(protocol.RmResult{EntryFound: false})}),
	})
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "no pull requests found" >&2; exit 1`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if _, err := os.Stat(worktree); err == nil {
		t.Error("the git worktree was not removed")
	}
	if !strings.Contains(stderr, "nothing was deallocated") {
		t.Errorf("stderr = %q, want the no-entry path stated", stderr)
	}
}

// TestRmNeitherIsAMessageAndAStop: no entry and no worktree with the slug
// is a message and a stop (exit 1).
func TestRmNeitherIsAMessageAndAStop(t *testing.T) {
	main, _, _ := rmFixture(t)
	sock := shortSock(t, "rm-neither")
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"rm": canned(&protocol.Response{Result: mustJSONT(protocol.RmResult{EntryFound: false})}),
	})
	t.Setenv("WT_SOCKET", sock)
	fakeGh(t, `echo "no pull requests found" >&2; exit 1`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "no-such-worktree")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, "nothing to remove") {
		t.Errorf("stderr = %q, want the neither-path message", stderr)
	}
}

// TestRmRequiresATarget: rm with no slug and a cwd that is not a worktree
// is a usage error — rm never picks a target on its own.
func TestRmRequiresATarget(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runCLI(t, "rm", "--cwd", dir)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--slug") {
		t.Errorf("stderr = %q, want the slug flag named", stderr)
	}
}

// TestRmCoordinatorUnreachableExitsFive: with the coordinator down, rm
// exits 5 naming the start command.
func TestRmCoordinatorUnreachableExitsFive(t *testing.T) {
	main, _, _ := rmFixture(t)
	t.Setenv("WT_SOCKET", shortSock(t, "nothing-listens"))
	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitUnreachable {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUnreachable, stderr)
	}
	// The remedy is platform-specific by design: where a supervisor is
	// implemented it is `wt daemon install`, and elsewhere it is the
	// foreground `wtd` invocation. Asserting either one literally pins the
	// platform the test happens to run on, so assert that a start command is
	// named at all.
	if !strings.Contains(stderr, "wtd") && !strings.Contains(stderr, "daemon install") {
		t.Errorf("stderr = %q, want a command that starts the coordinator", stderr)
	}
}
