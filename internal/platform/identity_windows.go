//go:build windows

package platform

import "fmt"

// fileIdentity reports unavailable on Windows: the stat fields the cache's
// validity check needs (device, inode) are not reported by the Windows
// stat path, so a cache entry can never be validated — the guard
// reclassifies every call. Safe by construction and stated: the cache is a
// performance seam, and a platform that cannot validate entries simply
// does not use it (08-platform.md §4's weaker-Windows rule).
func fileIdentity(path string) (dev, ino uint64, mtime int64, err error) {
	return 0, 0, 0, fmt.Errorf("file identity is unavailable on windows; the guard cache is disabled")
}
