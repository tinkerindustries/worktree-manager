package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// TestSpecPathName: --name takes a caller-supplied name and normalises it,
// so a hook handed a name nobody typed still gets a worktree. The
// substitution is stated on stderr; stdout stays the answer.
func TestSpecPathName(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))

	code, stdout, stderr := runCLI(t, "spec", "path", "--name", "Add The Export Button")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	want := filepath.Join(root, ".claude", "worktrees", "add-the-export-button")
	if got := strings.TrimSpace(stdout); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if !strings.Contains(stderr, "normalised") {
		t.Errorf("stderr = %q, want the substitution stated", stderr)
	}
}

// A name that is already a slug passes through untouched and silently: the
// note is for the caller whose name was altered. The 37-character name from
// the customer report is the case that matters — it is legal under the
// current cap and must survive intact.
func TestSpecPathNameLeavesALegalNameAlone(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	const name = "print-pipeline-connector-error-0b6837"
	code, stdout, stderr := runCLI(t, "spec", "path", "--name", name, "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	var got SpecPathResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not the result object: %v (%q)", err, stdout)
	}
	if got.Slug != name {
		t.Errorf("slug = %q, want %q unchanged", got.Slug, name)
	}
	if strings.Contains(stderr, "normalised") {
		t.Errorf("an unchanged name was reported as normalised: %q", stderr)
	}
}

// --json is the hook's form: the slug it must use downstream, the path and
// the base revision, in one call.
func TestSpecPathJSON(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))

	code, stdout, stderr := runCLI(t, "spec", "path", "--slug", "brisk-otter", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	var got SpecPathResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not the result object: %v (%q)", err, stdout)
	}
	if got.Slug != "brisk-otter" {
		t.Errorf("slug = %q", got.Slug)
	}
	if want := filepath.Join(root, ".claude", "worktrees", "brisk-otter"); got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	// plain-app names no base, so the answer is the default rather than
	// whatever the asking session had checked out.
	if got.Base != spec.DefaultWorktreeBase {
		t.Errorf("base = %q, want the default %q", got.Base, spec.DefaultWorktreeBase)
	}
}

// Exactly one of --slug and --name: they differ in whether an illegal value
// is refused, so a caller passing both has not decided which it wants.
func TestSpecPathRefusesBothOrNeither(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	for _, args := range [][]string{
		{"spec", "path"},
		{"spec", "path", "--slug", "brisk-otter", "--name", "Brisk Otter"},
	} {
		code, stdout, stderr := runCLI(t, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, ExitUsage)
		}
		if stdout != "" {
			t.Errorf("%v: wrote to stdout: %q", args, stdout)
		}
		if !strings.Contains(stderr, "exactly one of --slug and --name") {
			t.Errorf("%v: stderr = %q", args, stderr)
		}
	}
}

// A name with nothing to keep is refused rather than replaced: the tool
// never names a worktree out of nothing.
func TestSpecPathNameRefusesAnEmptyResult(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	code, stdout, stderr := runCLI(t, "spec", "path", "--name", "!!!")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if stdout != "" {
		t.Errorf("a refused name wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "cannot be normalised") {
		t.Errorf("stderr = %q", stderr)
	}
}

// --slug stays strict, and its remedy now names the way through.
func TestSpecPathSlugRemedyNamesName(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	code, _, stderr := runCLI(t, "spec", "path", "--slug", "Brisk Otter")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "--name") {
		t.Errorf("the remedy does not name the normalising flag: %q", stderr)
	}
}
