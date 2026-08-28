package claudehook

// skill_refresh_test.go covers the question install and uninstall have to
// answer about an installed skill file: is this wt's own, or the user's?
//
// Content equality cannot answer it. A file that differs from what this
// binary would write is either an older release's copy or an edit, and the
// two want opposite treatment. Reading "differs" as "edited" is what made
// `wt claude install --refresh-only` unable to update this skill at all —
// every file that legitimately changed between releases was skipped as a
// user edit, which is the one thing --refresh-only exists to prevent.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installedSkill(t *testing.T, l Layout, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(l.SkillDir(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading the installed %s: %v", rel, err)
	}
	return string(data)
}

func writeSkill(t *testing.T, l Layout, rel, content string) {
	t.Helper()
	path := filepath.Join(l.SkillDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), skillMode); err != nil {
		t.Fatal(err)
	}
}

// installedLayout is a fresh layout with the hooks and the skill installed.
// A refresh does nothing at all unless the hooks are already there, so the
// plain install always comes first.
func installedLayout(t *testing.T) Layout {
	t.Helper()
	l := layoutIn(t)
	if _, err := Install(l, Options{Skill: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	return l
}

// TestRefreshUpdatesAStaleSkillFile is the regression. A previous release's
// skill file — wt's own, recorded as wt's own, holding text this release no
// longer ships — must be brought up to date by a refresh. Reading "differs"
// as "edited" skipped it, so an upgrade never reached the skill.
func TestRefreshUpdatesAStaleSkillFile(t *testing.T) {
	l := installedLayout(t)
	const rel = "SKILL.md"
	body, err := skillBody(rel)
	if err != nil {
		t.Fatal(err)
	}

	// What an older release left behind: its own document, and a record
	// saying wt wrote it.
	const stale = "# worktree-onboarding\n\nWhat the last release said.\n"
	writeSkill(t, l, rel, stale)
	m, err := readSkillManifest(l)
	if err != nil {
		t.Fatal(err)
	}
	m.Files[rel] = digest([]byte(stale))
	if err := writeSkillManifest(l, m); err != nil {
		t.Fatal(err)
	}

	changes, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh over a stale skill file: %v", err)
	}
	if _, ok := hasAction(changes, "skipped"); ok {
		t.Errorf("the refresh skipped a file wt had written: %#v", changes)
	}
	got := installedSkill(t, l, rel)
	if got != string(body) {
		t.Error("the refresh did not bring the stale skill file up to date")
	}
	if strings.Contains(got, "What the last release said") {
		t.Error("the previous release's text survived the refresh")
	}
}

// A file the user made their own is left alone and reported, rather than
// overwritten.
func TestRefreshLeavesAUsersSkillFileAlone(t *testing.T) {
	l := installedLayout(t)
	const rel = "SKILL.md"
	mine := "# my own onboarding\n\nI rewrote this entirely.\n"
	writeSkill(t, l, rel, mine)

	changes, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh over a rewritten skill file: %v", err)
	}
	skip, ok := hasAction(changes, "skipped")
	if !ok {
		t.Fatalf("changes = %#v, want the user's file skipped", changes)
	}
	if !strings.Contains(skip.Detail, "--force") {
		t.Errorf("the skip does not name the flag that would overwrite it: %q", skip.Detail)
	}
	if got := installedSkill(t, l, rel); got != mine {
		t.Error("the refresh overwrote a file the user had made their own")
	}

	// --force is the way through, and it is the only way.
	if _, err := Install(l, Options{Skill: true, RefreshOnly: true, Force: true}); err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	body, err := skillBody(rel)
	if err != nil {
		t.Fatal(err)
	}
	if installedSkill(t, l, rel) != string(body) {
		t.Error("--force did not overwrite the file")
	}
}

// With no record at all — an installation from a release that kept none —
// a file identical to what this binary would write is still provably wt's
// own, so it is adopted and recorded rather than skipped. That is what
// stops every existing installation needing a --force once.
func TestRefreshAdoptsAnUnrecordedButCurrentCopy(t *testing.T) {
	l := installedLayout(t)
	if err := os.Remove(l.ManifestPath()); err != nil {
		t.Fatal(err)
	}

	changes, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh with no record: %v", err)
	}
	if _, ok := hasAction(changes, "skipped"); ok {
		t.Errorf("an unrecorded copy of wt's own document was skipped: %#v", changes)
	}
	m, err := readSkillManifest(l)
	if err != nil {
		t.Fatal(err)
	}
	rels, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels {
		body, err := skillBody(rel)
		if err != nil {
			t.Fatal(err)
		}
		if installedSkill(t, l, rel) != string(body) {
			t.Errorf("adopting %s changed the document", rel)
		}
		if m.Files[rel] != digest(body) {
			t.Errorf("%s was not recorded after adoption", rel)
		}
	}
}

// With no record, a file that is *not* what this binary would write is
// genuinely ambiguous, and the refusal says so instead of claiming an edit
// it cannot know about.
func TestUnrecordedAndDifferentIsReportedHonestly(t *testing.T) {
	l := installedLayout(t)
	const rel = "SKILL.md"
	writeSkill(t, l, rel, "# something else\n")
	if err := os.Remove(l.ManifestPath()); err != nil {
		t.Fatal(err)
	}

	changes, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	skip, ok := hasAction(changes, "skipped")
	if !ok {
		t.Fatalf("changes = %#v, want the ambiguous file skipped", changes)
	}
	if strings.Contains(skip.Detail, "edited since") {
		t.Errorf("the skip claims an edit it cannot know about: %q", skip.Detail)
	}
	if !strings.Contains(skip.Detail, "either an edit") {
		t.Errorf("the skip does not say what it actually knows: %q", skip.Detail)
	}
}

// A skipped file keeps its old record, so a later run still recognises a
// copy wt wrote two releases ago rather than forgetting it wrote it.
func TestASkippedFileKeepsItsRecord(t *testing.T) {
	l := installedLayout(t)
	const rel = "SKILL.md"
	before, err := readSkillManifest(l)
	if err != nil {
		t.Fatal(err)
	}
	writeSkill(t, l, rel, "# the user's\n")

	if _, err := Install(l, Options{Skill: true, RefreshOnly: true}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	after, err := readSkillManifest(l)
	if err != nil {
		t.Fatal(err)
	}
	if after.Files[rel] != before.Files[rel] {
		t.Errorf("the skipped file's record changed: %q -> %q", before.Files[rel], after.Files[rel])
	}
	// The files that were not skipped keep working.
	rels, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range rels {
		if other == rel {
			continue
		}
		body, err := skillBody(other)
		if err != nil {
			t.Fatal(err)
		}
		if after.Files[other] != digest(body) {
			t.Errorf("the skip abandoned the record for %s", other)
		}
	}
}

// A second refresh over a current installation changes nothing.
func TestRefreshOfACurrentSkillIsIdempotent(t *testing.T) {
	l := installedLayout(t)
	rels, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, rel := range rels {
		before[rel] = installedSkill(t, l, rel)
	}

	changes, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	for _, c := range changes {
		if strings.HasPrefix(c.Path, l.SkillDir()) && c.Action != "unchanged" {
			t.Errorf("a current skill file was reported %q: %#v", c.Action, c)
		}
	}
	for _, rel := range rels {
		if installedSkill(t, l, rel) != before[rel] {
			t.Errorf("%s changed on a no-op refresh", rel)
		}
	}
}

// The installed documents are byte-identical to the ones this repository
// holds. The record is a sidecar precisely so that stays true — and so that
// references/primitives.md, which quotes the managed-block markers in an
// example, never gains a second real block.
func TestInstalledSkillIsVerbatim(t *testing.T) {
	l := installedLayout(t)
	rels, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels {
		body, err := skillBody(rel)
		if err != nil {
			t.Fatal(err)
		}
		if installedSkill(t, l, rel) != string(body) {
			t.Errorf("the installed %s is not the embedded document verbatim", rel)
		}
	}
}

// A record from a newer schema is refused rather than half-read, and
// nothing is written on the way to the refusal.
func TestANewerManifestIsRefused(t *testing.T) {
	l := installedLayout(t)
	const rel = "SKILL.md"
	writeSkill(t, l, rel, "# stale\n")
	if err := os.WriteFile(l.ManifestPath(),
		[]byte(`{"schema_version": 99, "files": {}}`), skillMode); err != nil {
		t.Fatal(err)
	}

	_, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err == nil {
		t.Fatal("a newer manifest was accepted, want a refusal")
	}
	if !strings.Contains(err.Error(), "upgrade wt") {
		t.Errorf("the refusal does not name the upgrade: %v", err)
	}
	if installedSkill(t, l, rel) != "# stale\n" {
		t.Error("the refused install wrote a file anyway")
	}
}

// A record that will not parse says nothing about the files, so it is
// treated as no record rather than failing the install — and the fallback
// is the same provable one.
func TestAnUnparseableManifestFallsBackToNoRecord(t *testing.T) {
	l := installedLayout(t)
	if err := os.WriteFile(l.ManifestPath(), []byte("{not json"), skillMode); err != nil {
		t.Fatal(err)
	}
	changes, err := Install(l, Options{Skill: true, RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh over an unparseable record: %v", err)
	}
	if _, ok := hasAction(changes, "skipped"); ok {
		t.Errorf("an unparseable record made wt disown its own files: %#v", changes)
	}
	var m skillManifest
	data, err := os.ReadFile(l.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("the record was not rewritten as valid JSON: %v", err)
	}
}

// Uninstall reads ownership the same way install does: it removes wt's own
// file however stale, refuses one the user made their own, and takes the
// record with the files.
func TestUninstallReadsTheSameOwnership(t *testing.T) {
	l := installedLayout(t)
	const rel = "SKILL.md"
	const stale = "# older\n\nA previous release.\n"
	writeSkill(t, l, rel, stale)
	m, err := readSkillManifest(l)
	if err != nil {
		t.Fatal(err)
	}
	m.Files[rel] = digest([]byte(stale))
	if err := writeSkillManifest(l, m); err != nil {
		t.Fatal(err)
	}

	if _, err := Uninstall(l, Options{Skill: true}); err != nil {
		t.Fatalf("uninstall over a stale skill file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.SkillDir(), rel)); err == nil {
		t.Error("uninstall left a stale file wt had written")
	}
	if _, err := os.Stat(l.ManifestPath()); err == nil {
		t.Error("uninstall left the record behind")
	}

	// And the other direction.
	if _, err := Install(l, Options{Skill: true}); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	mine := "# mine\n\nNot what wt wrote.\n"
	writeSkill(t, l, rel, mine)
	if _, err := Uninstall(l, Options{Skill: true}); err == nil {
		t.Error("uninstall removed a file the user had made their own")
	}
	if got := installedSkill(t, l, rel); got != mine {
		t.Error("the refused uninstall changed the file anyway")
	}
	if _, err := os.Stat(l.ManifestPath()); err != nil {
		t.Error("the refused uninstall removed the record describing what is still there")
	}
}
