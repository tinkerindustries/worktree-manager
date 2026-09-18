package identity

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
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

// NormaliseSlug turns an arbitrary caller-supplied name into a legal slug,
// or reports why it cannot.
//
// It exists because the name is not always the caller's to choose. Claude
// Code names a worktree after the task that prompted it and appends a hash,
// and the WorktreeCreate hook is handed that name rather than asked for
// one. A name the hook cannot pass to `wt spec path` fails worktree
// creation outright, so the person loses a worktree over a name nobody
// typed. Losing characters off the end of a name beats losing the tree.
//
// The mapping is deterministic and total over non-empty ASCII-bearing
// names: lower-case, every character outside [a-z0-9] becomes a hyphen,
// runs of hyphens collapse, leading and trailing hyphens are trimmed, and
// the result is cut to spec.SlugMaxLen (then re-trimmed, so a cut never
// leaves a trailing hyphen). Determinism is the point: the same name
// normalises to the same slug on every machine and every run, so a slug is
// still the stable identity that rm, the registry and the descriptor
// filename all agree on.
//
// A name with nothing to keep — empty, or punctuation and spaces alone —
// returns a reason rather than an invented slug. The tool never names a
// worktree out of nothing.
func NormaliseSlug(name string) (string, string) {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			// One hyphen per run: appending to an empty builder or after
			// a hyphen writes nothing, which also drops leading hyphens.
			s := b.String()
			if s != "" && !strings.HasSuffix(s, "-") {
				b.WriteByte('-')
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if out == "" {
		return "", fmt.Sprintf("%q has no letters or digits to make a slug from", name)
	}
	if len(out) > spec.SlugMaxLen {
		out = strings.TrimSuffix(out[:spec.SlugMaxLen], "-")
	}
	return out, ""
}
