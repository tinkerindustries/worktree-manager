package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeExe creates an executable file in dir and returns its path. On
// Windows an executable is one with a PATHEXT extension, not one with a
// mode bit, so the name carries .bat there.
func writeExe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".bat"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

// TestLookHelperPrefersPath proves the fallback list never shadows a
// binary the process PATH already resolves. /usr/local/bin is
// admin-writable on macOS, so a prepend would be a privilege question.
func TestLookHelperPrefersPath(t *testing.T) {
	onPath, fallback := t.TempDir(), t.TempDir()
	want := writeExe(t, onPath, "wt-helper-probe")
	writeExe(t, fallback, "wt-helper-probe")
	t.Setenv("PATH", onPath)

	got, err := lookHelper("wt-helper-probe", []string{fallback})
	if err != nil {
		t.Fatalf("lookHelper: %v", err)
	}
	if got != want {
		t.Errorf("resolved %q, want the PATH copy %q", got, want)
	}
}

// TestLookHelperFallsBackToKnownDir is the bug itself: the binary exists,
// the process PATH does not name its directory, and the lookup must still
// find it. This is docker in /usr/local/bin under launchd's PATH.
func TestLookHelperFallsBackToKnownDir(t *testing.T) {
	stripped, fallback := t.TempDir(), t.TempDir()
	want := writeExe(t, fallback, "wt-helper-probe")
	t.Setenv("PATH", stripped)

	got, err := lookHelper("wt-helper-probe", []string{fallback})
	if err != nil {
		t.Fatalf("lookHelper: %v", err)
	}
	if got != want {
		t.Errorf("resolved %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolved %q, want an absolute path", got)
	}
}

// TestLookHelperSearchesDirsInOrder proves the first matching fallback
// directory wins, so the list's order is the tie-break between two
// installs.
func TestLookHelperSearchesDirsInOrder(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	want := writeExe(t, first, "wt-helper-probe")
	writeExe(t, second, "wt-helper-probe")
	t.Setenv("PATH", t.TempDir())

	got, err := lookHelper("wt-helper-probe", []string{first, second})
	if err != nil {
		t.Fatalf("lookHelper: %v", err)
	}
	if got != want {
		t.Errorf("resolved %q, want the first directory's copy %q", got, want)
	}
}

// TestLookHelperMissingNamesWhatWasSearched is the diagnosis the report
// asked for: a refusal that says which PATH and which directories were
// looked in, so "docker is installed and working" and "the daemon cannot
// see it" are distinguishable without ps eww.
func TestLookHelperMissingNamesWhatWasSearched(t *testing.T) {
	stripped, fallback := t.TempDir(), t.TempDir()
	t.Setenv("PATH", stripped)

	_, err := lookHelper("wt-helper-absent", []string{fallback})
	if err == nil {
		t.Fatal("resolved a binary that does not exist")
	}
	var nf *HelperNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error is %T, want *HelperNotFoundError", err)
	}
	msg := err.Error()
	for _, want := range []string{"wt-helper-absent", stripped, fallback} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// TestHelperDirsAreAbsolute guards the list this platform actually
// compiles in. A relative entry would resolve against the coordinator's
// working directory, which is not a location anything is installed to.
func TestHelperDirsAreAbsolute(t *testing.T) {
	for _, d := range helperDirs() {
		if !filepath.IsAbs(d) {
			t.Errorf("helper directory %q is not absolute", d)
		}
	}
}

// TestHelperDirsCoverTheReportedGaps pins the two directories the bug was
// about on macOS: Docker Desktop's /usr/local/bin symlink and Homebrew's
// /opt/homebrew/bin, neither of which launchd puts on a LaunchAgent's
// PATH.
func TestHelperDirsCoverTheReportedGaps(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the reported gaps are launchd's PATH on macOS")
	}
	dirs := helperDirs()
	for _, want := range []string{"/usr/local/bin", "/opt/homebrew/bin"} {
		if !slices.Contains(dirs, want) {
			t.Errorf("helperDirs() = %v, missing %q", dirs, want)
		}
	}
}

// TestHelperDirsEnvReplacesBuiltinList covers the escape hatch for an
// install location no built-in list can know about.
func TestHelperDirsEnvReplacesBuiltinList(t *testing.T) {
	custom := t.TempDir()
	t.Setenv(HelperDirsEnv, custom)

	got := HelperDirs()
	if len(got) != 1 || got[0] != custom {
		t.Fatalf("HelperDirs() = %v, want [%s]", got, custom)
	}
}

// TestHelperDirsEnvEmptySearchesPathOnly proves the empty value is a real
// answer rather than "unset". It is how a caller says a binary is absent
// even though a known install directory holds one.
func TestHelperDirsEnvEmptySearchesPathOnly(t *testing.T) {
	t.Setenv(HelperDirsEnv, "")
	if got := HelperDirs(); len(got) != 0 {
		t.Fatalf("HelperDirs() = %v, want none", got)
	}

	fallback := t.TempDir()
	writeExe(t, fallback, "wt-helper-probe")
	t.Setenv("PATH", t.TempDir())

	if _, err := lookHelper("wt-helper-probe", HelperDirs()); err == nil {
		t.Error("resolved a binary that only the emptied fallback list held")
	}
}

// TestHelperDirsEnvUnsetUsesBuiltinList pins that an unset variable leaves
// the platform's own list in place, which is the whole point of the fix.
func TestHelperDirsEnvUnsetUsesBuiltinList(t *testing.T) {
	os.Unsetenv(HelperDirsEnv)
	if got, want := len(HelperDirs()), len(helperDirs()); got != want {
		t.Errorf("HelperDirs() has %d entries, want the built-in list's %d", got, want)
	}
}

// writeScript creates an executable script in dir, with the body written
// in the shell the platform actually runs.
func writeScript(t *testing.T, dir, name, sh, bat string) string {
	t.Helper()
	body := sh
	if runtime.GOOS == "windows" {
		name += ".bat"
		body = bat
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

// TestHelperCommandFindsSiblingOfHelper is the reported bug: colima
// resolves limactl through its own PATH at runtime, and both live in the
// directory LookHelper already searched. Resolving the binary is not
// enough — the child must be given a PATH that leads with the directory it
// came from. The plain exec.Command control proves the test discriminates.
func TestHelperCommandFindsSiblingOfHelper(t *testing.T) {
	install := t.TempDir()
	writeScript(t, install, "wt-helper-sibling",
		"#!/bin/sh\necho reached\n",
		"@echo off\r\necho reached\r\n")
	bin := writeScript(t, install, "wt-helper-main",
		"#!/bin/sh\nexec wt-helper-sibling\n",
		"@echo off\r\ncall wt-helper-sibling.bat\r\n")

	t.Setenv("PATH", t.TempDir())
	t.Setenv(HelperDirsEnv, install)

	if _, err := exec.Command(bin).Output(); err == nil {
		t.Fatal("the sibling resolved under the stripped PATH; the fixture no longer reproduces the bug")
	}

	cmd, err := HelperCommand("wt-helper-main")
	if err != nil {
		t.Fatalf("HelperCommand: %v", err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the helper: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "reached" {
		t.Errorf("helper printed %q, want %q", got, "reached")
	}
}

// TestHelperCommandKeepsTheRestOfTheEnvironment proves the child gets the
// coordinator's environment with PATH amended and nothing else touched:
// the docker seam runs on DOCKER_HOST reaching the child.
func TestHelperCommandKeepsTheRestOfTheEnvironment(t *testing.T) {
	install := t.TempDir()
	writeExe(t, install, "wt-helper-probe")
	t.Setenv("PATH", t.TempDir())
	t.Setenv(HelperDirsEnv, install)
	t.Setenv("WT_HELPER_TEST_VAR", "carried")

	cmd, err := HelperCommand("wt-helper-probe")
	if err != nil {
		t.Fatalf("HelperCommand: %v", err)
	}
	if !slices.Contains(cmd.Env, "WT_HELPER_TEST_VAR=carried") {
		t.Errorf("the child's environment dropped WT_HELPER_TEST_VAR: %v", cmd.Env)
	}
}

// TestHelperEnvPrepends covers the PATH arithmetic on its own: the
// helper's directory leads, the caller's PATH follows in order, and a
// directory already leading is not repeated — this runs in a resident
// process, and a repeated entry would grow PATH on every call.
func TestHelperEnvPrepends(t *testing.T) {
	sep := string(os.PathListSeparator)
	for _, tc := range []struct {
		name, path, dir, want string
	}{
		{"prepends", "/usr/bin" + sep + "/bin", "/opt/homebrew/bin", "PATH=/opt/homebrew/bin" + sep + "/usr/bin" + sep + "/bin"},
		{"empty path", "", "/opt/homebrew/bin", "PATH=/opt/homebrew/bin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := helperEnv([]string{"HOME=/home/x"}, tc.path, tc.dir)
			if len(got) != 2 || got[1] != tc.want {
				t.Fatalf("helperEnv = %v, want [HOME=/home/x %s]", got, tc.want)
			}
		})
	}

	t.Run("already leading", func(t *testing.T) {
		path := "/opt/homebrew/bin" + sep + "/usr/bin"
		env := []string{"PATH=" + path}
		if got := helperEnv(env, path, "/opt/homebrew/bin"); len(got) != 1 {
			t.Errorf("helperEnv repeated a leading directory: %v", got)
		}
	})
}

// TestHelperErrorCarriesStderr is the second half of the report: Output()
// populates ExitError.Stderr, and a helper that fails from the inside puts
// the diagnosis there. "colima list: exit status 1" alone left a reader
// with nowhere to go.
func TestHelperErrorCarriesStderr(t *testing.T) {
	install := t.TempDir()
	bin := writeScript(t, install, "wt-helper-fails",
		"#!/bin/sh\necho 'error retrieving instances' >&2\nexit 1\n",
		"@echo off\r\necho error retrieving instances 1>&2\r\nexit /b 1\r\n")

	_, err := exec.Command(bin).Output()
	if err == nil {
		t.Fatal("the fixture exited 0")
	}
	got := HelperError("wt-helper-fails", err).Error()
	if !strings.Contains(got, "error retrieving instances") {
		t.Errorf("HelperError = %q, want it to carry the child's stderr", got)
	}
	if !strings.Contains(got, "wt-helper-fails") {
		t.Errorf("HelperError = %q, want it to name the command", got)
	}
}

// TestHelperErrorWithoutStderr leaves a silent failure as it was, rather
// than appending an empty colon.
func TestHelperErrorWithoutStderr(t *testing.T) {
	err := HelperError("wt-probe", errors.New("boom"))
	if got := err.Error(); got != "wt-probe: boom" {
		t.Errorf("HelperError = %q, want %q", got, "wt-probe: boom")
	}
}
