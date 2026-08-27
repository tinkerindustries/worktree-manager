package identity

import (
	"errors"

	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// fixtures is the real-repository layer of plan.md §4: classification is
// tested against real git repositories created in t.TempDir(), never against
// mocked git output — the bug M1 exists to prevent (D4) was a misreading of
// what git reports, and a test built on a mock of the same misreading passes
// while the bug survives (01-identity.md §8).
//
// The six fixtures of plan.md §4:
//
//  1. a plain repository
//  2. a repository with two linked worktrees
//  3. a clone
//  4. a worktree whose directory has been removed
//  5. each of the above reached through a symlink
type fixtures struct {
	root string

	plain string // fixture 1: the plain repository
	main  string // fixture 2: the two-worktree repository's main checkout
	wt1   string // fixture 2: first linked worktree
	wt2   string // fixture 2: second linked worktree
	clone string // fixture 3: a clone

	removedPath string // fixture 4: the removed worktree's directory path

	linkPlain string // fixture 5: symlink to plain
	linkMain  string // fixture 5: symlink to the main checkout
	linkWT1   string // fixture 5: symlink to wt1
	linkClone string // fixture 5: symlink to the clone
	linkGone  string // fixture 5: symlink to the removed worktree's path
}

// tempDir is t.TempDir() with symlinks already resolved. git reports
// --show-toplevel in resolved form, so a fixture built under the raw
// t.TempDir() compares unequal on macOS, where the temp root sits under /var,
// a symlink to /private/var. On Linux the two are the same string and the
// difference never shows.
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

func buildFixtures(t *testing.T) *fixtures {
	t.Helper()
	root := tempDir(t)
	fx := &fixtures{root: root}

	fx.plain = filepath.Join(root, "plain")
	initRepo(t, fx.plain)

	// Fixture 2: a repository with two linked worktrees.
	fx.main = filepath.Join(root, "main")
	initRepo(t, fx.main)
	runGitIn(t, fx.main, "worktree", "add", "-q", filepath.Join(root, "wt1"), "-b", "w1")
	runGitIn(t, fx.main, "worktree", "add", "-q", filepath.Join(root, "wt2"), "-b", "w2")
	fx.wt1 = filepath.Join(root, "wt1")
	fx.wt2 = filepath.Join(root, "wt2")

	// Fixture 3: a clone.
	fx.clone = filepath.Join(root, "clone")
	run(t, "git", "clone", "-q", fx.plain, fx.clone)

	// Fixture 4: a worktree whose directory has been removed. Its path is
	// recorded before the removal; the registration stays in the git dir
	// until pruned, but the directory itself is gone.
	fx.removedPath = filepath.Join(root, "gone")
	runGitIn(t, fx.main, "worktree", "add", "-q", fx.removedPath, "-b", "gone")
	if err := os.RemoveAll(fx.removedPath); err != nil {
		t.Fatal(err)
	}

	// Fixture 5: each of the above reached through a symlink.
	fx.linkPlain = filepath.Join(root, "link-plain")
	fx.linkMain = filepath.Join(root, "link-main")
	fx.linkWT1 = filepath.Join(root, "link-wt1")
	fx.linkClone = filepath.Join(root, "link-clone")
	fx.linkGone = filepath.Join(root, "link-gone")
	for link, target := range map[string]string{
		fx.linkPlain: fx.plain,
		fx.linkMain:  fx.main,
		fx.linkWT1:   fx.wt1,
		fx.linkClone: fx.clone,
		fx.linkGone:  fx.removedPath,
	} {
		symlinkOrSkip(t, target, link)
	}
	return fx
}

// initRepo creates a git repository with one commit, so worktree add and
// clone have something to check out.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, dir, "init", "-q", "-b", "main", ".")
	runGitIn(t, dir, "config", "user.email", "fixture@localhost")
	runGitIn(t, dir, "config", "user.name", "fixture")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, dir, "add", "file.txt")
	runGitIn(t, dir, "commit", "-q", "-m", "init")
}

func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// classifyRow is one row of the fixture table.
type classifyRow struct {
	name    string
	dir     string
	outcome Outcome
	wantErr bool
}

// TestClassifyFixtures is exit criterion 1: the six fixtures classify
// correctly, including through symlinks.
func TestClassifyFixtures(t *testing.T) {
	fx := buildFixtures(t)
	rows := []classifyRow{
		// Fixture 1: a plain repository.
		{"plain", fx.plain, PrimaryCheckout, false},
		// Fixture 2: the main checkout is a primary checkout; both linked
		// worktrees are linked worktrees.
		{"main checkout", fx.main, PrimaryCheckout, false},
		{"linked worktree 1", fx.wt1, LinkedWorktree, false},
		{"linked worktree 2", fx.wt2, LinkedWorktree, false},
		// Fixture 3: a clone is indistinguishable from a primary checkout
		// until the standalone declaration is made (01-identity.md §2.1).
		{"clone", fx.clone, PrimaryCheckout, false},
		{"clone with standalone", fx.clone, StandaloneClone, false},
		// Fixture 4: a worktree whose directory has been removed is
		// reported as itself, never translated into "not a repository"
		// (M1 §7).
		{"removed worktree", fx.removedPath, 0, true},
		// Fixture 5: each of the above reached through a symlink. git
		// resolves the link, so the outcome and the root match the real
		// path's.
		{"plain via symlink", fx.linkPlain, PrimaryCheckout, false},
		{"main checkout via symlink", fx.linkMain, PrimaryCheckout, false},
		{"linked worktree via symlink", fx.linkWT1, LinkedWorktree, false},
		{"clone via symlink", fx.linkClone, PrimaryCheckout, false},
		{"removed worktree via symlink", fx.linkGone, 0, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			// The standalone row carries its declaration; everything else
			// runs without one.
			standalone := row.name == "clone with standalone"
			cls, err := Classify(row.dir, standalone)
			if row.wantErr {
				if err == nil {
					t.Fatalf("Classify(%s) succeeded (outcome %v), want an error", row.dir, cls.Outcome)
				}
				if _, ok := err.(*DeletedWorktreeError); !ok {
					t.Errorf("Classify(%s) error = %T %v, want *DeletedWorktreeError", row.dir, err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Classify(%s): %v", row.dir, err)
			}
			if cls.Outcome != row.outcome {
				t.Errorf("Classify(%s).Outcome = %v, want %v", row.dir, cls.Outcome, row.outcome)
			}
			// The acting root must equal the real path of the directory,
			// not a symlinked spelling of it.
			real, err := filepath.EvalSymlinks(row.dir)
			if err != nil {
				t.Fatal(err)
			}
			if cls.WorktreeRoot != real {
				t.Errorf("Classify(%s).WorktreeRoot = %q, want %q", row.dir, cls.WorktreeRoot, real)
			}
		})
	}
}

// TestClassifyCarriesTheReach pins 01-identity.md §3: the main checkout path
// is a field on the classification result, never a function, so a caller's
// reach for it is visible in review.
func TestClassifyCarriesTheReach(t *testing.T) {
	fx := buildFixtures(t)

	cls, err := Classify(fx.wt1, false)
	if err != nil {
		t.Fatal(err)
	}
	if cls.Outcome != LinkedWorktree {
		t.Fatalf("outcome = %v, want linked-worktree", cls.Outcome)
	}
	if cls.MainCheckoutPath != fx.main {
		t.Errorf("MainCheckoutPath = %q, want %q", cls.MainCheckoutPath, fx.main)
	}
	if cls.GitCommonDir != filepath.Join(fx.main, ".git") {
		t.Errorf("GitCommonDir = %q, want %q", cls.GitCommonDir, filepath.Join(fx.main, ".git"))
	}
	if cls.WorktreeRoot != fx.wt1 {
		t.Errorf("WorktreeRoot = %q, want the worktree's own root %q", cls.WorktreeRoot, fx.wt1)
	}

	// A primary checkout's main checkout path is itself.
	cls, err = Classify(fx.main, false)
	if err != nil {
		t.Fatal(err)
	}
	if cls.Outcome != PrimaryCheckout || cls.MainCheckoutPath != fx.main {
		t.Errorf("primary: outcome=%v main=%q, want primary-checkout %q", cls.Outcome, cls.MainCheckoutPath, fx.main)
	}

	// A standalone clone's main checkout path is itself.
	cls, err = Classify(fx.clone, true)
	if err != nil {
		t.Fatal(err)
	}
	if cls.Outcome != StandaloneClone || cls.MainCheckoutPath != fx.clone {
		t.Errorf("standalone: outcome=%v main=%q, want standalone-clone %q", cls.Outcome, cls.MainCheckoutPath, fx.clone)
	}
}

// TestClassifyStandaloneInLinkedWorktree pins the M1 §7 refusal: the
// combination is meaningless and is probably a copied descriptor.
func TestClassifyStandaloneInLinkedWorktree(t *testing.T) {
	fx := buildFixtures(t)
	_, err := Classify(fx.wt1, true)
	var want *StandaloneInLinkedWorktreeError
	if err == nil {
		t.Fatal("Classify succeeded, want StandaloneInLinkedWorktreeError")
	}
	if !errors.As(err, &want) {
		t.Errorf("error = %T %v, want *StandaloneInLinkedWorktreeError", err, err)
	}
}

// TestClassifyNotARepository: git rev-parse fails, and the outcome is
// NotARepository, not an error.
func TestClassifyNotARepository(t *testing.T) {
	dir := t.TempDir()
	cls, err := Classify(dir, false)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if cls.Outcome != NotARepository {
		t.Errorf("outcome = %v, want not-a-repository", cls.Outcome)
	}
	if cls.WorktreeRoot != "" || cls.MainCheckoutPath != "" {
		t.Errorf("a not-a-repository classification must carry no paths: %+v", cls)
	}
}

// TestWorktreeRoot: the acting root is always the current worktree's own
// root, from --show-toplevel.
func TestWorktreeRoot(t *testing.T) {
	fx := buildFixtures(t)
	for _, dir := range []string{fx.plain, fx.main, fx.wt1, fx.clone} {
		root, err := WorktreeRoot(dir)
		if err != nil {
			t.Fatalf("WorktreeRoot(%s): %v", dir, err)
		}
		if root != dir {
			t.Errorf("WorktreeRoot(%s) = %q, want %q", dir, root, dir)
		}
	}
	if _, err := WorktreeRoot(t.TempDir()); err == nil {
		t.Error("WorktreeRoot in a non-repository succeeded, want an error")
	}
}

// TestOutcomeNames pins the verdict strings the guard and show emit.
func TestOutcomeNames(t *testing.T) {
	want := map[Outcome]string{
		NotARepository:  "not-a-repository",
		PrimaryCheckout: "primary-checkout",
		LinkedWorktree:  "linked-worktree",
		StandaloneClone: "standalone-clone",
	}
	for o, s := range want {
		if o.String() != s {
			t.Errorf("Outcome(%d).String() = %q, want %q", o, o.String(), s)
		}
	}
}

// symlinkOrSkip creates a symlink, skipping the test when the platform
// will not let it.
//
// Creating a symlink on Windows needs SeCreateSymbolicLinkPrivilege, which
// an ordinary account holds only with Developer Mode on; without it
// os.Symlink fails with "A required privilege is not held by the client".
// A test about symlink semantics cannot run there, and a skip naming the
// reason is the honest answer — the same call the mapped-drive probe in
// realpath_windows_test.go makes. Every other platform, and Windows CI,
// creates the link and runs the test.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create a symlink on this machine (%v); creating one needs SeCreateSymbolicLinkPrivilege, which Developer Mode grants", err)
		}
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}
