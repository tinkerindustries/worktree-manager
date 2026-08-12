package identity

// guard_cache.go is the phase-7 per-session classification cache for the
// enforcement hook (the plan.md §9.2 decision, 07-agent-surface.md §6.3):
// the classification of one cwd cannot change while a session runs unless
// the worktree is removed underneath it, so the guard hook — which runs on
// every file tool call — caches the classification keyed on cwd and skips
// the git subprocesses on a hit.
//
// The cache lives in the directory the generated hook names through
// WT_GUARD_CACHE, one small file per resolved cwd. The entry records the
// classification plus the stat identity of the recorded root (dev, inode,
// mtime). Validity is one stat: when the root still exists with the same
// identity the classification is reused; when it is gone — the worktree
// was removed mid-session — or changed, the entry drops and the call
// reclassifies, returning the invalidation note once so the session hears
// about it and keeps working elsewhere.
//
// The descriptor is deliberately not cached: the guard's shared-store
// denial reads it fresh on every call, so a changed descriptor is honoured
// the same call.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// cacheVersion is the cache entry schema. Bumping it invalidates every
// existing entry structurally rather than by comparison.
const cacheVersion = 1

// guardCacheEntry is one cache file: the classification and the stat
// identity it was recorded under.
type guardCacheEntry struct {
	Version int    `json:"version"`
	Outcome string `json:"outcome"`
	// WorktreeRoot is both the classification's root and the stat target
	// the entry's validity is checked against.
	WorktreeRoot     string `json:"worktree_root"`
	MainCheckoutPath string `json:"main_checkout_path,omitempty"`
	GitCommonDir     string `json:"git_common_dir,omitempty"`
	Standalone       bool   `json:"standalone,omitempty"`
	Dev              uint64 `json:"dev"`
	Ino              uint64 `json:"ino"`
	MtimeNanos       int64  `json:"mtime_nanos"`
}

// ClassifyCached classifies cwd, consulting the per-session cache when
// cacheDir is non-empty. The second return is the one-time note when a
// cached classification was dropped because the recorded root vanished or
// changed — the worktree-removed-mid-session case.
func ClassifyCached(cwd string, standalone bool, cacheDir string) (*Classification, string, error) {
	if cacheDir == "" {
		cls, err := Classify(cwd, standalone)
		return cls, "", err
	}
	key, err := cacheKey(cwd)
	if err != nil {
		// An unresolvable cwd cannot be keyed; classify fresh rather than
		// refusing the call.
		cls, cerr := Classify(cwd, standalone)
		return cls, "", cerr
	}
	path := filepath.Join(cacheDir, key+".json")

	// A hit: the recorded root still exists with the same identity.
	entry, rerr := readCacheEntry(path)
	if rerr == nil && entry.Version == cacheVersion && entryValid(*entry) {
		return &Classification{
			Outcome:          outcomeFromString(entry.Outcome),
			WorktreeRoot:     entry.WorktreeRoot,
			MainCheckoutPath: entry.MainCheckoutPath,
			GitCommonDir:     entry.GitCommonDir,
			Standalone:       entry.Standalone,
		}, "", nil
	}

	// Miss or invalid: classify, then write the entry (best-effort — a
	// cache that cannot be written degrades to an uncached guard, never to
	// a refused one).
	note := ""
	if rerr == nil && entry.Version == cacheVersion && !entryValid(*entry) {
		// The cached root vanished or changed: the worktree was removed
		// mid-session (or replaced). The session hears about it once, even
		// when the reclassification itself fails because the cwd is gone,
		// and the stale entry is dropped so the next call is a clean miss.
		note = "the worktree this session classified was removed or replaced mid-session; the guard reclassified, and paths under the old root no longer exist"
		_ = os.Remove(path)
	}
	cls, err := Classify(cwd, standalone)
	if err != nil || cls == nil {
		return cls, note, err
	}

	stat := fileIdentity{}
	if target := cacheIdentityTarget(cls.WorktreeRoot); target != "" {
		if dev, ino, mtime, serr := platform.FileIdentity(target); serr == nil {
			stat = fileIdentity{dev: dev, ino: ino, mtime: mtime}
		}
	}
	entry = &guardCacheEntry{
		Version: cacheVersion, Outcome: cls.Outcome.String(),
		WorktreeRoot: cls.WorktreeRoot, MainCheckoutPath: cls.MainCheckoutPath,
		GitCommonDir: cls.GitCommonDir, Standalone: cls.Standalone,
		Dev: stat.dev, Ino: stat.ino, MtimeNanos: stat.mtime,
	}
	if werr := writeCacheEntry(path, *entry); werr != nil {
		// Best-effort by contract: the guard degrades to uncached, it
		// never refuses on a cache failure.
		_ = werr
	}
	return cls, note, nil
}

// entryValid reports whether the recorded root still exists with the same
// identity: the worktree-removed-mid-session case is exactly a vanished or
// replaced root.
func entryValid(e guardCacheEntry) bool {
	dev, ino, mtime, err := platform.FileIdentity(cacheIdentityTarget(e.WorktreeRoot))
	if err != nil {
		return false
	}
	return dev == e.Dev && ino == e.Ino && mtime == e.MtimeNanos
}

// cacheIdentityTarget is the path whose identity a cache entry is
// validated against: the root's own .git entry when it exists, else the
// root itself. The root directory's own mtime is not usable — the
// guard's case-sensitivity probe creates and removes one probe file per
// call in the root, bumping the directory's mtime and self-invalidating
// every entry on every call (08-platform.md §4.2). The .git entry is not
// touched by the probe, is recreated with a new identity when the
// worktree is re-created, and vanishes with the worktree on removal.
func cacheIdentityTarget(root string) string {
	gitEntry := filepath.Join(root, ".git")
	if _, err := os.Stat(gitEntry); err == nil {
		return gitEntry
	}
	return root
}

// readCacheEntry loads one cache file; any failure is a miss.
func readCacheEntry(path string) (*guardCacheEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e guardCacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// writeCacheEntry writes one cache file atomically, creating the cache
// directory. Best-effort by contract: the guard degrades to uncached, it
// never refuses on a cache failure.
func writeCacheEntry(path string, e guardCacheEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guard-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// cacheKey hashes the symlink-resolved cwd into a filename: the same
// directory reached through different spellings shares one entry.
func cacheKey(cwd string) (string, error) {
	// platform.RealPath rather than EvalSymlinks: the key has to stay the
	// same after the worktree is removed, or the entry written while it
	// existed can never be found and invalidated. EvalSymlinks fails on a
	// path that is gone, and falling back to Abs yields a different string
	// wherever an ancestor is a symlink — on macOS /var is a symlink to
	// /private/var, so every removal leaked its entry and the mid-session
	// removal was never noticed. RealPath resolves through the deepest
	// existing ancestor and re-appends the missing tail, so the key is
	// stable across the path existing and not existing.
	resolved, err := platform.RealPath(cwd)
	if err != nil {
		return "", fmt.Errorf("resolving %s for the guard cache: %w", cwd, err)
	}
	h := fnv.New64a()
	h.Write([]byte(resolved))
	return fmt.Sprintf("%x", h.Sum64()), nil
}

// fileIdentity is the stat identity a cache entry's validity is checked
// against.
type fileIdentity struct {
	dev   uint64
	ino   uint64
	mtime int64
}

// outcomeFromString maps a cached outcome back to the enum. An unknown
// string is a newer or foreign cache: the entry is invalid, and the next
// call reclassifies rather than trusting a string it does not know.
func outcomeFromString(s string) Outcome {
	switch s {
	case "not-a-repository":
		return NotARepository
	case "primary-checkout":
		return PrimaryCheckout
	case "linked-worktree":
		return LinkedWorktree
	case "standalone-clone":
		return StandaloneClone
	}
	return NotARepository
}
