//go:build linux

package platform

// helperDirs is the Linux fallback list, searched only after PATH. A
// systemd user unit inherits a narrower PATH than the user's shell —
// docker is usually in /usr/bin and so resolves anyway, but a
// user-installed gh or docker CLI under ~/.local/bin, Homebrew or snap
// does not.
func helperDirs() []string {
	dirs := []string{
		"/usr/local/bin",
		"/home/linuxbrew/.linuxbrew/bin", // Homebrew on Linux
		"/snap/bin",
	}
	return append(dirs, underHome(".local/bin", ".docker/bin", ".rd/bin",
		".linuxbrew/bin")...)
}
