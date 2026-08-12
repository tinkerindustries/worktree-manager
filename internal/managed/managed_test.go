package managed

// managed_test.go pins the block convention's contract: regeneration
// replaces only the block so hand edits outside it survive, the field
// records are what doctor parses, unbalanced markers refuse with line
// numbers, and a markerless file gains the block without losing its own
// text (the CLAUDE.md case).

import (
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/envfile"
)

// TestReplacePreservesHandEditsOutsideTheBlock is exit criterion 4's core:
// regenerating an edited file preserves the edits outside the managed
// block.
func TestReplacePreservesHandEditsOutsideTheBlock(t *testing.T) {
	fields := map[string]string{"app": "plain-app", "descriptor": "wt-env.json"}
	existing := []string{
		"# A hand-written skill",
		"",
		"The developer added this paragraph after generation.",
		"",
	}
	existing = append(existing, Render(fields, []string{"a fact line"})...)
	content := strings.Join(existing, "\n") + "\n"

	// A hand edit lands outside the block, then regeneration runs.
	edited := strings.Replace(content, "The developer added this paragraph after generation.",
		"The developer added this paragraph after generation — and then edited it.", 1)
	out, err := Replace([]byte(edited), map[string]string{"app": "plain-app", "descriptor": "wt-env.json", "resources": "api, db"}, nil)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}

	if !strings.Contains(string(out), "and then edited it") {
		t.Errorf("the hand edit outside the block did not survive regeneration:\n%s", out)
	}
	if !strings.Contains(string(out), "# wt-field: resources=api, db") {
		t.Errorf("the fresh block does not carry the new field:\n%s", out)
	}
	if !strings.Contains(string(out), "# wt-field: app=plain-app") {
		t.Errorf("the fresh block lost the app field:\n%s", out)
	}
	if strings.Contains(string(out), "a fact line") {
		t.Errorf("the old block's content survived regeneration:\n%s", out)
	}
}

// TestParseExtractsFieldRecords: doctor's half — the block parses into
// name/value records plus content lines.
func TestParseExtractsFieldRecords(t *testing.T) {
	content := "# header\n\n" + strings.Join(Render(map[string]string{
		"app": "plain-app", "band api": "8200", "resources": "api, db",
	}, []string{"a fact line", "another fact line"}), "\n") + "\n# footer\n"

	block, ok, err := Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !ok {
		t.Fatal("the markers were not found")
	}
	if got, _ := block.Lookup("app"); got != "plain-app" {
		t.Errorf("app = %q, want plain-app", got)
	}
	if got, _ := block.Lookup("band api"); got != "8200" {
		t.Errorf("band api = %q, want 8200", got)
	}
	if len(block.Fields) != 3 {
		t.Errorf("fields = %+v, want three records", block.Fields)
	}
	if len(block.Content) != 2 || block.Content[0] != "a fact line" {
		t.Errorf("content = %+v, want the two fact lines", block.Content)
	}
}

// TestParseNoMarkers: a file without markers has an empty block, not an
// error.
func TestParseNoMarkers(t *testing.T) {
	block, ok, err := Parse([]byte("# just a file\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ok || len(block.Fields) != 0 {
		t.Errorf("a markerless file parsed as %+v ok=%v, want empty and false", block, ok)
	}
}

// TestReplaceRefusesUnbalancedMarkers: a lone start marker refuses the
// regeneration naming the line number, like the .env block refuses
// (05-delivery.md §8).
func TestReplaceRefusesUnbalancedMarkers(t *testing.T) {
	content := "line one\n" + StartMarker + "\nline two\n"
	_, err := Replace([]byte(content), map[string]string{"app": "x"}, nil)
	if err == nil {
		t.Fatal("a never-closed block was regenerated")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the refusal does not name the line: %v", err)
	}

	content = "line one\n" + EndMarker + "\nline two\n"
	_, err = Replace([]byte(content), map[string]string{"app": "x"}, nil)
	if err == nil {
		t.Fatal("an end marker without a start marker was regenerated")
	}

	content = StartMarker + "\nx\n" + StartMarker + "\ny\n" + EndMarker + "\n"
	_, err = Replace([]byte(content), map[string]string{"app": "x"}, nil)
	if err == nil {
		t.Fatal("nested markers were regenerated")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("the refusal does not name the nesting: %v", err)
	}
}

// TestReplaceAppendsBlockToMarkerlessFile: a repo file with no markers yet
// gains the block appended, its own text untouched — the CLAUDE.md
// tripwire joining a repository's own instructions.
func TestReplaceAppendsBlockToMarkerlessFile(t *testing.T) {
	content := "# CLAUDE.md\n\nThis repository's own instructions.\n"
	out, err := Replace([]byte(content), map[string]string{"app": "plain-app"}, tripwireContent())
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if !strings.Contains(string(out), "This repository's own instructions.") {
		t.Errorf("the repo's own text was lost:\n%s", out)
	}
	if !strings.Contains(string(out), "Never hardcode a port") {
		t.Errorf("the tripwire content is missing:\n%s", out)
	}
	if !strings.HasPrefix(string(out), "# CLAUDE.md\n\nThis repository's own instructions.") {
		t.Errorf("the appended block landed before the repo's text:\n%s", out)
	}
	// The result is itself re-replaceable: idempotent regeneration.
	out2, err := Replace(out, map[string]string{"app": "plain-app"}, tripwireContent())
	if err != nil {
		t.Fatalf("second Replace: %v", err)
	}
	if strings.Count(string(out2), StartMarker) != 1 {
		t.Errorf("regeneration stacked markers:\n%s", out2)
	}
}

// tripwireContent is the CLAUDE.md tripwire's content lines.
func tripwireContent() []string {
	return []string{
		"This repository uses per-worktree environments.",
		"- Never hardcode a port or a path: read them with `wt show`.",
		"- Something else creates the worktree; `wt init` attaches to it.",
	}
}

// TestMarkersAreTheEnvfileConvention pins the phase-7 rule literally:
// the generated artefacts use the .env block's own markers — one marker
// convention in the system, never a second one (plan.md §5 phase 7).
func TestMarkersAreTheEnvfileConvention(t *testing.T) {
	if StartMarker != envfile.StartMarker || EndMarker != envfile.EndMarker {
		t.Errorf("markers differ from the .env block's: %q/%q vs %q/%q",
			StartMarker, EndMarker, envfile.StartMarker, envfile.EndMarker)
	}
}
