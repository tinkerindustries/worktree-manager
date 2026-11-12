// Package treecheck holds the checks that must pass before a worktree is
// destroyed: uncommitted changes, unpushed commits, and what gh reports
// about the branch's pull request.
//
// Three callers run them — `wt rm`, `wt cleanup` and the coordinator's
// scheduled sweep. Each keeps its own policy: rm refuses on an open PR,
// cleanup requires a merged one, the sweep logs the skip. The mechanism is
// here so the three cannot disagree about what git and gh said.
//
// Every check takes a Runner, so the client passes the real git and gh and
// the coordinator passes its seam.
//
// The one import beyond the standard library is internal/platform, for
// the "path for an external tool" conversion WorktreeRemove needs: a
// realised path is this process's spelling, not git's.
package treecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// Runner runs one git or gh command in dir and returns its output.
type Runner func(dir string, args ...string) ([]byte, error)

// Git runs one git command in dir. It returns stdout, and the error names
// the command and carries git's stderr.
//
// Resolved through platform.HelperCommand for the same reason gh is: the
// coordinator's sweep runs these checks from wtd, and doctor reports git
// reachable when LookHelper finds it. A call site that searched PATH alone
// would fail on a machine whose git is only in a fallback directory, while
// doctor reported it fine.
func Git(dir string, args ...string) ([]byte, error) {
	cmd, err := platform.HelperCommand("git", args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrGitMissing, err)
	}
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.Stderr, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// Gh runs one gh command in dir and returns its combined output, which is
// where gh puts both its JSON and its "no pull requests found". A missing
// binary is an error, never an empty answer.
func Gh(dir string, args ...string) ([]byte, error) {
	// Resolved through platform.HelperCommand, not exec.LookPath: the
	// coordinator's scheduled cleanup sweep calls this from wtd, whose
	// supervisor-supplied PATH does not include the /opt/homebrew/bin
	// Homebrew installs gh into — and gh shells out to git, which it finds
	// on the PATH it is given.
	cmd, err := platform.HelperCommand("gh", args...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrGhMissing, err)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

// ErrGhMissing is the answer when the gh binary is in neither the process
// PATH nor a known install directory. Callers match on it with errors.Is;
// the wrapped detail names the locations searched.
var ErrGhMissing = errors.New("gh is not installed or not on PATH")

// ErrGitMissing is the answer when the git binary is in neither the
// process PATH nor a known install directory. The wrapped detail names the
// locations searched.
var ErrGitMissing = errors.New("git is not installed or not on PATH")

// ErrNoUpstream is Unpushed's answer when the branch has no upstream. An
// absent upstream is a stop, not a pass: nothing proves the commits are
// anywhere else.
var ErrNoUpstream = errors.New("the branch has no upstream")

// Result is a tree check's outcome. Lines is empty when the check passes,
// and holds the offending paths or commits when it does not.
type Result struct{ Lines []string }

// OK reports whether the check passed.
func (r Result) OK() bool { return len(r.Lines) == 0 }

// First renders at most n of the offending lines for a message.
func (r Result) First(n int) string {
	shown := r.Lines
	if len(shown) > n {
		shown = shown[:n]
	}
	return strings.Join(shown, "; ")
}

// Uncommitted reports the worktree's uncommitted changes.
func Uncommitted(dir string, git Runner) (Result, error) {
	out, err := git(dir, "status", "--porcelain")
	if err != nil {
		return Result{}, err
	}
	return Result{Lines: nonEmptyLines(string(out))}, nil
}

// Unpushed reports the commits on HEAD that the upstream does not have. An
// absent upstream returns ErrNoUpstream.
func Unpushed(dir string, git Runner) (Result, error) {
	out, err := git(dir, "log", "@{u}..HEAD", "--oneline")
	if err != nil {
		if isNoUpstream(err.Error() + " " + string(out)) {
			return Result{}, ErrNoUpstream
		}
		return Result{}, err
	}
	return Result{Lines: nonEmptyLines(string(out))}, nil
}

// isNoUpstream recognises git's several ways of saying the branch has no
// upstream configured.
func isNoUpstream(text string) bool {
	for _, s := range []string{"no upstream", "@{u}", "unknown revision"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// OnBranch returns the worktree's branch name. A detached HEAD returns
// ("", false): a branch is what a pull request points at.
func OnBranch(dir string, git Runner) (string, bool) {
	out, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	name := strings.TrimSpace(string(out))
	if err != nil || name == "" || name == "HEAD" {
		return "", false
	}
	return name, true
}

// State is what gh reports about a branch's pull request.
type State string

const (
	StateOpen   State = "OPEN"
	StateDraft  State = "DRAFT"
	StateMerged State = "MERGED"
	StateClosed State = "CLOSED"
	// StateNone is gh answering that the branch has no pull request.
	StateNone State = "NONE"
)

// PR is gh's answer for one branch.
type PR struct {
	State  State
	Number int
}

// Open reports whether the pull request still points at a live branch, which
// is the question `wt rm` refuses on. A draft counts as open.
func (p PR) Open() bool { return p.State == StateOpen || p.State == StateDraft }

// Merged reports whether the pull request landed, which is the question
// cleanup and the sweep require a yes to.
func (p PR) Merged() bool { return p.State == StateMerged }

// Describe renders the answer as prose for a message: "PR #42 is merged",
// or "no pull request for this branch".
func (p PR) Describe() string {
	if p.State == StateNone {
		return "no pull request for this branch"
	}
	return fmt.Sprintf("PR #%d is %s", p.Number, strings.ToLower(string(p.State)))
}

// UnavailableError is a check that could not run. A check that cannot run
// fails closed, and the caller decides what that means for its verb.
type UnavailableError struct {
	// NotInstalled and NotAuthenticated pick the remedy; neither set means
	// the tool ran and could not answer.
	NotInstalled     bool
	NotAuthenticated bool
	Detail           string
}

func (e *UnavailableError) Error() string { return e.Detail }

// PRState asks gh about the pull request for the branch checked out in dir.
// It reports StateNone when gh answers that there is no pull request, and an
// *UnavailableError when gh could not answer at all.
func PRState(dir string, gh Runner) (PR, error) {
	out, err := gh(dir, "pr", "view", "--json", "number,state")
	if errors.Is(err, ErrGhMissing) {
		return PR{}, &UnavailableError{NotInstalled: true, Detail: err.Error()}
	}
	if err == nil {
		var pr struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		}
		if jerr := json.Unmarshal(out, &pr); jerr == nil && pr.Number > 0 {
			return PR{State: State(strings.ToUpper(pr.State)), Number: pr.Number}, nil
		}
	}
	text := strings.TrimSpace(string(out))
	lower := strings.ToLower(text)
	if strings.Contains(lower, "no pull requests found") {
		return PR{State: StateNone}, nil
	}
	if isAuthFailure(lower) {
		return PR{}, &UnavailableError{NotAuthenticated: true, Detail: text}
	}
	return PR{}, &UnavailableError{Detail: text}
}

// AuthStatus is the gh gate: the binary exists and is authenticated. It
// returns nil when gh can answer, and an *UnavailableError when it cannot.
func AuthStatus(gh Runner) error {
	out, err := gh("", "auth", "status")
	if errors.Is(err, ErrGhMissing) {
		return &UnavailableError{NotInstalled: true, Detail: err.Error()}
	}
	if err != nil {
		// gh auth status exits non-zero exactly when the CLI is not
		// authenticated (or is broken, which is the same gate): the message
		// is not reliable across gh versions, the exit code is.
		return &UnavailableError{NotAuthenticated: true, Detail: strings.TrimSpace(string(out))}
	}
	return nil
}

// isAuthFailure recognises gh's unauthenticated answers.
func isAuthFailure(lower string) bool {
	for _, s := range []string{"auth", "authenticate", "login"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// WorktreeRemove runs `git worktree remove <dir>` from the repository's
// main checkout. git refusing is signal that a check missed something, and
// the refusal is reported as itself. The error carries git's own words.
//
// The one exception is a tree with submodules. git refuses every such tree
// unless forced, clean or not, because removing it deletes the submodule
// clones kept under its git dir's modules directory. For that tree alone,
// wt runs the check git skips when forced (status with
// --ignore-submodules=none must be empty) and checks every clone under
// modules for commits no remote holds. Only when both pass does it run the
// removal with a single --force, which still refuses a locked worktree.
//
// This is the one check whose directory is also a command-line argument,
// so it is the one that must convert. Every caller here holds a realised
// path — that is what the registry records and what containment
// comparisons are built on — and on Windows realisation produces the
// extended-length \\?\C:\... spelling, which git answers with "is not a
// working tree". platform.ExternalPath is the documented conversion out
// (it is the identity on unix), applied once here so the three callers
// cannot disagree about it.
//
// The tree's ignored files are moved aside before git runs and deleted in
// parallel after it succeeds (setaside.go), which is most of the time a
// removal takes on Windows. When git refuses they are moved back. leftover
// is the trash directory a successful removal could not finish deleting,
// with the reason, or "": the worktree is gone either way, so it is
// reported beside the success rather than as a failure.
func WorktreeRemove(dir string, git Runner) (leftover string, err error) {
	dir = platform.ExternalPath(dir)
	aside := setAsideIgnored(dir, git)
	if err := removeTree(dir, git); err != nil {
		text := err.Error()
		if rerr := aside.restore(); rerr != nil {
			text += "; " + rerr.Error()
		}
		return "", errors.New(text)
	}
	return aside.discard(), nil
}

// removeTree runs the removal itself: unforced, and forced only for a tree
// with submodules that passes submodulesSafeToDelete.
func removeTree(dir string, git Runner) error {
	from := removeFrom(dir, git)
	out, err := git(from, "worktree", "remove", dir)
	if err == nil {
		return nil
	}
	refusal := gitWords(out, err)
	modules, ok := submoduleStore(dir, git)
	if !ok {
		return errors.New(refusal)
	}
	if err := submodulesSafeToDelete(dir, modules, git); err != nil {
		return err
	}
	if out, err := git(from, "worktree", "remove", "--force", dir); err != nil {
		return errors.New(gitWords(out, err))
	}
	return nil
}

// gitWords is the text of a failed git command: its stderr, or the error
// when git printed nothing.
func gitWords(out []byte, err error) string {
	if text := strings.TrimSpace(string(out)); text != "" {
		return text
	}
	return err.Error()
}

// submoduleStore returns the worktree's own modules directory, where git
// keeps the clones of the submodules initialised in this tree, and whether
// it exists. Its presence is what makes git refuse an unforced removal.
func submoduleStore(dir string, git Runner) (string, bool) {
	out, err := git(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", false
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", false
	}
	modules := filepath.Join(filepath.FromSlash(gitDir), "modules")
	info, err := os.Stat(modules)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return modules, true
}

// submodulesSafeToDelete is the check a forced removal of a tree with
// submodules must pass first. The tree must be clean by git's own measure
// for an unforced removal, and no clone under modules may hold a commit
// that is on no remote, because the removal deletes the clone.
func submodulesSafeToDelete(dir, modules string, git Runner) error {
	out, err := git(dir, "status", "--porcelain", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("it has submodules, and wt could not check it is clean: %w", err)
	}
	if dirty := (Result{Lines: nonEmptyLines(string(out))}); !dirty.OK() {
		return fmt.Errorf("it has submodules, which git removes only with --force, and wt forces that only for a clean tree; this one contains modified or untracked files: %s", dirty.First(5))
	}
	repos, err := submoduleClones(modules)
	if err != nil {
		return fmt.Errorf("it has submodules, and wt could not list their clones under %s: %w", modules, err)
	}
	for _, repo := range repos {
		name, _ := filepath.Rel(modules, repo)
		name = filepath.ToSlash(name)
		out, err := git(repo, "--git-dir="+repo, "log", "--oneline", "HEAD", "--branches", "--not", "--remotes")
		if err != nil {
			return fmt.Errorf("it has submodules, and wt could not check submodule %s for unpushed commits: %w", name, err)
		}
		if local := (Result{Lines: nonEmptyLines(string(out))}); !local.OK() {
			return fmt.Errorf("submodule %s has %d commit(s) on no remote, and removing the worktree deletes its clone: %s; push them or remove the worktree by hand with git worktree remove --force", name, len(local.Lines), local.First(3))
		}
	}
	return nil
}

// submoduleClones lists the git repositories under a modules directory.
// A submodule's name may contain slashes, so a clone can sit at any depth,
// and a nested submodule's clone sits under its parent clone's own modules
// directory.
func submoduleClones(modules string) ([]string, error) {
	var repos []string
	err := filepath.WalkDir(modules, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || path == modules {
			return nil
		}
		if isGitDir(path) {
			repos = append(repos, path)
			nested := filepath.Join(path, "modules")
			if info, err := os.Stat(nested); err == nil && info.IsDir() {
				more, err := submoduleClones(nested)
				if err != nil {
					return err
				}
				repos = append(repos, more...)
			}
			return fs.SkipDir
		}
		return nil
	})
	return repos, err
}

// isGitDir reports whether path is a git directory: a HEAD file beside an
// objects directory.
func isGitDir(path string) bool {
	head, err := os.Stat(filepath.Join(path, "HEAD"))
	if err != nil || head.IsDir() {
		return false
	}
	objects, err := os.Stat(filepath.Join(path, "objects"))
	return err == nil && objects.IsDir()
}

// removeFrom is the directory `git worktree remove` is run from, which
// must not be the directory it is about to delete.
//
// On unix it can be: a directory is unlinkable while it is a process's
// current directory. Windows refuses to remove a directory any process is
// standing in, so running git from inside the tree failed a removal that
// was otherwise fine — "error: failed to delete '<tree>': Permission
// denied" against a clean, merged, perfectly removable worktree, from
// `wt rm`, `wt cleanup` and the coordinator's sweep alike.
//
// The repository's main checkout is the one directory certainly inside the
// repository and certainly not this tree, so the removal runs from there.
// It is used only when git confirms it is a work tree — a linked worktree
// of a bare repository has no such directory — and the tree itself is the
// fallback, which is still correct everywhere but Windows.
func removeFrom(dir string, git Runner) string {
	out, err := git(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return dir
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return dir
	}
	common = filepath.FromSlash(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	main := filepath.Dir(filepath.Clean(common))
	if main == "" || main == "." || main == dir {
		return dir
	}
	inside, err := git(main, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return dir
	}
	return main
}

// nonEmptyLines splits a command's output into its non-empty lines.
func nonEmptyLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
