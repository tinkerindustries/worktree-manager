package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// waitEndpoint waits for an in-process server to write endpoint.json
// into its store root (the server writes it once the listener is bound)
// and returns its base URL — the race-free way a test learns the port of
// a 127.0.0.1:0 listener.
func waitEndpoint(t *testing.T, storeRoot string) string {
	t.Helper()
	path := api.EndpointPath(storeRoot)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ep, err := api.ReadEndpoint(path); err == nil {
			return ep.BaseURL
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the coordinator never wrote the endpoint file")
	return ""
}

// runCLI runs the client with the given args and returns the exit code plus
// the two streams.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestExitCodeConstants(t *testing.T) {
	if ExitOK != 0 || ExitFailure != 1 || ExitUsage != 2 || ExitRefused != 3 || ExitUnavailable != 4 || ExitUnreachable != 5 {
		t.Errorf("exit codes are not the fixed set: %d %d %d %d %d %d", ExitOK, ExitFailure, ExitUsage, ExitRefused, ExitUnavailable, ExitUnreachable)
	}
}

func TestRunHelp(t *testing.T) {
	code, stdout, _ := runCLI(t, "help")
	if code != ExitOK {
		t.Errorf("help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "spec validate") || !strings.Contains(stdout, "spec explain") {
		t.Errorf("help output lacks the verbs:\n%s", stdout)
	}
	code, _, _ = runCLI(t)
	if code != ExitUsage {
		t.Errorf("no args exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "bogus")
	if code != ExitUsage {
		t.Errorf("unknown verb exit = %d, want 2", code)
	}
}

func TestRunSpecValidateValid(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "fixtures", "compose-app", "wt.yaml")
	code, stdout, _ := runCLI(t, "spec", "validate", path)
	if code != ExitOK {
		t.Fatalf("validate exit = %d, want 0; stderr: %s", code, stderrOf(t))
	}
	if !strings.Contains(stdout, "valid:") {
		t.Errorf("stdout = %q, want a valid: line", stdout)
	}
}

// stderrOf is a helper so the test above reads cleanly.
func stderrOf(t *testing.T) string { return "" }

func TestRunSpecValidateJSON(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "fixtures", "plain-app", "wt.yaml")
	code, stdout, stderr := runCLI(t, "spec", "validate", "--json", path)
	if code != ExitOK {
		t.Fatalf("validate exit = %d, want 0; stderr: %s", code, stderr)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if v["valid"] != true {
		t.Errorf("json valid = %v, want true", v["valid"])
	}
	if strings.Contains(stdout, "\n\n") || strings.Count(stdout, "{") != 1 {
		t.Errorf("--json must print exactly one JSON object and nothing else:\n%s", stdout)
	}
}

func TestRunSpecValidateInvalid(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "specs", "cycle.yaml")
	code, stdout, stderr := runCLI(t, "spec", "validate", path)
	if code != ExitFailure {
		t.Errorf("invalid spec exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "template cycle") || !strings.Contains(stderr, "fix:") {
		t.Errorf("stderr does not name the field, reason and remedy:\n%s", stderr)
	}
	if stdout != "" {
		t.Errorf("non-json invalid validate wrote to stdout: %q", stdout)
	}
}

func TestRunSpecValidateInvalidJSON(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "specs", "overlong-name.yaml")
	code, stdout, _ := runCLI(t, "spec", "validate", "--json", path)
	if code != ExitFailure {
		t.Errorf("exit = %d, want 1", code)
	}
	var v struct {
		Valid  bool   `json:"valid"`
		Field  string `json:"field"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if v.Valid {
		t.Error("json valid = true, want false")
	}
	if !strings.Contains(v.Field, "resources[0]") || !strings.Contains(v.Reason, "cap is 63") {
		t.Errorf("json does not name the field and reason: %+v", v)
	}
}

func TestRunSpecValidateNotAdopted(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runCLI(t, "spec", "validate", dir)
	if code != ExitUnavailable {
		t.Errorf("not-adopted exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "not adopted") && !strings.Contains(stderr, "no wt.yaml") {
		t.Errorf("stderr does not report not adopted:\n%s", stderr)
	}
}

func TestRunSpecValidateUsage(t *testing.T) {
	code, _, _ := runCLI(t, "spec", "validate", "--bogus")
	if code != ExitUsage {
		t.Errorf("unknown flag exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "spec", "validate", "a", "b")
	if code != ExitUsage {
		t.Errorf("two paths exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "spec", "nonsense")
	if code != ExitUsage {
		t.Errorf("unknown spec verb exit = %d, want 2", code)
	}
}

// chdir changes the working directory for the duration of the test. spec
// explain finds the spec by walking up from cwd.
func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunSpecExplainTable(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	code, stdout, stderr := runCLI(t, "spec", "explain",
		"--slot", "1", "--slug", "alpha",
		"--base", "api=4200", "--base", "proxy=4200",
		"--home", "/Users/test", "--worktree", "/Users/test/wt/alpha")
	if code != ExitOK {
		t.Fatalf("explain exit = %d, want 0; stderr: %s", code, stderr)
	}
	// group of size 2 off base 4200: slot 1 gives proxy 4202, api 4203.
	for _, want := range []string{"api", "port", "4203", "compose-app-alpha-1", "compose-app-alpha-1-test", "proxy", "4202"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table lacks %q:\n%s", want, stdout)
		}
	}
}

func TestRunSpecExplainJSON(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	code, stdout, stderr := runCLI(t, "spec", "explain", "--json",
		"--slot", "2", "--slug", "alpha",
		"--base", "api=4200", "--base", "proxy=4200",
		"--home", "/Users/test", "--worktree", "/Users/test/wt/alpha")
	if code != ExitOK {
		t.Fatalf("explain exit = %d, want 0; stderr: %s", code, stderr)
	}
	var v struct {
		App       string `json:"app"`
		Slug      string `json:"slug"`
		Slot      int    `json:"slot"`
		Resources map[string]struct {
			Type  string `json:"type"`
			Value any    `json:"value"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if v.App != "compose-app" || v.Slug != "alpha" || v.Slot != 2 {
		t.Errorf("header = %+v", v)
	}
	// group of size 2 off base 4200: slot 2 gives proxy 4204, api 4205.
	if v.Resources["api"].Value != float64(4205) {
		t.Errorf("api = %v, want 4205", v.Resources["api"].Value)
	}
	if v.Resources["compose"].Value != "compose-app-alpha-2" {
		t.Errorf("compose = %v", v.Resources["compose"].Value)
	}
}

// TestRunSpecExplainDisjoint is exit criterion 3 at the binary level: two
// explain runs, slot 1 and slot 2, produce disjoint resource tables.
func TestRunSpecExplainDisjoint(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	run := func(slot, slug string) map[string]any {
		t.Helper()
		code, stdout, stderr := runCLI(t, "spec", "explain", "--json",
			"--slot", slot, "--slug", slug,
			"--base", "api=4200", "--base", "proxy=4200",
			"--home", "/Users/test", "--worktree", "/Users/test/wt/"+slug)
		if code != ExitOK {
			t.Fatalf("explain slot %s exit = %d: %s", slot, code, stderr)
		}
		var v struct {
			Resources map[string]struct {
				Value any `json:"value"`
			} `json:"resources"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
			t.Fatalf("stdout: %v", err)
		}
		out := map[string]any{}
		for name, r := range v.Resources {
			out[name] = r.Value
		}
		return out
	}
	table1 := run("1", "brisk-otter")
	table2 := run("2", "brisk-otter")
	for name, v1 := range table1 {
		if v2, ok := table2[name]; ok && v1 == v2 {
			t.Errorf("resource %q resolves to %v in both tables; tables are not disjoint", name, v1)
		}
	}
}

func TestRunSpecExplainUsage(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	// Missing slot.
	code, _, _ := runCLI(t, "spec", "explain", "--slug", "alpha", "--base", "api=4200")
	if code != ExitUsage {
		t.Errorf("missing --slot exit = %d, want 2", code)
	}
	// Missing slug.
	code, _, _ = runCLI(t, "spec", "explain", "--slot", "1")
	if code != ExitUsage {
		t.Errorf("missing --slug exit = %d, want 2", code)
	}
	// Base for a non-port resource.
	code, _, stderr := runCLI(t, "spec", "explain", "--slot", "1", "--slug", "alpha",
		"--base", "compose=4200", "--home", "/Users/test", "--worktree", "/Users/test/wt")
	if code != ExitUsage {
		t.Errorf("base for non-port exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "no port resource") {
		t.Errorf("stderr does not name the problem:\n%s", stderr)
	}
	// Missing base for a port resource.
	code, _, stderr = runCLI(t, "spec", "explain", "--slot", "1", "--slug", "alpha",
		"--base", "api=4200", "--home", "/Users/test", "--worktree", "/Users/test/wt")
	if code != ExitUsage {
		t.Errorf("missing base exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "proxy") {
		t.Errorf("stderr does not name the port resource missing a base:\n%s", stderr)
	}
	// Invalid slot.
	code, _, _ = runCLI(t, "spec", "explain", "--slot", "0", "--slug", "alpha",
		"--base", "api=4200", "--base", "proxy=4200", "--home", "/Users/test", "--worktree", "/Users/test/wt")
	if code != ExitUsage {
		t.Errorf("slot 0 exit = %d, want 2", code)
	}
	// Invalid slug.
	code, _, _ = runCLI(t, "spec", "explain", "--slot", "1", "--slug", "Not A Slug",
		"--base", "api=4200", "--base", "proxy=4200", "--home", "/Users/test", "--worktree", "/Users/test/wt")
	if code != ExitUsage {
		t.Errorf("invalid slug exit = %d, want 2", code)
	}
}

func TestRunSpecExplainNotAdopted(t *testing.T) {
	chdir(t, t.TempDir())
	code, _, stderr := runCLI(t, "spec", "explain", "--slot", "1", "--slug", "alpha",
		"--home", "/Users/test", "--worktree", "/Users/test/wt")
	if code != ExitUnavailable {
		t.Errorf("not-adopted explain exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "wt.yaml") {
		t.Errorf("stderr does not name the missing spec:\n%s", stderr)
	}
}

// TestWriteErrorEmptyRemedy pins the structural rule: an error that reaches
// the user with an empty remedy is reported as a defect.
func TestWriteErrorEmptyRemedy(t *testing.T) {
	var buf bytes.Buffer
	WriteError(&buf, New(ExitFailure, "something broke", ""))
	if !strings.Contains(buf.String(), "internal defect") {
		t.Errorf("empty remedy was not flagged as a defect: %q", buf.String())
	}
	var buf2 bytes.Buffer
	WriteError(&buf2, New(ExitFailure, "something broke", "run: wt spec validate"))
	if strings.Contains(buf2.String(), "internal defect") || !strings.Contains(buf2.String(), "fix: run: wt spec validate") {
		t.Errorf("remedy not printed: %q", buf2.String())
	}
}

// TestWriteErrorWrapped pins that fmt.Errorf-wrapped errors still resolve
// to the exit-code type.
func TestWriteErrorWrapped(t *testing.T) {
	var buf bytes.Buffer
	inner := New(ExitRefused, "refused", "clean the tree first")
	WriteError(&buf, fmt.Errorf("rm: %w", inner))
	if !strings.Contains(buf.String(), "refused") || !strings.Contains(buf.String(), "clean the tree first") {
		t.Errorf("wrapped error lost its message or remedy: %q", buf.String())
	}
}

// TestSpecPathDefault: `wt spec path` prints where a worktree of this
// repository goes — an absolute path, anchored at the repository root and
// not at cwd, so the answer is the same from anywhere inside the tree.
func TestSpecPathDefault(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	code, stdout, stderr := runCLI(t, "spec", "path", "--slug", "brisk-otter")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	want := filepath.Join(root, ".claude", "worktrees", "brisk-otter")
	if strings.TrimSpace(stdout) != want {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(stdout), want)
	}
}

// TestSpecPathOverride: the vm-app fixture puts its trees outside the
// repository, and `spec path` resolves that template against the main
// checkout — the one place the `../` in it is given a meaning.
func TestSpecPathOverride(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "vm-app"))
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "vm-app"))
	code, stdout, stderr := runCLI(t, "spec", "path", "--slug", "brisk-otter")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	want := filepath.Join(filepath.Dir(root), "vm-app-worktrees", "brisk-otter")
	if strings.TrimSpace(stdout) != want {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(stdout), want)
	}
}

// TestSpecPathRefusesABadSlug: the slug is checked before anything is
// resolved, as a usage error — the path is only as valid as the slug in it.
func TestSpecPathRefusesABadSlug(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	code, stdout, stderr := runCLI(t, "spec", "path", "--slug", "Brisk Otter")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if stdout != "" {
		t.Errorf("a refused slug wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "is not a valid slug") {
		t.Errorf("stderr = %q, want the slug rule named", stderr)
	}
}

// openTestStore opens a coordinator store for a test and closes it when
// the test ends. The handle holds wt.db and its -wal/-shm sidecars open;
// leaving it open leaks it on every platform and, on Windows, stops
// t.TempDir() removing the directory it lives in — which is how the leak
// was noticed.
func openTestStore(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.Open(root)
	if err != nil {
		t.Fatalf("opening the store at %s: %v", root, err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store at %s: %v", root, err)
		}
	})
	return st
}

// setTestHome points the tool's home-directory resolution at dir.
//
// Every home lookup in this tool is os.UserHomeDir, which reads
// USERPROFILE on Windows and HOME everywhere else. Setting HOME alone left
// the Windows runs resolving {home} against the real user's directory,
// so a fixture's state paths were written outside the test's own tree —
// silently, because the directory exists. Both variables are set, so the
// isolation holds whichever one the platform reads.
func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
