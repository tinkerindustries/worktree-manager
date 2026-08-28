package identity

import (
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// TestNormaliseSlug covers the mapping the WorktreeCreate hook depends on:
// whatever Claude Code hands it becomes a legal slug, or is refused with a
// reason. Every accepted result must satisfy the slug rule itself, which is
// the property that matters — a normaliser that emits an illegal slug moves
// the failure rather than fixing it.
func TestNormaliseSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already legal", "brisk-otter", "brisk-otter"},
		{"upper case", "Brisk-Otter", "brisk-otter"},
		{"spaces", "add the export button", "add-the-export-button"},
		{"underscores and dots", "fix_the.thing", "fix-the-thing"},
		{"slashes", "feature/export-csv", "feature-export-csv"},
		{"collapses runs", "a   --  b", "a-b"},
		{"trims edges", "--brisk-otter--", "brisk-otter"},
		{"drops non-ascii", "café-menu", "caf-menu"},
		{"digits lead", "2fa-login", "2fa-login"},
		// The report's case: 37 characters, legal today, unchanged.
		{"the reported name", "print-pipeline-connector-error-0b6837", "print-pipeline-connector-error-0b6837"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := NormaliseSlug(c.in)
			if reason != "" {
				t.Fatalf("NormaliseSlug(%q) refused: %s", c.in, reason)
			}
			if got != c.want {
				t.Errorf("NormaliseSlug(%q) = %q, want %q", c.in, got, c.want)
			}
			if r := ValidateSlug(got); r != "" {
				t.Errorf("NormaliseSlug(%q) = %q, which is not a legal slug: %s", c.in, got, r)
			}
		})
	}
}

// TestNormaliseSlugTruncates pins the cut: a name past the cap loses its
// tail rather than the whole worktree, and the cut never leaves a trailing
// hyphen behind.
func TestNormaliseSlugTruncates(t *testing.T) {
	// A cut landing mid-word keeps every character up to the cap.
	long := "a-very-long-worktree-name-that-claude-codex-generated-from-a-task"
	got, reason := NormaliseSlug(long)
	if reason != "" {
		t.Fatalf("refused: %s", reason)
	}
	if got != long[:spec.SlugMaxLen] {
		t.Errorf("mid-word cut = %q, want %q", got, long[:spec.SlugMaxLen])
	}
	if r := ValidateSlug(got); r != "" {
		t.Errorf("%q is not a legal slug: %s", got, r)
	}

	// A cut landing on a hyphen is trimmed: the slug rule allows a trailing
	// hyphen, but a name ending in one reads as truncated-by-accident.
	pad := ""
	for len(pad) < spec.SlugMaxLen-1 {
		pad += "a"
	}
	got, reason = NormaliseSlug(pad + "-tail")
	if reason != "" {
		t.Fatalf("refused: %s", reason)
	}
	if got != pad {
		t.Errorf("cut on a hyphen = %q, want %q", got, pad)
	}
}

// TestNormaliseSlugRefuses: the tool never invents a name out of nothing.
func TestNormaliseSlugRefuses(t *testing.T) {
	for _, in := range []string{"", "---", "   ", "!!!", "—"} {
		if got, reason := NormaliseSlug(in); reason == "" {
			t.Errorf("NormaliseSlug(%q) = %q, want a refusal", in, got)
		}
	}
}

// TestNormaliseSlugDeterministic: the same name gives the same slug, which
// is what lets the slug stay the identity rm and the registry agree on.
func TestNormaliseSlugDeterministic(t *testing.T) {
	const in = "Add The Export Button (v2)"
	first, _ := NormaliseSlug(in)
	for i := 0; i < 8; i++ {
		got, _ := NormaliseSlug(in)
		if got != first {
			t.Fatalf("run %d gave %q, first gave %q", i, got, first)
		}
	}
	if second, _ := NormaliseSlug(first); second != first {
		t.Errorf("normalising a slug changed it: %q -> %q", first, second)
	}
}
