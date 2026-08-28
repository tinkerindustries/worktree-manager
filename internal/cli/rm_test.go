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

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// rmFixture builds an adopted repo with one linked worktree whose branch
// has an upstream (a bare remote) and no unpushed commits, so the safety
// checks pass. It returns the main checkout, the worktree root and the
// bare remote.
func rmFixture(t *testing.T) (main, worktree, remote string) {
	t.Helper()
	return rmFixtureRemoval(t, spec.Removal{})
}

// rmFixtureRemoval is rmFixture with a removal policy committed in the
// wt.yaml, which is how a repository states that a check it cares about
// must refuse rather than warn.
func rmFixtureRemoval(t *testing.T, rem spec.Removal) (main, worktree, remote string) {
	t.Helper()
	sp := lifecycleSpec(t)
	sp.Removal = rem
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
// path, clean teardown, and a doctor that finds nothing — which is what rm
// asks it after the teardown, and the ordinary answer.
func rmCoord(t *testing.T, worktree string, handlers map[string]func(*api.Request) *api.Response) string {
	t.Helper()
	all := map[string]func(*api.Request) *api.Response{
		"doctor": canned(&api.Response{Result: mustJSONT(api.DoctorResult{})}),
	}
	if h, ok := handlers["doctor"]; ok {
		all["doctor"] = h
	}
	for _, verb := range []string{"rm"} {
		verb := verb
		all[verb] = func(req *api.Request) *api.Response {
			if h, ok := handlers[verb]; ok {
				return h(req)
			}
			var args api.RmArgs
			json.Unmarshal(req.Args, &args)
			return &api.Response{Result: mustJSONT(api.RmResult{
				EntryFound: true,
				Path:       worktree,
				Resources:  []string{"api", "db"},
				Removed:    !args.DryRun,
			})}
		}
	}
	return fakeCoordServer(t, all)
}

// fakeGh puts a fake gh on PATH; the shape of the script is documented on
// fakeGhOnPath in fakegh_test.go.
func fakeGh(t *testing.T, script string) {
	t.Helper()
	fakeGhOnPath(t, script)
}

// TestRmStopsOnUncommittedChanges: a dirty tree stops rm with exit 3.
func TestRmStopsOnUncommittedChanges(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
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

// TestRmUncommittedWarnStillMeetsGitsRefusal: a repository may set
// removal.uncommitted: warn, and rm then says its piece and continues — but
// `git worktree remove` is never forced, so git is the one that stops it.
// The two rails are separate on purpose: the checks are advice about the
// tree, git's refusal is a fact about it.
func TestRmUncommittedWarnStillMeetsGitsRefusal(t *testing.T) {
	warn := spec.RemovalWarn
	main, worktree, _ := rmFixtureRemoval(t, spec.Removal{Uncommitted: &warn})
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)
	writeT(t, filepath.Join(worktree, "dirty.txt"), "uncommitted\n")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitFailure, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "uncommitted changes") {
		t.Errorf("stderr = %q, want the dirty tree warned about", stderr)
	}
	if !strings.Contains(stderr, "nothing was forced") {
		t.Errorf("stderr = %q, want git's refusal reported as itself", stderr)
	}
}

// TestRmJSONCarriesTheWarnings: a warned-past check is bounded coverage, so
// --json states it too rather than reporting a clean removal.
func TestRmJSONCarriesTheWarnings(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":42,"state":"OPEN"}'`)

	code, stdout, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	var got rmResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding %q: %v", stdout, err)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "PR #42") {
		t.Errorf("warnings = %v, want the open PR named", got.Warnings)
	}
}

// TestRmWarnsPastUnpushedCommits: the default policy for the unpushed
// check is warn, so commits ahead of the upstream are printed and rm
// continues — a local-only branch is the ordinary shape of a personal
// project.
func TestRmWarnsPastUnpushedCommits(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)
	writeT(t, filepath.Join(worktree, "extra.txt"), "extra\n")
	gitT(t, worktree, "add", ".")
	gitT(t, worktree, "commit", "-m", "unpushed work")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "unpushed") {
		t.Errorf("stderr = %q, want the unpushed commits warned about", stderr)
	}
	if _, err := os.Stat(worktree); err == nil {
		t.Errorf("the worktree %s survived rm", worktree)
	}
}

// TestRmStrictStopsOnUnpushedCommits: --strict makes every check refuse for
// one run, whatever the spec says.
func TestRmStrictStopsOnUnpushedCommits(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)
	writeT(t, filepath.Join(worktree, "extra.txt"), "extra\n")
	gitT(t, worktree, "add", ".")
	gitT(t, worktree, "commit", "-m", "unpushed work")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--strict")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "unpushed") {
		t.Errorf("stderr = %q, want the unpushed-commits refusal", stderr)
	}
}

// TestRmSpecStopsOnUnpushedCommits: the committed spec is the repository's
// policy, and removal.unpushed: refuse restores the rail with no flag.
func TestRmSpecStopsOnUnpushedCommits(t *testing.T) {
	refuse := spec.RemovalRefuse
	main, worktree, _ := rmFixtureRemoval(t, spec.Removal{Unpushed: &refuse})
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
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

// TestRmForceOverridesAStrictSpec: --force warns past every check for one
// run, including one the spec set to refuse.
func TestRmForceOverridesAStrictSpec(t *testing.T) {
	refuse := spec.RemovalRefuse
	main, worktree, _ := rmFixtureRemoval(t, spec.Removal{Unpushed: &refuse})
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)
	writeT(t, filepath.Join(worktree, "extra.txt"), "extra\n")
	gitT(t, worktree, "add", ".")
	gitT(t, worktree, "commit", "-m", "unpushed work")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want the warned-past check stated", stderr)
	}
}

// TestRmStrictAndForceTogetherIsUsage: the two ask for opposite things, so
// giving both is a usage error rather than a silent precedence rule.
func TestRmStrictAndForceTogetherIsUsage(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--strict", "--force")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--strict") || !strings.Contains(stderr, "--force") {
		t.Errorf("stderr = %q, want both flags named", stderr)
	}
}

// TestRmWarnsPastAnAbsentUpstream: an absent upstream is still a hit, but
// under the default policy it is a warning rather than a stop — a branch
// that was never pushed is what a personal project looks like.
func TestRmWarnsPastAnAbsentUpstream(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)
	// Detach the upstream: the branch forgets origin/wt-1.
	gitT(t, worktree, "branch", "--unset-upstream")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "no upstream") {
		t.Errorf("stderr = %q, want the absent upstream warned about", stderr)
	}
}

// TestRmStrictStopsOnAbsentUpstream: under a refusing policy an absent
// upstream is itself a stop, not a pass (B11.10).
func TestRmStrictStopsOnAbsentUpstream(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)
	gitT(t, worktree, "branch", "--unset-upstream")

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--strict")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "no upstream") {
		t.Errorf("stderr = %q, want the absent-upstream refusal", stderr)
	}
}

// TestRmWarnsPastAnOpenPR: the default policy for the PR check is warn, so
// an open PR is printed and rm continues. The remote branch is still never
// deleted, so the PR keeps pointing at something.
func TestRmWarnsPastAnOpenPR(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":42,"state":"OPEN"}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "PR #42") {
		t.Errorf("stderr = %q, want the open PR warned about by number", stderr)
	}
}

// TestRmSpecStopsOnOpenPR: a repository whose branches always carry a PR
// says so, and then an open PR stops rm with exit 3 — the PR points at the
// branch, and the remote branch is never deleted.
func TestRmSpecStopsOnOpenPR(t *testing.T) {
	refuse := spec.RemovalRefuse
	main, worktree, _ := rmFixtureRemoval(t, spec.Removal{OpenPR: &refuse})
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":42,"state":"OPEN"}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "PR #42") {
		t.Errorf("stderr = %q, want the open PR named", stderr)
	}
}

// TestRmProceedsPastAFinishedPR: a merged or closed PR is the end of the
// branch's life, and rm is how the worktree goes with it. gh reports both
// the same way it reports an open one — exit 0 with the state in the JSON —
// so the state is what decides, not gh's exit code.
func TestRmProceedsPastAFinishedPR(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			main, worktree, _ := rmFixture(t)
			t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
			fakeGh(t, `echo '{"number":42,"state":"`+state+`"}'`)

			code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
			}
			if _, err := os.Stat(worktree); err == nil {
				t.Errorf("the worktree %s survived rm", worktree)
			}
		})
	}
}

// TestRmStrictStopsOnADraftPR: a draft PR still points at the branch, so it
// is an open PR for rm's purposes.
func TestRmStrictStopsOnADraftPR(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":43,"state":"DRAFT"}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--strict")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "PR #43") {
		t.Errorf("stderr = %q, want the draft PR named", stderr)
	}
}

// TestRmGhMissingWarnsByDefault: gh absent from PATH is a check that
// cannot run. Under the default policy that is a warning, not a stop —
// otherwise a machine without gh could remove no worktree at all.
func TestRmGhMissingWarnsByDefault(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	setPathWithoutGh(t)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "open-PR check") {
		t.Errorf("stderr = %q, want the check that could not run named", stderr)
	}
}

// TestRmGhMissingExitsFourWhenStrict is exit criterion 6 under a refusing
// policy: a safety check that cannot run fails closed — with gh absent from
// PATH, rm stops with exit 4 naming the check.
func TestRmGhMissingExitsFourWhenStrict(t *testing.T) {
	refuse := spec.RemovalRefuse
	main, worktree, _ := rmFixtureRemoval(t, spec.Removal{OpenPR: &refuse})
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	setPathWithoutGh(t)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitUnavailable {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUnavailable, stderr)
	}
	if !strings.Contains(stderr, "gh") || !strings.Contains(stderr, "open-PR check") {
		t.Errorf("stderr = %q, want the check that could not run named", stderr)
	}
}

// TestRmGhUnauthenticatedExitsFourWhenStrict: an unauthenticated gh is the
// same fail-closed exit 4, naming the remedy.
func TestRmGhUnauthenticatedExitsFourWhenStrict(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo "gh: To get started with GitHub CLI, please run: gh auth login" >&2; exit 4`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--strict")
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
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
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
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
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
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
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
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
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

// TestRmAbandonPassesTheFlagAndPrintsWhatItLeftBehind is item 5b's CLI
// half: --abandon reaches the coordinator's rm verb, and the coordinator's
// own notes come back printed — --abandon is destructive-adjacent, so its
// whole point is that the operator learns exactly what it did. This also
// pins the fix to a real gap the flag would otherwise have fallen into:
// runRm used to build its final report from its own notes alone and never
// looked at the ones the rm verb returned.
func TestRmAbandonPassesTheFlagAndPrintsWhatItLeftBehind(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	var gotAbandon bool
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, map[string]func(*api.Request) *api.Response{
		"rm": func(req *api.Request) *api.Response {
			var args api.RmArgs
			json.Unmarshal(req.Args, &args)
			gotAbandon = args.Abandon
			if args.DryRun {
				return &api.Response{Result: mustJSONT(api.RmResult{
					EntryFound: true, Path: worktree, Resources: []string{"compose"},
				})}
			}
			return &api.Response{Result: mustJSONT(api.RmResult{
				EntryFound: true, Path: worktree, Resources: []string{"compose"}, Removed: true,
				Notes: []string{"--abandon: dropped the registry entry and freed the slot without tearing down 1 resource(s), left for hand cleanup: compose"},
			})}
		},
	}))
	fakeGh(t, `echo '{"number":0}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--abandon")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !gotAbandon {
		t.Error("the rm request must carry Abandon: true")
	}
	if !strings.Contains(stderr, "left for hand cleanup: compose") {
		t.Errorf("stderr = %q, want the coordinator's abandon note printed", stderr)
	}
}

// TestRmAbandonDryRunPreviewSaysAbandonNotTeardown: the dry-run preview
// under --abandon must never claim a teardown would happen, since none
// does — it names what would be abandoned instead.
func TestRmAbandonDryRunPreviewSaysAbandonNotTeardown(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))
	fakeGh(t, `echo '{"number":0}'`)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--abandon", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitOK, stderr)
	}
	if !strings.Contains(stderr, "would abandon the resources without tearing them down") {
		t.Errorf("stderr = %q, want the abandon-specific preview wording", stderr)
	}
	if strings.Contains(stderr, "would tear down the resources") {
		t.Errorf("stderr = %q, must not use the ordinary teardown wording under --abandon", stderr)
	}
}

// TestRmAbandonWithPurgeIsUsage: --abandon runs no driver, so a purge —
// which only happens as part of a state-path's own teardown — would
// silently do nothing under it. The combination is refused before rm ever
// dials the coordinator, the same way an unresolvable --purge value is.
//
// The repository is built inline here rather than through purge_test.go's
// purgeSpecRepo/withPurgeDB: those live behind a `//go:build !acceptance`
// tag, and this file carries no tag of its own, so a call to them would
// leave `go vet -tags acceptance ./...` (CLAUDE.md's own gate) unable to
// compile this package at all.
func TestRmAbandonWithPurgeIsUsage(t *testing.T) {
	sp := lifecycleSpec(t)
	for i := range sp.Resources {
		if sp.Resources[i].Name == "db" {
			sp.Resources[i].Purge = &spec.Purge{Flag: "--purge-db"}
		}
	}
	dir := t.TempDir()
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	writeT(t, filepath.Join(dir, "wt.yaml"), string(data))

	code, _, stderr := runCLI(t, "rm", "--cwd", dir, "--slug", "gone", "--abandon", "--purge", "db")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr:\n%s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--abandon") || !strings.Contains(stderr, "db") {
		t.Errorf("the refusal must name both --abandon and the selected resource:\n%s", stderr)
	}
}

// TestRmDirectoryPresentNoEntry: with no registry entry, rm is a `git
// worktree remove` and nothing to deallocate (04-lifecycle.md §7.3).
func TestRmDirectoryPresentNoEntry(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"rm": canned(&api.Response{Result: mustJSONT(api.RmResult{EntryFound: false})}),
	})
	t.Setenv("WT_ENDPOINT", ep)
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
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"rm": canned(&api.Response{Result: mustJSONT(api.RmResult{EntryFound: false})}),
	})
	t.Setenv("WT_ENDPOINT", ep)
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
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
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

// TestRmVerifiesItsOwnTeardown: rm asks doctor whether anything of the
// entry outlived the teardown, and reports what it finds. A half-finished
// teardown frees the slot while its resources are still running, which is
// the collision the slot model exists to prevent and the one failure that
// is invisible at the moment it happens.
func TestRmVerifiesItsOwnTeardown(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	fakeGh(t, `echo '{"number":7,"state":"MERGED"}'`)
	ep := rmCoord(t, worktree, map[string]func(*api.Request) *api.Response{
		"doctor": canned(&api.Response{Result: mustJSONT(api.DoctorResult{
			Findings: []api.DoctorFinding{
				{App: "lifecycle-app", Slug: "wt-1", Level: "error",
					Message: "a container still carries the compose project label",
					Remedy:  "run: wt reconcile"},
				// Another entry's finding, and an info line about this one:
				// neither is drift from this teardown.
				{App: "lifecycle-app", Slug: "wt-2", Level: "error", Message: "somebody else's problem"},
				{App: "lifecycle-app", Slug: "wt-1", Level: "info", Message: "just so you know"},
			},
		})}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	// rm cannot undo what it destroyed, so drift never changes the exit
	// code. It is reported loudly instead, with the verb that repairs it.
	if !strings.Contains(stderr, "compose project label") {
		t.Errorf("the drift was not reported: %q", stderr)
	}
	if !strings.Contains(stderr, "wt reconcile") {
		t.Errorf("the drift report does not name the repair: %q", stderr)
	}
	if strings.Contains(stderr, "somebody else's problem") {
		t.Errorf("another entry's finding was reported as this teardown's drift: %q", stderr)
	}
	if strings.Contains(stderr, "just so you know") {
		t.Errorf("an info finding was reported as drift: %q", stderr)
	}

	var res rmResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout: %v\n%s", err, stdout)
	}
	if len(res.Drift) != 1 {
		t.Fatalf("drift = %v, want the one finding for this entry", res.Drift)
	}
}

// A clean teardown reports no drift and says nothing about it: a line that
// appears every time is a line a reader stops seeing.
func TestRmCleanTeardownReportsNoDrift(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	fakeGh(t, `echo '{"number":7,"state":"MERGED"}'`)
	t.Setenv("WT_ENDPOINT", rmCoord(t, worktree, nil))

	code, stdout, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if strings.Contains(stderr, "drift:") {
		t.Errorf("a clean teardown reported drift: %q", stderr)
	}
	var res rmResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout: %v\n%s", err, stdout)
	}
	if len(res.Drift) != 0 {
		t.Errorf("drift = %v, want none", res.Drift)
	}
}

// A doctor that does not answer is not evidence of drift. Saying "drift" on
// no evidence is the silent-degrade failure in the other direction, so the
// unchecked case is reported as unchecked.
func TestRmReportsAnUnverifiableTeardownAsUnverified(t *testing.T) {
	main, worktree, _ := rmFixture(t)
	fakeGh(t, `echo '{"number":7,"state":"MERGED"}'`)
	ep := rmCoord(t, worktree, map[string]func(*api.Request) *api.Response{
		"doctor": canned(&api.Response{Error: &api.Error{Code: 1, Msg: "doctor is having a day"}}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, _, stderr := runCLI(t, "rm", "--cwd", main, "--slug", "wt-1")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "could not be verified") {
		t.Errorf("stderr = %q, want the check reported as unrun", stderr)
	}
	if !strings.Contains(stderr, "wt doctor") {
		t.Errorf("stderr = %q, want the manual check named", stderr)
	}
}
