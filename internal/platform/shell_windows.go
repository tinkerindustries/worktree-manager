package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// gitBashCandidates are where Git for Windows puts its POSIX shell, in the
// order they are tried after PATH. The usr\bin copy is the full one; the
// bin\ copy is the wrapper the Git Bash shortcut launches.
var gitBashCandidates = []string{
	`Git\usr\bin\sh.exe`,
	`Git\bin\sh.exe`,
}

// shellPath resolves the POSIX shell on Windows: sh.exe from PATH, then the
// Git for Windows install. Hook commands are POSIX shell, so cmd.exe is not
// a fallback — it would misread the same spec that works on every other
// platform.
func shellPath() (string, error) {
	if sh, err := exec.LookPath("sh.exe"); err == nil {
		return sh, nil
	}
	roots := []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"),
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, rel := range gitBashCandidates {
			p := filepath.Join(root, rel)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("no POSIX shell: hook commands are shell commands, and sh.exe is not on PATH or in a Git for Windows install; install Git for Windows (winget install Git.Git) or put its sh.exe on PATH")
}
