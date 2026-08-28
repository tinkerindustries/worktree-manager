package spec

// worktrees_test.go pins the `worktrees:` block: the default is where
// Claude Code puts a tree, an override resolves against the main checkout,
// and the refusals are the ones that would otherwise surface as a
// collision long after the spec was written.

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func str(s string) *string { return &s }

// abs turns a POSIX-shaped fixture path into one that is absolute on the
// running platform. On Windows a rooted path with no volume — what
// filepath.FromSlash("/srv/repo") produces — is *not* absolute, so
// WorktreeLocation would join it onto the main checkout and the rule
// under test ("an absolute template stands on its own, a relative one
// resolves against the main checkout") could never be exercised. A volume
// letter is what makes it absolute; the paths are never touched on disk.
func abs(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}
	return filepath.FromSlash(p)
}

// absTemplate is abs in the spelling a spec template is written in:
// forward slashes on every platform (validation refuses a backslash).
func absTemplate(p string) string { return filepath.ToSlash(abs(p)) }

// TestWorktreePathDefault: a spec that says nothing gets Claude Code's own
// directory, so the manual flow and the automatic one land in one place.
func TestWorktreePathDefault(t *testing.T) {
	s := &Spec{App: "bacio"}
	if got := WorktreePathTemplate(s); got != ".claude/worktrees/{slug}" {
		t.Errorf("template = %q, want the Claude Code default", got)
	}
	got, err := WorktreeLocation(s, abs("/repo"), "brisk-otter", abs("/home/u"))
	if err != nil {
		t.Fatalf("WorktreeLocation: %v", err)
	}
	want := filepath.Join(abs("/repo"), ".claude", "worktrees", "brisk-otter")
	if got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
}

// TestWorktreeLocationOverrides: the three shapes a repository can ask
// for — a sibling directory, a path under the home directory, and an
// absolute one — each resolved against the main checkout, never cwd.
func TestWorktreeLocationOverrides(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{"sibling", "../{app}-worktrees/{slug}", filepath.Join(abs("/srv"), "bacio-worktrees", "brisk-otter")},
		{"home", "{home}/wt/{app}/{slug}", filepath.Join(abs("/home/u"), "wt", "bacio", "brisk-otter")},
		{"absolute", absTemplate("/var") + "/wt/{slug}", filepath.Join(abs("/var"), "wt", "brisk-otter")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Spec{App: "bacio", Worktrees: Worktrees{Path: str(tt.template)}}
			got, err := WorktreeLocation(s, abs("/srv/repo"), "brisk-otter", abs("/home/u"))
			if err != nil {
				t.Fatalf("WorktreeLocation: %v", err)
			}
			if got != tt.want {
				t.Errorf("location = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWorktreeIgnoreLine: the line exists exactly when the trees land
// inside the repository, because that is exactly when the main checkout
// would otherwise be untracked-dirty for good.
func TestWorktreeIgnoreLine(t *testing.T) {
	tests := []struct {
		template string
		want     string
	}{
		{".claude/worktrees/{slug}", "/.claude/worktrees/"},
		{"trees/{app}/{slug}", "/trees/"},
		{"../{app}-worktrees/{slug}", ""},
		{"{home}/wt/{slug}", ""},
		{"/var/wt/{slug}", ""},
		{"wt-{slug}", ""},
	}
	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			s := &Spec{App: "bacio", Worktrees: Worktrees{Path: str(tt.template)}}
			got, ok := WorktreeIgnoreLine(s)
			if tt.want == "" {
				if ok {
					t.Errorf("ignore line = %q, want none", got)
				}
				return
			}
			if !ok || got != tt.want {
				t.Errorf("ignore line = %q (ok=%v), want %q", got, ok, tt.want)
			}
		})
	}
}

// TestWorktreePathRefusals: every refusal names worktrees.path and says
// what to write instead.
func TestWorktreePathRefusals(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		reasonSub string
	}{
		{"empty", "", "must not be empty"},
		{"no slug", ".claude/worktrees/{app}", "must reference {slug}"},
		{"unknown variable", ".claude/worktrees/{slot}-{slug}", "unknown template variable {slot}"},
		{"tilde", "~/wt/{slug}", "the home directory is {home}"},
		{"backslash", `..\wt\{slug}`, "must use forward slashes"},
		{"unclosed", ".claude/worktrees/{slug", "unclosed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorktrees(&Worktrees{Path: str(tt.template)})
			if err == nil {
				t.Fatal("validated, want a refusal")
			}
			var fe *FieldError
			if !asFieldError(err, &fe) {
				t.Fatalf("error = %v, want a FieldError", err)
			}
			if fe.Field != "worktrees.path" {
				t.Errorf("field = %q, want worktrees.path", fe.Field)
			}
			if !strings.Contains(fe.Reason, tt.reasonSub) {
				t.Errorf("reason = %q, want substring %q", fe.Reason, tt.reasonSub)
			}
		})
	}
}

// TestWorktreesAbsentIsNotAnOverride: an absent block validates and reads
// as the default, which is what makes the field optional for every
// repository already adopted.
func TestWorktreesAbsentIsNotAnOverride(t *testing.T) {
	if err := validateWorktrees(&Worktrees{}); err != nil {
		t.Fatalf("an absent worktrees block was refused: %v", err)
	}
}

// TestWorktreeBaseDefault: a spec that says nothing branches from
// origin/main, not from whatever the asking session had checked out. The
// default is the remote-tracking ref deliberately — a local branch is only
// as fresh as the last pull, and the point of naming a base is that it does
// not depend on the state of the checkout that asked.
func TestWorktreeBaseDefault(t *testing.T) {
	s := &Spec{App: "bacio"}
	if got := WorktreeBase(s); got != "origin/main" {
		t.Errorf("base = %q, want origin/main", got)
	}
	if DefaultWorktreeBase != "origin/main" {
		t.Errorf("DefaultWorktreeBase = %q", DefaultWorktreeBase)
	}
}

// A repository that names a base gets it back verbatim: the value is one
// git revision and this package never interprets it.
func TestWorktreeBaseOverride(t *testing.T) {
	for _, want := range []string{"main", "develop", "upstream/trunk", "v1.4.0", "HEAD"} {
		s := &Spec{App: "bacio", Worktrees: Worktrees{Base: str(want)}}
		if got := WorktreeBase(s); got != want {
			t.Errorf("base = %q, want %q", got, want)
		}
		if err := Validate(&Spec{
			Version: 1, App: "bacio",
			Resources: []Resource{{Type: "port", Name: "api"}},
			Worktrees: Worktrees{Base: str(want)},
			Emit:      Emit{Descriptor: Descriptor{Filename: "wt-env.json", Format: "json"}},
		}); err != nil {
			t.Errorf("Validate refused the base %q: %v", want, err)
		}
	}
}

// The refusals are the values that are wrong on their face. The revision
// itself is git's to resolve — this package never runs git — so an
// unresolvable-but-well-formed base validates here and fails at the point
// of use, with a message naming the field.
func TestWorktreeBaseRefusals(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		reason string
	}{
		{"empty", "", "must not be empty"},
		{"leading hyphen", "--force", "must not start with a hyphen"},
		{"inner space", "origin/my branch", "must not contain whitespace"},
		{"trailing space", "origin/main ", "must not contain whitespace"},
		{"tab", "origin/main\tx", "must not contain whitespace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(&Spec{
				Version: 1, App: "bacio",
				Resources: []Resource{{Type: "port", Name: "api"}},
				Worktrees: Worktrees{Base: str(c.base)},
				Emit:      Emit{Descriptor: Descriptor{Filename: "wt-env.json", Format: "json"}},
			})
			if err == nil {
				t.Fatalf("base %q validated, want refusal", c.base)
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != "worktrees.base" {
				t.Fatalf("error = %v, want a FieldError on worktrees.base", err)
			}
			if !strings.Contains(fe.Reason, c.reason) {
				t.Errorf("reason = %q, want it to contain %q", fe.Reason, c.reason)
			}
		})
	}
}

// The base is checked even when the block names no path: a spec setting
// only worktrees.base must not slip past validation.
func TestWorktreeBaseCheckedWithoutAPath(t *testing.T) {
	err := Validate(&Spec{
		Version: 1, App: "bacio",
		Resources: []Resource{{Type: "port", Name: "api"}},
		Worktrees: Worktrees{Base: str("")},
		Emit:      Emit{Descriptor: Descriptor{Filename: "wt-env.json", Format: "json"}},
	})
	if err == nil {
		t.Fatal("an empty base with no path validated, want refusal")
	}
}
