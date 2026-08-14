//go:build darwin || linux

package platform

import (
	"fmt"
	"os"
	"syscall"
)

// WithPrivateUmask runs fn with the process umask set to 0o077 and
// restores it before returning, so every file fn creates is born 0600 —
// the store's file permission model. The SQLite driver creates wt.db and
// its WAL and SHM sidecars at the process umask, so the flip must cover
// the open itself, not a post-creation chmod. wtd is single-threaded
// while the store opens, so the process-wide umask flip is safe (the same
// reasoning as listenUnix).
func WithPrivateUmask(fn func() error) error {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	return fn()
}

// VerifyPrivateFileModes refuses when any of the paths exists with group
// or other permission bits set. Entry secrets live in the store database
// and its WAL and SHM sidecars, so the 0600 model is verified after open
// rather than assumed: a hostile umask would otherwise leave the secrets
// world-readable and nothing would say so.
func VerifyPrivateFileModes(paths ...string) error {
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("checking %s: %w", p, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf(
				"%s is %v; the store database and its WAL/SHM sidecars must be 0600 because entry secrets live in them — check the umask of the process that created the store, then remove the file and restart wtd",
				p, fi.Mode().Perm())
		}
	}
	return nil
}
