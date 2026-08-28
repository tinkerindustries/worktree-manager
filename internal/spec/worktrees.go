package spec

// worktrees.go is where a repository says its worktrees go, and what they
// branch from.
//
// `wt` never creates one — Claude Code's `isolation: "worktree"`, the
// dispatch harness, a container's clone or a hand-typed `git worktree add`
// does (04-lifecycle.md §1) — so this is not a location either binary
// enforces. It is the repository's answer to "where does the next one go",
// recorded once in the committed spec instead of re-derived by every agent
// that creates one, and resolvable with `wt spec path --slug <slug>` so
// the answer is computed rather than reasoned about.
//
// The default is where Claude Code's own isolation puts a tree,
// `.claude/worktrees/<slug>` (B11.13): the manual flow is then genuinely
// the manual equivalent of the automatic one rather than a second
// convention beside it.

import (
	"path/filepath"
	"strings"
)

// Worktrees is the spec's `worktrees:` block: where a worktree of this
// repository goes, and what it branches from.
type Worktrees struct {
	// Path is the path template, evaluated with {slug}, {app} and {home}.
	// Absent, DefaultWorktreePath applies; present and empty, validation
	// refuses it, because an empty path is a typo and not a way to ask for
	// the default.
	Path *string `yaml:"path,omitempty"`

	// Base is the git revision a new worktree branches from. Absent,
	// DefaultWorktreeBase applies; present and empty, validation refuses
	// it, for the same reason Path does.
	//
	// It is here because the alternative is branching from whatever the
	// asking session happens to have checked out. Ask for a worktree from
	// inside another worktree and that is the other worktree's branch; ask
	// from a main checkout nobody has pulled in a fortnight and it is a
	// fortnight-old main. Neither is visible at the time and both surface
	// later as work on the wrong parent. The repository saying once what a
	// tree branches from is the same move as it saying once where trees go.
	Base *string `yaml:"base,omitempty"`
}

// DefaultWorktreePath is the template a spec that says nothing gets: the
// directory Claude Code's own `isolation: "worktree"` creates trees in.
const DefaultWorktreePath = ".claude/worktrees/{slug}"

// DefaultWorktreeBase is the revision a spec that says nothing branches
// from. It is a remote-tracking ref rather than `main`: the local branch is
// only as fresh as the last pull, and the whole point of naming a base is
// that it does not depend on the state of the checkout that asked.
const DefaultWorktreeBase = "origin/main"

// WorktreeBase returns the effective base revision, applying the default.
// It is the value the generated artefacts record, so what doctor compares
// is the effective base and not the presence of a key.
func WorktreeBase(s *Spec) string {
	if s.Worktrees.Base == nil {
		return DefaultWorktreeBase
	}
	return *s.Worktrees.Base
}

// WorktreePathVars are the variables the path template may reference. The
// set is deliberately smaller than a resource template's: {slot} is not
// known until the coordinator allocates one, which happens after the tree
// exists, and {worktree} is the thing being named.
var WorktreePathVars = []string{"app", "slug", "home"}

// WorktreePathTemplate returns the effective template, applying the
// default. It is the value the generated artefacts record, so what doctor
// compares is the effective template and not the presence of a key.
func WorktreePathTemplate(s *Spec) string {
	if s.Worktrees.Path == nil {
		return DefaultWorktreePath
	}
	return *s.Worktrees.Path
}

// WorktreeLocation resolves the template for one slug, returning an
// absolute path.
//
// A relative template resolves against mainCheckout — the repository's
// primary checkout, never cwd. Resolving against cwd would put a worktree
// created from inside a worktree underneath that worktree, which is a
// different tree every time and not what the convention says.
func WorktreeLocation(s *Spec, mainCheckout, slug, home string) (string, error) {
	value, err := substitute(WorktreePathTemplate(s), func(name string) (string, bool) {
		switch name {
		case "app":
			return s.App, true
		case "slug":
			return slug, true
		case "home":
			return home, true
		}
		return "", false
	})
	if err != nil {
		return "", &FieldError{Field: "worktrees.path", Reason: err.Error()}
	}
	// The template is written with forward slashes on every platform
	// (validation refuses a backslash), so it becomes a native path here
	// and nowhere else.
	p := filepath.FromSlash(value)
	if !filepath.IsAbs(p) {
		p = filepath.Join(mainCheckout, p)
	}
	return filepath.Clean(p), nil
}

// WorktreeIgnoreLine is the gitignore line covering the directory the
// worktrees land in, and whether there is one to write.
//
// A tree inside the repository is untracked content in the main checkout:
// without this line `git status --porcelain` never prints nothing again,
// and the create skill's pre-flight — which requires exactly that — fails
// from the second worktree onwards. The line is the literal directory
// prefix of the template, anchored with a leading slash so it matches at
// the repository root and nowhere deeper.
//
// There is no line when the trees land outside the repository ({home}, an
// absolute path, a `../` sibling layout) or when the template has no
// directory to name (`wt-{slug}` at the root): nothing is untracked in the
// first case, and the second cannot be covered without guessing at a glob.
func WorktreeIgnoreLine(s *Spec) (string, bool) {
	t := WorktreePathTemplate(s)
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "{home}") {
		return "", false
	}
	brace := strings.IndexByte(t, '{')
	if brace < 0 {
		return "", false
	}
	slash := strings.LastIndexByte(t[:brace], '/')
	if slash <= 0 {
		return "", false
	}
	dir := t[:slash]
	if dir == ".." || strings.HasPrefix(dir, "../") {
		return "", false
	}
	return "/" + dir + "/", true
}
