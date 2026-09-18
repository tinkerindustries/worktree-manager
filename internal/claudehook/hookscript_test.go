package claudehook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// hookscript_test.go drives the two hook scripts themselves, against real
// git repositories in t.TempDir() and a real `wt` binary. The rest of this
// package's tests cover installing and uninstalling the scripts; these
// cover what they do when Claude Code runs them, which is where the
// decisions that cost a person a worktree are made.

func shellForHook(t *testing.T) string {
	t.Helper()
	sh, err := platform.ShellPath()
	if err != nil {
		t.Skipf("no POSIX shell on this machine: %v", err)
	}
	return sh
}

// wtBinary builds the client once per test binary. The hook calls
// `wt spec path --name ... --json` and reads three fields out of the
// answer, so a fake wt would leave the two halves free to drift apart —
// which is the failure this whole file exists to catch.
func wtBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH; cannot build wt for the hook tests")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "wt")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/wt")
	cmd.Dir = filepath.Join("..", "..")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building wt: %v\n%s", err, b)
	}
	return out
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wt", "GIT_AUTHOR_EMAIL=wt@example.invalid",
		"GIT_COMMITTER_NAME=wt", "GIT_COMMITTER_EMAIL=wt@example.invalid",
		"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "gitconfig"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(t.TempDir(), "gitconfig-system"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo makes a git repository with one commit on main, and returns its
// path. spec, when non-empty, is written to wt.yaml — its presence is the
// adoption signal the hook branches on.
func repo(t *testing.T, spec string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	// The temp directory is behind a symlink on macOS (/var -> /private/var)
	// and the hook reports the realised path, so the test compares against
	// the realised one too.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	git(t, dir, "init", "--initial-branch=main", ".")
	if spec != "" {
		if err := os.WriteFile(filepath.Join(dir, "wt.yaml"), []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "first")
	return dir
}

// adoptedSpec is the smallest spec that makes a repository adopted. It
// names no worktrees block, so the defaults apply — which is the case that
// matters, because it is what every repository gets.
const adoptedSpec = `version: 1
app: hooked
resources:
  - type: port
    name: api
emit:
  descriptor:
    filename: wt-env.json
    format: json
`

// runCreate runs the create hook with the given payload and environment.
// The script is rendered exactly as install writes it, so the managed block
// naming the wt binary is the one the script reads back.
func runCreate(t *testing.T, wt, name, cwd string, env ...string) (int, string, string) {
	t.Helper()
	return runScript(t, createTemplate, wt, env,
		`{"name": `+quote(t, name)+`, "cwd": `+quote(t, cwd)+`}`)
}

func runRemove(t *testing.T, wt, path string, env ...string) (int, string, string) {
	t.Helper()
	return runScript(t, removeTemplate, wt, env, `{"worktree_path": `+quote(t, path)+`}`)
}

func quote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runScript(t *testing.T, body []byte, wt string, env []string, stdin string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(path, renderScript(body, wt), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shellForHook(t), path)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wt", "GIT_AUTHOR_EMAIL=wt@example.invalid",
		"GIT_COMMITTER_NAME=wt", "GIT_COMMITTER_EMAIL=wt@example.invalid",
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, "gitconfig"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, "gitconfig-system"))
	cmd.Env = append(cmd.Env, env...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the hook: %v", err)
	}
	return code, out.String(), errb.String()
}

// TestRegisteredCommandSurvivesShellParsing runs shellQuote's output through
// the exact mechanism Claude Code uses on a registered hook — `sh -c
// <command>`, the command as shell script text, not an argv already split
// for it — and proves a Windows-style backslash path comes out the other
// side unchanged. An unquoted path fails this: sh's escape processing
// silently drops every backslash before the hook script ever runs.
func TestRegisteredCommandSurvivesShellParsing(t *testing.T) {
	sh := shellForHook(t)
	path := `C:\Users\alex\.claude\hooks\wt-worktree-create.sh`
	cmd := exec.Command(sh, "-c", "printf '%s' "+shellQuote(path))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh -c: %v", err)
	}
	if string(out) != path {
		t.Errorf("the path came through as %q, want %q", out, path)
	}
}

// TestCreateNormalisesTheNameItIsHanded: Claude Code names a worktree after
// the task that prompted it, so the name arriving at the hook is not
// necessarily a legal slug and is nobody's choice. It is normalised rather
// than refused — the alternative costs a person their worktree.
func TestCreateNormalisesTheNameItIsHanded(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec)

	code, stdout, stderr := runCreate(t, wt, "Add The Export Button", root, "WT_HOOK_NO_ENV=1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	path := strings.TrimSpace(stdout)
	want := filepath.Join(root, ".claude", "worktrees", "add-the-export-button")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("the worktree was not created at %s: %v", path, err)
	}
	// The branch takes the normalised slug too, so the directory basename
	// the remove hook later hands to `wt rm` names the same thing.
	if got := git(t, path, "rev-parse", "--abbrev-ref", "HEAD"); got != "add-the-export-button" {
		t.Errorf("branch = %q, want the normalised slug", got)
	}
	if !strings.Contains(stderr, "normalised") {
		t.Errorf("the substitution was not stated: %q", stderr)
	}
}

// A name past the slug cap is cut rather than refused. This is the case
// from the customer report, where a 37-character generated name failed
// creation outright and the remedy — pick a shorter name — was addressed to
// somebody who was not in the room.
func TestCreateAcceptsAnOverlongGeneratedName(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec)

	const name = "print-pipeline-connector-error-with-a-very-long-tail-0b6837"
	code, stdout, stderr := runCreate(t, wt, name, root, "WT_HOOK_NO_ENV=1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("no path on stdout")
	}
	if fi, err := os.Stat(strings.TrimSpace(stdout)); err != nil || !fi.IsDir() {
		t.Fatalf("no worktree at %s: %v", stdout, err)
	}
}

// TestCreateBranchesFromTheSpecBase: the reported failure. A worktree asked
// for from inside another worktree used to branch off that worktree's
// branch, silently. It must branch from what the spec names instead.
func TestCreateBranchesFromTheSpecBase(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec+"worktrees:\n  base: main\n")
	mainHead := git(t, root, "rev-parse", "HEAD")

	// A first worktree, with a commit on it that must not be inherited.
	code, stdout, stderr := runCreate(t, wt, "first", root, "WT_HOOK_NO_ENV=1")
	if code != 0 {
		t.Fatalf("first: exit %d; stderr:\n%s", code, stderr)
	}
	first := strings.TrimSpace(stdout)
	if err := os.WriteFile(filepath.Join(first, "only-here"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, first, "add", "-A")
	git(t, first, "commit", "-m", "work in the first worktree")

	// The second is asked for from inside the first — the case that used to
	// pick up the first worktree's branch.
	code, stdout, stderr = runCreate(t, wt, "second", first, "WT_HOOK_NO_ENV=1")
	if code != 0 {
		t.Fatalf("second: exit %d; stderr:\n%s", code, stderr)
	}
	second := strings.TrimSpace(stdout)
	if got := git(t, second, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("the second worktree branched from %s, want main at %s", got, mainHead)
	}
	if _, err := os.Stat(filepath.Join(second, "only-here")); err == nil {
		t.Error("the second worktree inherited the first worktree's commit")
	}
	if !strings.Contains(stderr, "branching second from main") {
		t.Errorf("the base was not stated: %q", stderr)
	}
}

// With no origin and no base named, the default origin/main does not
// resolve. A repository with local-only branches is ordinary, so it still
// gets a worktree — from the main checkout's HEAD, never from the asking
// session's, and the fallback says so.
func TestCreateFallsBackWhenOriginMainIsAbsent(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec)

	code, stdout, stderr := runCreate(t, wt, "local-only", root, "WT_HOOK_NO_ENV=1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("no path on stdout")
	}
	if !strings.Contains(stderr, "origin/main does not resolve") {
		t.Errorf("the fallback was not stated: %q", stderr)
	}
	if !strings.Contains(stderr, "worktrees.base") {
		t.Errorf("the fallback does not name the field that would pin it: %q", stderr)
	}
}

// A base the repository named that does not resolve is a refusal, not a
// fallback: the repository asked for something specific and did not get it.
func TestCreateRefusesAnUnresolvableNamedBase(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec+"worktrees:\n  base: release/nonexistent\n")

	code, stdout, stderr := runCreate(t, wt, "doomed", root, "WT_HOOK_NO_ENV=1")
	if code == 0 {
		t.Fatalf("exit = 0, want a refusal; stdout %q", stdout)
	}
	if !strings.Contains(stderr, "worktrees.base") {
		t.Errorf("the refusal does not name the field: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", "doomed")); err == nil {
		t.Error("a worktree was left behind by a refused creation")
	}
}

// TestCreateRefusesAnExistingBranch: the hook creates branches and never
// checks out an existing one. Adopting silently is not what the caller
// asked for, and the collision surfaces much later as a rejected push.
func TestCreateRefusesAnExistingBranch(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec+"worktrees:\n  base: main\n")
	git(t, root, "branch", "taken")

	code, stdout, stderr := runCreate(t, wt, "taken", root, "WT_HOOK_NO_ENV=1")
	if code == 0 {
		t.Fatalf("exit = 0, want a refusal; stdout %q", stdout)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr = %q, want the collision named", stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", "taken")); err == nil {
		t.Error("a worktree was created onto an existing branch")
	}
}

// The remote is consulted too. A branch of this name on origin passes every
// local check and collides later; here origin is a real repository on disk,
// so ls-remote answers for real.
func TestCreateRefusesABranchThatExistsOnOrigin(t *testing.T) {
	wt := wtBinary(t)
	origin := repo(t, "")
	git(t, origin, "branch", "theirs")
	root := repo(t, adoptedSpec+"worktrees:\n  base: main\n")
	git(t, root, "remote", "add", "origin", origin)

	code, stdout, stderr := runCreate(t, wt, "theirs", root, "WT_HOOK_NO_ENV=1")
	if code == 0 {
		t.Fatalf("exit = 0, want a refusal; stdout %q", stdout)
	}
	if !strings.Contains(stderr, "already exists on origin") {
		t.Errorf("stderr = %q, want the remote collision named", stderr)
	}
}

// TestCreateNoEnvSkipsInit: WT_HOOK_NO_ENV=1 makes the worktree and stops.
// The wt binary here is one that fails if called for anything but the spec
// derivation, so a wt init would show up as a failure.
func TestCreateNoEnvSkipsInit(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, adoptedSpec+"worktrees:\n  base: main\n")

	code, stdout, stderr := runCreate(t, wt, "no-env", root, "WT_HOOK_NO_ENV=1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("no path on stdout")
	}
	if !strings.Contains(stderr, "WT_HOOK_NO_ENV=1") {
		t.Errorf("the skip was not stated: %q", stderr)
	}
	if strings.Contains(stderr, "wt init failed") {
		t.Errorf("init ran despite WT_HOOK_NO_ENV: %q", stderr)
	}
}

// An unadopted repository keeps the worktree Claude Code would have made
// itself — branched from the session's HEAD, in Claude Code's own
// directory — and is told how to change that. What is new is that the base
// is stated rather than silent.
func TestCreateUnadoptedKeepsClaudeCodesBehaviour(t *testing.T) {
	wt := wtBinary(t)
	root := repo(t, "")

	code, stdout, stderr := runCreate(t, wt, "plain", root)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	// Compared with the separators normalised. The unadopted branch builds
	// the path in the shell, so on Windows it carries git's forward slashes,
	// while the adopted branch gets a native path back from `wt spec path`.
	// Both name the same directory and Windows accepts either, so the
	// difference is a spelling and not something to assert on.
	want := filepath.ToSlash(filepath.Join(root, ".claude", "worktrees", "plain"))
	if got := filepath.ToSlash(strings.TrimSpace(stdout)); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if !strings.Contains(stderr, "branching plain from main") {
		t.Errorf("the base was not stated: %q", stderr)
	}
	if !strings.Contains(stderr, "worktree-onboarding") {
		t.Errorf("the unadopted notice is missing: %q", stderr)
	}
}

// TestRemoveDistinguishesRefusedFromUnavailable: exit 3 and exit 4 have
// opposite remedies — decide, versus install the missing tool — and used to
// report the same sentence. The fake wt here is enough: what is under test
// is the hook's reading of the status, not wt rm's policy.
func TestRemoveDistinguishesRefusedFromUnavailable(t *testing.T) {
	root := repo(t, adoptedSpec)
	tree := filepath.Join(root, ".claude", "worktrees", "gone")
	git(t, root, "worktree", "add", "-b", "gone", tree)

	fakeWt := func(t *testing.T, exit string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "wt")
		body := "#!/bin/sh\necho 'the reason wt rm gave' >&2\nexit " + exit + "\n"
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	code, _, stderr := runRemove(t, fakeWt(t, "3"), tree)
	if code == 0 {
		t.Fatal("exit = 0, want a failure")
	}
	if !strings.Contains(stderr, "safety check") || !strings.Contains(stderr, "exit 3") {
		t.Errorf("exit 3 was not reported as a refusal: %q", stderr)
	}
	if !strings.Contains(stderr, "the reason wt rm gave") {
		t.Errorf("wt rm's own reason did not come through: %q", stderr)
	}

	code, _, stderr = runRemove(t, fakeWt(t, "4"), tree)
	if code == 0 {
		t.Fatal("exit = 0, want a failure")
	}
	if !strings.Contains(stderr, "could not check") || !strings.Contains(stderr, "exit 4") {
		t.Errorf("exit 4 was not reported as an unavailable check: %q", stderr)
	}
	if strings.Contains(stderr, "safety check found something worth keeping") {
		t.Errorf("exit 4 was reported as a refusal: %q", stderr)
	}
}
