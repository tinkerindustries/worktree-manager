package platform

// FileIdentity returns the stat identity of a path — device, inode and
// mtime — that the guard's per-session classification cache validates its
// entries against (internal/identity/guard_cache.go, 07-agent-surface.md
// §6.3): a cached classification stays valid exactly while the recorded
// root exists with the same identity, and a worktree removed mid-session
// makes the root vanish, dropping the cache.
//
// Every supported platform reports the three fields, from its own source:
// stat's st_dev/st_ino/st_mtim on unix, and
// GetFileInformationByHandle's volume serial and 64-bit file index on
// Windows, whose os.Stat does not carry them. A path that cannot be
// statted, and a filesystem that reports no identity at all, are both
// errors — the cache treats an error as "cannot validate" and the guard
// degrades to an uncached classification, which is the safe direction and
// is stated, never silent.
func FileIdentity(path string) (dev, ino uint64, mtime int64, err error) {
	return fileIdentity(path)
}
