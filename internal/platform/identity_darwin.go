//go:build darwin

package platform

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity stats a path and reports its device, inode and mtime — the
// identity the guard's cache validates against. A path that cannot be
// statted has no identity: the cache treats that as invalid, which is the
// worktree-removed case.
func fileIdentity(path string) (dev, ino uint64, mtime int64, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, fmt.Errorf("stat identity unavailable on this platform")
	}
	return uint64(st.Dev), uint64(st.Ino), st.Mtimespec.Nano(), nil
}
