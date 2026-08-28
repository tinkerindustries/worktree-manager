//go:build windows

package platform

import (
	"os"
	"path/filepath"
)

// helperDirs is the Windows fallback list, searched only after PATH. A
// logon scheduled task picks up the user's registry PATH, so Windows is
// the least exposed of the three platforms — but that PATH is the one
// captured at logon, and a helper installed since then is invisible until
// the next one.
func helperDirs() []string {
	var dirs []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if p := os.Getenv(env); p != "" {
			dirs = append(dirs,
				filepath.Join(p, "Docker", "Docker", "resources", "bin"),
				filepath.Join(p, "GitHub CLI"),
				filepath.Join(p, "Git", "cmd"))
		}
	}
	if p := os.Getenv("LOCALAPPDATA"); p != "" {
		dirs = append(dirs, filepath.Join(p, "Microsoft", "WindowsApps"))
	}
	return append(dirs, underHome(".docker\\bin", ".rd\\bin")...)
}
