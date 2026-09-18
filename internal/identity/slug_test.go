package identity

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// TestValidateSlug: the reason-giving form of the slug rule. The rule and the
// cap are spec.ValidSlug and spec.SlugMaxLen — this package writes no second
// regexp (01-identity.md §4.1: one implementation used by the registry,
// container namespaces and file paths alike).
func TestValidateSlug(t *testing.T) {
	tests := []struct {
		slug string
		want string // "" means valid
	}{
		{"brisk-otter", ""},
		{"a", ""},
		{"no", ""}, // YAML-hazardous but legal (ARCHITECTURE.md §8.2)
		{"0", ""},
		{"a-1-b", ""},
		{"", "empty slug"},
		{"Brisk-Otter", "must match"},
		{"-lead", "must match"},
		{"has_space", "must match"},
		{"under_score", "must match"},
		{strings.Repeat("a", spec.SlugMaxLen), ""},
		{strings.Repeat("a", spec.SlugMaxLen+1), fmt.Sprintf("cap is %d", spec.SlugMaxLen)},
	}
	for _, tt := range tests {
		got := ValidateSlug(tt.slug)
		if tt.want == "" {
			if got != "" {
				t.Errorf("ValidateSlug(%q) = %q, want valid", tt.slug, got)
			}
		} else if !strings.Contains(got, tt.want) {
			t.Errorf("ValidateSlug(%q) = %q, want it to contain %q", tt.slug, got, tt.want)
		}
	}
}

// TestValidateSlugAgreesWithSpec pins the one-implementation rule: the
// reason-giving form and spec.ValidSlug never disagree.
func TestValidateSlugAgreesWithSpec(t *testing.T) {
	slugs := []string{"", "a", "no", "Brisk-Otter", "-x", "x_y", strings.Repeat("a", spec.SlugMaxLen+1), strings.Repeat("b", spec.SlugMaxLen)}
	for _, s := range slugs {
		valid := spec.ValidSlug(s)
		reason := ValidateSlug(s)
		if valid != (reason == "") {
			t.Errorf("ValidateSlug(%q) = %q while spec.ValidSlug = %v; the two implementations disagree", s, reason, valid)
		}
	}
}

// TestDefaultSlug: the slug defaults to the worktree directory basename,
// never the branch (01-identity.md §4.1).
func TestDefaultSlug(t *testing.T) {
	if got := DefaultSlug("/Users/alex/Repos/bacio/.claude/worktrees/brisk-otter"); got != "brisk-otter" {
		t.Errorf("DefaultSlug = %q, want brisk-otter", got)
	}
	if got := DefaultSlug(filepath.Join(t.TempDir(), "alpha")); got != "alpha" {
		t.Errorf("DefaultSlug = %q, want alpha", got)
	}
}

// TestDescriptorPath: the descriptor sits at the worktree root under the
// spec's emit.descriptor.filename, never a constant of this package's own.
func TestDescriptorPath(t *testing.T) {
	d := spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}
	if got := DescriptorPath("/repo/wt/alpha", d); got != filepath.Join("/repo/wt/alpha", "wt-env.yaml") {
		t.Errorf("DescriptorPath = %q", got)
	}
}
