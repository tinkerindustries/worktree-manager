package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// adoptedTestRepo is a two-worktree repository that has adopted the tooling:
// a committed wt.yaml, a CLAUDE.md in the main checkout, and a descriptor in
// the linked worktree recording a shared store. It is built with real git
// commands in t.TempDir(), the plan.md §4 fixture layer.
type adoptedTestRepo struct {
	main        string
	wt1         string
	sharedStore string
}

// tempDir is t.TempDir() with symlinks already resolved. git reports
// --show-toplevel resolved, so a fixture under the raw t.TempDir() compares
// unequal on macOS, where the temp root is under /var, a symlink to
// /private/var.
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

func buildAdoptedTestRepo(t *testing.T) *adoptedTestRepo {
	t.Helper()
	root := tempDir(t)
	ar := &adoptedTestRepo{
		main:        filepath.Join(root, "repo"),
		wt1:         filepath.Join(root, "wt1"),
		sharedStore: filepath.Join(root, "shared", "db.sqlite"),
	}
	if err := os.MkdirAll(ar.main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, ar.main, "init", "-q", "-b", "main", ".")
	git(t, ar.main, "config", "user.email", "fixture@localhost")
	git(t, ar.main, "config", "user.name", "fixture")

	spec := `version: 1
app: guard-app
slots:
  max: 8
resources:
  - type: port
    name: api
emit:
  descriptor:
    filename: wt-env.yaml
    format: yaml
`
	writeFile(t, filepath.Join(ar.main, "wt.yaml"), spec)
	writeFile(t, filepath.Join(ar.main, "CLAUDE.md"), "parent copy\n")
	git(t, ar.main, "add", "wt.yaml", "CLAUDE.md")
	git(t, ar.main, "commit", "-q", "-m", "adopt")
	git(t, ar.main, "worktree", "add", "-q", ar.wt1, "-b", "wt1")

	desc := fmt.Sprintf(`version: 1
app: guard-app
slug: wt1
slot: 1
path: %s
standalone: false
description: ""
resources:
  api:
    type: port
    value: 4101
state: {}
shared:
  - name: %s
    impact: writes are visible to every worktree and the main checkout
extras: {}
`, ar.wt1, ar.sharedStore)
	writeFile(t, filepath.Join(ar.wt1, "wt-env.yaml"), desc)
	return ar
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunGuardDeniesWriteToPrimaryCheckout is exit criterion 2 at the binary
// level: `wt guard` refuses the write and the denial names the correct root
// verbatim. Denied is exit 3, the "refused by a safety check" code.
func TestRunGuardDeniesWriteToPrimaryCheckout(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	writePath := filepath.Join(ar.main, "README.md")
	code, stdout, stderr := runCLI(t, "guard", "--json", "--cwd", ar.wt1, "--path", writePath)
	if code != ExitRefused {
		t.Fatalf("exit = %d, want 3 (refused); stderr: %s", code, stderr)
	}
	var v struct {
		Allowed      bool   `json:"allowed"`
		Reason       string `json:"reason"`
		WorktreeRoot string `json:"worktree_root"`
		Outcome      string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if v.Allowed {
		t.Error("allowed = true, want false")
	}
	if !strings.Contains(v.Reason, writePath) || !strings.Contains(v.Reason, ar.wt1) {
		t.Errorf("denial does not name the path and the correct root verbatim: %q", v.Reason)
	}
	if v.WorktreeRoot != ar.wt1 || v.Outcome != "linked-worktree" {
		t.Errorf("verdict = %+v", v)
	}
	if !strings.Contains(stderr, "denied") || !strings.Contains(stderr, ar.wt1) {
		t.Errorf("stderr does not carry the denial for a hook that surfaces only stderr:\n%s", stderr)
	}
	if strings.Count(stdout, "{") != 1 || strings.Contains(stdout, "\n\n") {
		t.Errorf("--json must print exactly one JSON object and nothing else:\n%s", stdout)
	}
}

// TestRunGuardDeniesReadCLAUDEOutside is exit criterion 3 at the binary
// level.
func TestRunGuardDeniesReadCLAUDEOutside(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	code, stdout, _ := runCLI(t, "guard", "--json", "--cwd", ar.wt1, "--tool", "Read", "--path", filepath.Join(ar.main, "CLAUDE.md"))
	if code != ExitRefused {
		t.Fatalf("exit = %d, want 3", code)
	}
	v := decodeVerdict(t, stdout)
	if v.WorktreeRoot != ar.wt1 || !strings.Contains(v.Reason, ar.wt1) {
		t.Errorf("denial does not name the correct root %s: %s", ar.wt1, stdout)
	}
}

// TestRunGuardAllowsInside: a write inside the worktree exits 0.
func TestRunGuardAllowsInside(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	code, stdout, _ := runCLI(t, "guard", "--json", "--cwd", ar.wt1, "--path", filepath.Join(ar.wt1, "new", "file.txt"))
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	var v struct {
		Allowed bool `json:"allowed"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Allowed {
		t.Errorf("allowed = false: %s", stdout)
	}
}

// TestRunGuardFailsOpen is exit criterion 4 at the binary level: pipes,
// $(...), variables and globs are allowed, with one verdict per call.
func TestRunGuardFailsOpen(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	inputs := []string{
		`{"command": "echo hi | tee out.txt"}`,
		`{"command": "cat $(find . -name '*.go') > out.txt"}`,
		`{"command": "echo $HOME > out.txt"}`,
		`{"command": "cat *.md > out.txt"}`,
		`{"file_path": "*.md"}`,
		`{"file_path": "/repo/$x/file"}`,
	}
	for _, input := range inputs {
		code, stdout, _ := runCLI(t, "guard", "--json", "--cwd", ar.wt1, "--tool", "Bash", "--input", input)
		if code != ExitOK {
			t.Errorf("input %s: exit = %d, want 0 (fail open); stdout: %s", input, code, stdout)
		}
		var v struct {
			Allowed bool `json:"allowed"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
			t.Fatal(err)
		}
		if !v.Allowed {
			t.Errorf("input %s: allowed = false, want true", input)
		}
	}
}

// TestRunGuardStdinPayload: a PreToolUse payload piped on stdin is the
// primary input surface, the shape phase 7's hook generates against.
func TestRunGuardStdinPayload(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	payload := fmt.Sprintf(`{"tool_name": "Write", "tool_input": {"file_path": %q}}`,
		filepath.Join(ar.main, "README.md"))
	old := guardStdin
	guardStdin = strings.NewReader(payload)
	defer func() { guardStdin = old }()
	code, stdout, _ := runCLI(t, "guard", "--json", "--cwd", ar.wt1)
	if code != ExitRefused {
		t.Fatalf("exit = %d, want 3", code)
	}
	v := decodeVerdict(t, stdout)
	if v.WorktreeRoot != ar.wt1 || !strings.Contains(v.Reason, ar.wt1) {
		t.Errorf("denial does not name the root %s: %s", ar.wt1, stdout)
	}
}

// TestRunGuardUsage: the verb is loud when the caller forgets the input.
func TestRunGuardUsage(t *testing.T) {
	code, _, _ := runCLI(t, "guard")
	if code != ExitUsage {
		t.Errorf("no input exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "guard", "--path", "/x", "--input", `{"file_path": "/y"}`)
	if code != ExitUsage {
		t.Errorf("--path with --input exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "guard", "--path", "/x", "extra")
	if code != ExitUsage {
		t.Errorf("positional argument exit = %d, want 2", code)
	}
}

// TestRunShowTable: the descriptor reads back as a table, with every value
// an agent might otherwise hardcode present (05-delivery.md §7).
func TestRunShowTable(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	code, stdout, stderr := runCLI(t, "show", "--cwd", ar.wt1)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	for _, want := range []string{"guard-app", "wt1", "4101", "api", ar.sharedStore, "writes are visible"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table lacks %q:\n%s", want, stdout)
		}
	}
}

// TestRunShowJSON: one JSON object, found true, carrying the descriptor.
func TestRunShowJSON(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	code, stdout, stderr := runCLI(t, "show", "--json", "--cwd", ar.wt1)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	var v struct {
		Found      bool `json:"found"`
		Descriptor struct {
			App       string `json:"app"`
			Slug      string `json:"slug"`
			Slot      int    `json:"slot"`
			Resources map[string]struct {
				Type  string `json:"type"`
				Value any    `json:"value"`
			} `json:"resources"`
		} `json:"descriptor"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("stdout: %v\n%s", err, stdout)
	}
	if !v.Found || v.Descriptor.App != "guard-app" || v.Descriptor.Slug != "wt1" || v.Descriptor.Slot != 1 {
		t.Errorf("verdict = %+v", v)
	}
	if v.Descriptor.Resources["api"].Value != float64(4101) {
		t.Errorf("api = %#v, want 4101", v.Descriptor.Resources["api"].Value)
	}
}

// TestRunShowDistinguishableCases: show says something useful in each of the
// cases it can distinguish — not a repository, not adopted, primary checkout,
// linked worktree without a descriptor (naming wt init).
func TestRunShowDistinguishableCases(t *testing.T) {
	ar := buildAdoptedTestRepo(t)

	// Not a repository: exit 4.
	code, _, stderr := runCLI(t, "show", "--cwd", t.TempDir())
	if code != ExitUnavailable {
		t.Errorf("not-a-repo exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "not inside a git work tree") {
		t.Errorf("stderr: %s", stderr)
	}

	// A repository with no spec: not adopted, exit 4.
	plain := t.TempDir()
	git(t, plain, "init", "-q", "-b", "main", ".")
	code, _, stderr = runCLI(t, "show", "--cwd", plain)
	if code != ExitUnavailable {
		t.Errorf("not-adopted exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "wt.yaml") {
		t.Errorf("stderr does not name the missing spec: %s", stderr)
	}

	// A primary checkout: slot 0, unmanaged, exit 0.
	code, stdout, _ := runCLI(t, "show", "--cwd", ar.main)
	if code != ExitOK {
		t.Errorf("primary-checkout exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "slot 0") {
		t.Errorf("stdout does not say slot 0 is unmanaged: %s", stdout)
	}

	// A linked worktree with no descriptor: names wt init, exit 4.
	if err := os.Remove(filepath.Join(ar.wt1, "wt-env.yaml")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(t, "show", "--cwd", ar.wt1)
	if code != ExitUnavailable {
		t.Errorf("no-descriptor exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "wt init") {
		t.Errorf("stderr does not name wt init: %s", stderr)
	}
	if !strings.Contains(stdout, "wt init") {
		t.Errorf("stdout does not name wt init: %s", stdout)
	}
}

// TestRunShowNewerVersion: a descriptor from a newer schema version is
// refused to be read, naming the upgrade (05-delivery.md §8).
func TestRunShowNewerVersion(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	dpath := filepath.Join(ar.wt1, "wt-env.yaml")
	data, err := os.ReadFile(dpath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dpath, strings.Replace(string(data), "version: 1", "version: 2", 1))
	code, stdout, stderr := runCLI(t, "show", "--cwd", ar.wt1)
	if code != ExitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "newer") || !strings.Contains(stderr, "upgrade wt") {
		t.Errorf("stderr does not name the upgrade: %s", stderr)
	}
	if !strings.Contains(stdout, "newer") {
		t.Errorf("stdout does not name the upgrade: %s", stdout)
	}
}

// TestRunShowUnparseable: an unparseable descriptor is reported with wt init
// named as the rebuild, and is never overwritten — show writes nothing.
func TestRunShowUnparseable(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	dpath := filepath.Join(ar.wt1, "wt-env.yaml")
	writeFile(t, dpath, "version: 1\n  broken: [")
	code, _, stderr := runCLI(t, "show", "--cwd", ar.wt1)
	if code != ExitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "wt init") {
		t.Errorf("stderr does not name wt init as the rebuild: %s", stderr)
	}
	data, err := os.ReadFile(dpath)
	if err != nil || !strings.Contains(string(data), "broken") {
		t.Errorf("show overwrote the unparseable descriptor (data=%q, err=%v)", data, err)
	}
}

// TestRunGuardDeletedWorktree: the guard reports the deleted directory as
// itself rather than misclassifying.
func TestRunGuardDeletedWorktree(t *testing.T) {
	ar := buildAdoptedTestRepo(t)
	if err := os.RemoveAll(ar.wt1); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "guard", "--cwd", ar.wt1, "--path", filepath.Join(ar.wt1, "x"))
	if code != ExitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no longer exists") || !strings.Contains(stderr, "git worktree prune") {
		t.Errorf("stderr does not report the deleted worktree with a remedy: %s", stderr)
	}
}

// TestRunHelpListsNewVerbs: the help names guard and show.
func TestRunHelpListsNewVerbs(t *testing.T) {
	code, stdout, _ := runCLI(t, "help")
	if code != ExitOK {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(stdout, "wt guard") || !strings.Contains(stdout, "wt show") {
		t.Errorf("help lacks the phase-1 verbs:\n%s", stdout)
	}
}

// guardVerdictJSON is the shape `wt guard --json` prints.
type guardVerdictJSON struct {
	Allowed      bool   `json:"allowed"`
	Reason       string `json:"reason"`
	WorktreeRoot string `json:"worktree_root"`
	Outcome      string `json:"outcome"`
}

// decodeVerdict decodes the one JSON object guard prints.
//
// The verdict is read as JSON rather than searched as text: a Windows path
// in a JSON string carries doubled backslashes, so a substring test for
// C:\Users\u\wt1 over the raw stdout is false for output that names it
// perfectly well.
func decodeVerdict(t *testing.T, stdout string) guardVerdictJSON {
	t.Helper()
	var v guardVerdictJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	return v
}
