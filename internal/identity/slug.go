package identity

import (
	"fmt"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// ValidateSlug returns the reason a slug is invalid, or "" when it is valid.
// It is the reason-giving form of spec.ValidSlug (01-identity.md §4.1): the
// rule and the cap live in the spec package — one implementation used by the
// registry, container namespaces and file paths alike — and this function
// turns the boolean into an error a user can fix.
func ValidateSlug(s string) string {
	if s == "" {
		return "empty slug"
	}
	if len(s) > spec.SlugMaxLen {
		return fmt.Sprintf("%d characters; the cap is %d", len(s), spec.SlugMaxLen)
	}
	if !spec.ValidSlug(s) {
		return "must match ^[a-z0-9][a-z0-9-]*$ — lowercase letters, digits and hyphens, starting with a letter or digit"
	}
	return ""
}

// DefaultSlug is the slug a worktree takes when nothing else names it: the
// worktree directory basename, never the branch (01-identity.md §4.1 —
// branches get renamed and deleted, directories do not). M1 validates a slug
// and never invents one; this is the documented default, applied by init in
// phase 5 and available now so the naming contract is visible.
func DefaultSlug(worktreeRoot string) string {
	return filepath.Base(worktreeRoot)
}
