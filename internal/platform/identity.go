package platform

// FileIdentity returns the stat identity of a path — device, inode and
// mtime — that the guard's per-session classification cache validates its
// entries against (internal/identity/guard_cache.go, 07-agent-surface.md
// §6.3): a cached classification stays valid exactly while the recorded
// root exists with the same identity, and a worktree removed mid-session
// makes the root vanish, dropping the cache.
//
// The three fields are the identity on the platforms that have them; on a
// platform that cannot report them (Windows), FileIdentity returns an
// error and the cache simply never validates an entry — the guard degrades
// to an uncached classification, which is the safe direction and is
// stated, never silent.
func FileIdentity(path string) (dev, ino uint64, mtime int64, err error) {
	return fileIdentity(path)
}
