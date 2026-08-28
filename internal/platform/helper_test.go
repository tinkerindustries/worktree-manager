package platform

import (
	"errors"
	"os"
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
