//go:build darwin

package platform

// helperDirs is the macOS fallback list, searched only after PATH. It
// covers the locations the coordinator's three helpers actually install
// to: Docker Desktop symlinks its CLI into /usr/local/bin and, on recent
// versions, ~/.docker/bin; Homebrew puts colima and gh in /opt/homebrew/bin
// on Apple silicon and /usr/local/bin on Intel. None of those are on the
// PATH launchd gives a LaunchAgent.
func helperDirs() []string {
	dirs := []string{
		"/opt/homebrew/bin", // Homebrew, Apple silicon: colima, gh
		"/usr/local/bin",    // Docker Desktop's CLI symlink; Homebrew on Intel
		"/opt/local/bin",    // MacPorts
	}
	// Docker Desktop's user-space CLI directory and Rancher Desktop's.
	return append(dirs, underHome(".docker/bin", ".rd/bin")...)
}
