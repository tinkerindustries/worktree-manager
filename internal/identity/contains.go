package identity

import (
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// Contains reports whether path lies inside root, with the two rails of
// 01-identity.md §3.1: symlinks are resolved on both sides before comparing
// (on macOS /tmp is /private/tmp and /Users can arrive via
// /System/Volumes/Data), and the comparison is on path segments, not string
// prefixes — contains("/a/b", "/a/bc") is false.
//
// The casing comparison follows the filesystem, not the operating system
// (08-platform.md §4.2): a case-sensitive comparison on a case-insensitive
// mount can be walked straight past, so the mount holding root is probed, and
// where the probe cannot determine the answer the comparison assumes
// case-insensitive — the conservative direction for the containment property.
//
// RealPath resolves through the deepest existing ancestor, so a not-yet-
// created path resolves too; the two paths are compared in that resolved
// form. A path that cannot be realised at all (no resolvable ancestor) is not
// contained: the guard must never approve a write it could not place.
func Contains(root, path string) bool {
	r, err := platform.RealPath(root)
	if err != nil {
		return false
	}
	p, err := platform.RealPath(path)
	if err != nil {
		return false
	}
	caseSensitive, err := platform.CaseSensitive(r)
	if err != nil {
		caseSensitive = false // conservative: assume case-insensitive (§8)
	}
	return containsResolved(r, p, caseSensitive)
}

// containsResolved is the pure segment-wise comparison on already-realised
// paths. It is split out from Contains so the case-insensitive branch is
// testable without a case-insensitive mount, which the test environments of
// this phase do not provide.
func containsResolved(root, path string, caseSensitive bool) bool {
	rootSegs := segments(root)
	pathSegs := segments(path)
	if len(pathSegs) < len(rootSegs) {
		return false
	}
	for i, rs := range rootSegs {
		ps := pathSegs[i]
		if rs == ps {
			continue
		}
		if !caseSensitive && strings.EqualFold(rs, ps) {
			continue
		}
		return false
	}
	return true
}

// segments splits a cleaned path into its components, dropping the empty
// segments that Clean leaves for a rooted path ("/a/b" → ["a", "b"]) and
// keeping a Windows volume name ("C:" → ["C:", ...]).
func segments(p string) []string {
	cleaned := filepath.Clean(p)
	parts := strings.Split(cleaned, string(filepath.Separator))
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
