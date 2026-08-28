package platform

// helper.go resolves the external helper binaries the coordinator shells
// out to. It exists because wtd is a resident process started by a
// supervisor, and a supervisor's environment is not the user's shell
// environment. launchd starts a LaunchAgent with PATH=/usr/bin:/bin:
// /usr/sbin:/sbin, which holds git and lsof but not docker
// (/usr/local/bin), colima or gh (/opt/homebrew/bin). A plain
// exec.LookPath inside the daemon therefore reports a binary missing that
// the user's own shell resolves, and the driver turns that into an
// "install Docker Desktop" instruction for a machine where Docker is
// already installed and running.
//
// LookHelper searches the process PATH first and a per-platform list of
// well-known install directories only when that fails. The order is
// deliberate: the fallback list never shadows a binary the caller's PATH
// already resolves, so a user who has put a particular docker ahead on
// their PATH keeps it. /usr/local/bin is admin-writable on macOS, which is
// the other reason it is a fallback rather than a prepend.
//
// This is the whole fix for the supervisor's stripped environment. Nothing
// writes PATH into a plist or a unit file: the resolution happens at call
// time, so it covers a hand-run wtd as well as a supervised one and takes
// effect on upgrade without a reinstall.
//
// A built-in list cannot know about a genuinely custom install location,
// so WT_HELPER_DIRS replaces it: an OS-separated list of directories, and
// the empty string to search nothing beyond PATH. That is the escape hatch
// for an exotic docker, and it is also the only way to express "this
// binary is absent" to a test, since emptying PATH no longer does it.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HelperNotFoundError is LookHelper's answer when a binary is in neither
// the process PATH nor any known install directory. It names both halves
// of the search, because the diagnosis a user needs is which locations
// were looked in — not a bare "not found".
type HelperNotFoundError struct {
	// Name is the binary that was searched for.
	Name string
	// PathEnv is the PATH the process was running with.
	PathEnv string
	// Dirs are the well-known directories searched after PATH.
	Dirs []string
}

// Error names the binary, the PATH searched and the fallback directories.
func (e *HelperNotFoundError) Error() string {
	path := e.PathEnv
	if path == "" {
		path = "(empty)"
	}
	if len(e.Dirs) == 0 {
		return fmt.Sprintf("%s was not found on this process's PATH (%s), and %s left no install directories to search",
			e.Name, path, HelperDirsEnv)
	}
	return fmt.Sprintf("%s was not found on this process's PATH (%s) or in any known install directory (%s)",
		e.Name, path, strings.Join(e.Dirs, ", "))
}

// LookHelper resolves a helper binary to an absolute path: the process
// PATH first, then the platform's known install directories. The returned
// path is absolute, so callers pass it to exec.Command directly rather
// than re-resolving the bare name against the child's PATH.
func LookHelper(name string) (string, error) {
	return lookHelper(name, HelperDirs())
}

// HelperDirs is the fallback list LookHelper actually searches:
// WT_HELPER_DIRS when it is set, and the platform's known install
// directories otherwise. An empty WT_HELPER_DIRS is a real answer — search
// nothing beyond PATH — which is why this reads the variable's presence
// rather than its truthiness.
func HelperDirs() []string {
	if v, ok := os.LookupEnv(HelperDirsEnv); ok {
		return filepath.SplitList(v)
	}
	return helperDirs()
}

// HelperDirsEnv names the variable that replaces the built-in fallback
// list.
const HelperDirsEnv = "WT_HELPER_DIRS"

// lookHelper is LookHelper's testable core, with the fallback directories
// injected. exec.LookPath does both halves of the work: with a bare name
// it searches PATH, and with a path containing a separator it checks that
// one file for executability — including the PATHEXT extensions on
// Windows, so the same code resolves docker and docker.exe.
func lookHelper(name string, dirs []string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range dirs {
		if p, err := exec.LookPath(filepath.Join(d, name)); err == nil {
			return p, nil
		}
	}
	return "", &HelperNotFoundError{Name: name, PathEnv: os.Getenv("PATH"), Dirs: dirs}
}

// homeDir is helperDirs' best effort at the user's home directory. A
// helper directory under an unresolvable home is simply not searched.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// underHome joins each relative element to the home directory, dropping
// them all when there is no home to join to.
func underHome(elems ...string) []string {
	h := homeDir()
	if h == "" {
		return nil
	}
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		out = append(out, filepath.Join(h, e))
	}
	return out
}
