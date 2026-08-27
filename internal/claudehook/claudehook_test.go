package claudehook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/managed"
)

// layoutIn is a Layout over a fresh temp directory.
func layoutIn(t *testing.T) Layout {
	t.Helper()
	return Layout{Dir: t.TempDir()}
}

// readSettingsFile reads the settings file back as a map.
func readSettingsFile(t *testing.T, l Layout) map[string]any {
	t.Helper()
	data, err := os.ReadFile(l.SettingsPath())
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings is not valid JSON: %v", err)
	}
	return root
}

// commandFor digs the registered command out of one event's entry.
func commandFor(t *testing.T, root map[string]any, event string) string {
	t.Helper()
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks object in settings")
	}
	cmd, recognised := registeredCommand(hooks[event])
	if !recognised {
		t.Fatalf("%s is not registered in the shape this package writes: %#v", event, hooks[event])
	}
	return cmd
}

func TestInstallWritesScriptsAndRegistersBothEvents(t *testing.T) {
	l := layoutIn(t)
	changes, err := Install(l, Options{WtBinary: "/opt/wt"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("install reported no changes")
	}

	for _, s := range scripts {
		path := l.ScriptPath(s.File)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		// Claude Code executes the hook directly; a script it cannot run
		// is not a hook. Windows has no execute bit to check — Go reports
		// -rw-rw-rw- for every file there — and a hook command is run
		// through a shell on that platform anyway.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %v)", s.File, info.Mode().Perm())
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(body), managed.StartMarker) {
			t.Errorf("%s carries no managed block", s.File)
		}
		if !strings.Contains(string(body), managed.FieldPrefix+"wt=/opt/wt") {
			t.Errorf("%s does not record the wt binary it should call", s.File)
		}
	}

	root := readSettingsFile(t, l)
	for _, s := range scripts {
		if got, want := commandFor(t, root, s.Event), l.ScriptPath(s.File); got != want {
			t.Errorf("%s registered to %q, want %q", s.Event, got, want)
		}
	}
}

func TestInstallPreservesEverythingElseInSettings(t *testing.T) {
	l := layoutIn(t)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "model": "opus",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "mk sync"}]}]
  },
  "permissions": {"defaultMode": "auto"}
}`
	if err := os.WriteFile(l.SettingsPath(), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(l, Options{}); err != nil {
		t.Fatalf("install: %v", err)
	}

	root := readSettingsFile(t, l)
	if root["model"] != "opus" {
		t.Errorf("model was lost: %#v", root["model"])
	}
	if _, ok := root["permissions"].(map[string]any); !ok {
		t.Errorf("permissions was lost: %#v", root["permissions"])
	}
	if got := commandFor(t, root, "Stop"); got != "mk sync" {
		t.Errorf("the user's Stop hook was lost: %q", got)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{Skill: true}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	changes, err := Install(l, Options{Skill: true})
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	for _, c := range changes {
		if c.Action != "unchanged" {
			t.Errorf("second install %s %s, want every change to be unchanged", c.Action, c.Path)
		}
	}
}

func TestInstallRefusesAForeignRegistrationAndWritesNothing(t *testing.T) {
	l := layoutIn(t)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks": {"WorktreeCreate": [{"hooks": [{"type": "command", "command": "/opt/mine.sh"}]}]}}`
	if err := os.WriteFile(l.SettingsPath(), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Install(l, Options{})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("install over a foreign hook returned %v, want a ConflictError", err)
	}
	if !strings.Contains(conflict.Error(), "/opt/mine.sh") {
		t.Errorf("the refusal does not name what is registered: %v", conflict)
	}
	// The refusal is validated before anything is written, so a refused
	// install leaves no scripts behind on the way to it.
	if _, err := os.Stat(l.ScriptPath(createScript)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused install left %s behind", createScript)
	}

	// --force is the way past it, and takes the registration over.
	if _, err := Install(l, Options{Force: true}); err != nil {
		t.Fatalf("forced install: %v", err)
	}
	if got, want := commandFor(t, readSettingsFile(t, l), CreateEvent), l.ScriptPath(createScript); got != want {
		t.Errorf("forced install registered %q, want %q", got, want)
	}
}

func TestInstallRefusesUnparseableSettings(t *testing.T) {
	l := layoutIn(t)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.SettingsPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(l, Options{}); err == nil {
		t.Fatal("install over unparseable settings succeeded, want a refusal")
	}
	// The file the tool could not read is the file the tool does not
	// replace.
	data, err := os.ReadFile(l.SettingsPath())
	if err != nil || string(data) != "{not json" {
		t.Errorf("the unparseable settings file was modified: %q, %v", data, err)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	l := layoutIn(t)
	changes, err := Install(l, Options{DryRun: true, Skill: true})
	if err != nil {
		t.Fatalf("dry-run install: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("dry run reported no changes")
	}
	for _, c := range changes {
		if _, err := os.Stat(c.Path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("dry run created %s", c.Path)
		}
	}
}

func TestUninstallRemovesOnlyWhatInstallAdded(t *testing.T) {
	l := layoutIn(t)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"model": "opus", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "mk sync"}]}]}}`
	if err := os.WriteFile(l.SettingsPath(), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(l, Options{Skill: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// A file the user put in the hooks directory is not this tool's.
	theirs := filepath.Join(l.HooksDir(), "mine.sh")
	if err := os.WriteFile(theirs, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Uninstall(l, Options{Skill: true}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	root := readSettingsFile(t, l)
	hooks, _ := root["hooks"].(map[string]any)
	if _, still := hooks[CreateEvent]; still {
		t.Error("uninstall left WorktreeCreate registered")
	}
	if _, still := hooks[RemoveEvent]; still {
		t.Error("uninstall left WorktreeRemove registered")
	}
	if got := commandFor(t, root, "Stop"); got != "mk sync" {
		t.Errorf("uninstall removed the user's Stop hook: %q", got)
	}
	if root["model"] != "opus" {
		t.Error("uninstall removed an unrelated setting")
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("uninstall removed a file it did not write: %v", err)
	}
	if _, err := os.Stat(l.SkillDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("uninstall left the skill directory behind: %v", err)
	}
}

func TestUninstallRefusesAnEditedScript(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// A script without the managed marker is not one this tool wrote.
	if err := os.WriteFile(l.ScriptPath(createScript), []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Uninstall(l, Options{})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("uninstall over a foreign script returned %v, want a ConflictError", err)
	}
	if _, err := os.Stat(l.ScriptPath(createScript)); err != nil {
		t.Errorf("the refused uninstall removed the file anyway: %v", err)
	}
}

func TestUninstallDropsAnEmptyHooksObject(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall(l, Options{}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	root := readSettingsFile(t, l)
	if _, present := root["hooks"]; present {
		t.Errorf("uninstall left an empty hooks object: %#v", root["hooks"])
	}
}

func TestUninstallOfNothingIsNotAnError(t *testing.T) {
	l := layoutIn(t)
	changes, err := Uninstall(l, Options{Skill: true})
	if err != nil {
		t.Fatalf("uninstall with nothing installed: %v", err)
	}
	for _, c := range changes {
		if c.Action != "unchanged" {
			t.Errorf("uninstall of nothing reported %s %s", c.Action, c.Path)
		}
	}
}

// TestRegisteredCommandRejectsRicherShapes proves the ownership test is
// conservative: anything this package would not have written reads as
// unrecognised, so install refuses instead of flattening it.
func TestRegisteredCommandRejectsRicherShapes(t *testing.T) {
	cases := map[string]string{
		"two groups":     `[{"hooks":[{"type":"command","command":"a"}]},{"hooks":[{"type":"command","command":"b"}]}]`,
		"two hooks":      `[{"hooks":[{"type":"command","command":"a"},{"type":"command","command":"b"}]}]`,
		"a matcher":      `[{"matcher":"Edit","hooks":[{"type":"command","command":"a"}]}]`,
		"not a command":  `[{"hooks":[{"type":"prompt","prompt":"a"}]}]`,
		"not an array":   `{"hooks":[{"type":"command","command":"a"}]}`,
		"no command key": `[{"hooks":[{"type":"command"}]}]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatal(err)
			}
			if _, recognised := registeredCommand(v); recognised {
				t.Errorf("%s was recognised as this package's shape", name)
			}
		})
	}
}

// TestSkillIsOptional proves --skill=false leaves the skills directory
// untouched in both directions.
func TestSkillIsOptional(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{Skill: false}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(l.SkillDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--skill=false installed the skill anyway: %v", err)
	}
}

// TestInstalledSkillMatchesTheEmbeddedCopy proves the installed skill is
// the document this repository holds, file for file.
func TestInstalledSkillMatchesTheEmbeddedCopy(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{Skill: true}); err != nil {
		t.Fatalf("install: %v", err)
	}
	rels, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) == 0 {
		t.Fatal("the embedded skill has no files")
	}
	for _, rel := range rels {
		want, err := skillBody(rel)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(l.SkillDir(), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading the installed %s: %v", rel, err)
		}
		if string(got) != string(want) {
			t.Errorf("the installed %s differs from the embedded copy", rel)
		}
	}
}

// TestEmbeddedSkillMatchesTheRepositorysCopy is the drift gate: the
// embedded skill under skill/ is a copy of .claude/skills/worktree-onboarding,
// which is the source of truth and the one this repository's own sessions
// load. An edit to one without the other fails here rather than shipping a
// binary that installs a stale document.
func TestEmbeddedSkillMatchesTheRepositorysCopy(t *testing.T) {
	source := filepath.Join("..", "..", ".claude", "skills", "worktree-onboarding")
	rels, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, rel := range rels {
		seen[rel] = true
		want, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s from the repository's skill: %v — the embedded copy has a file the source does not", rel, err)
		}
		got, err := skillBody(rel)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("internal/claudehook/skill/%s has drifted from .claude/skills/worktree-onboarding/%s — copy the source over it", rel, rel)
		}
	}
	err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(source, path)
		if rerr != nil {
			return rerr
		}
		if !seen[filepath.ToSlash(rel)] {
			t.Errorf(".claude/skills/worktree-onboarding/%s is not in the embedded copy — copy it into internal/claudehook/skill", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository's skill: %v", err)
	}
}
