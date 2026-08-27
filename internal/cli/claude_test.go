package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunClaudeInstallAndUninstall drives the verb end to end against a
// --prefix directory, which is what keeps the test off the machine's real
// ~/.claude.
func TestRunClaudeInstallAndUninstall(t *testing.T) {
	prefix := t.TempDir()

	code, stdout, stderr := runCLI(t, "claude", "install", "--prefix", prefix, "--json")
	if code != ExitOK {
		t.Fatalf("install exit = %d, want 0\nstderr:\n%s", code, stderr)
	}
	var result claudeResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("install --json did not print one JSON object: %v\n%s", err, stdout)
	}
	if result.Operation != "install" {
		t.Errorf("operation = %q, want install", result.Operation)
	}
	if result.Dir != prefix {
		t.Errorf("dir = %q, want %q", result.Dir, prefix)
	}
	if result.SkillDir == "" {
		t.Error("install reported no skill directory, but the skill is installed by default")
	}
	for _, name := range []string{"wt-worktree-create.sh", "wt-worktree-remove.sh"} {
		if _, err := os.Stat(filepath.Join(prefix, "hooks", name)); err != nil {
			t.Errorf("install did not write %s: %v", name, err)
		}
	}

	code, stdout, stderr = runCLI(t, "claude", "uninstall", "--prefix", prefix, "--json")
	if code != ExitOK {
		t.Fatalf("uninstall exit = %d, want 0\nstderr:\n%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("uninstall --json did not print one JSON object: %v\n%s", err, stdout)
	}
	if result.Operation != "uninstall" {
		t.Errorf("operation = %q, want uninstall", result.Operation)
	}
	for _, name := range []string{"wt-worktree-create.sh", "wt-worktree-remove.sh"} {
		if _, err := os.Stat(filepath.Join(prefix, "hooks", name)); err == nil {
			t.Errorf("uninstall left %s behind", name)
		}
	}
}

// TestRunClaudeRefusalIsExitThree proves a hook wt did not write is a
// refusal rather than a silent replacement, and that the message names the
// flag that would overrule it.
func TestRunClaudeRefusalIsExitThree(t *testing.T) {
	prefix := t.TempDir()
	settings := `{"hooks": {"WorktreeCreate": [{"hooks": [{"type": "command", "command": "/opt/theirs.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(prefix, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, "claude", "install", "--prefix", prefix)
	if code != ExitRefused {
		t.Errorf("install over a foreign hook exit = %d, want 3", code)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("the refusal does not name --force:\n%s", stderr)
	}

	code, _, _ = runCLI(t, "claude", "install", "--prefix", prefix, "--force")
	if code != ExitOK {
		t.Errorf("forced install exit = %d, want 0", code)
	}
}

func TestRunClaudeUsage(t *testing.T) {
	code, _, _ := runCLI(t, "claude")
	if code != ExitUsage {
		t.Errorf("bare claude exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "claude", "wibble")
	if code != ExitUsage {
		t.Errorf("unknown claude verb exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "claude", "install", "extra")
	if code != ExitUsage {
		t.Errorf("install with a positional argument exit = %d, want 2", code)
	}
}

// TestInstalledHooksAreValidShell runs the written scripts through the
// shell's own parser. They are shipped as text and never compiled, so a
// syntax error would otherwise reach a user's machine intact.
func TestInstalledHooksAreValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	prefix := t.TempDir()
	if code, _, stderr := runCLI(t, "claude", "install", "--prefix", prefix); code != ExitOK {
		t.Fatalf("install exit = %d\nstderr:\n%s", code, stderr)
	}
	for _, name := range []string{"wt-worktree-create.sh", "wt-worktree-remove.sh"} {
		path := filepath.Join(prefix, "hooks", name)
		out, err := exec.Command(sh, "-n", path).CombinedOutput()
		if err != nil {
			t.Errorf("%s is not valid shell: %v\n%s", name, err, out)
		}
	}
}
