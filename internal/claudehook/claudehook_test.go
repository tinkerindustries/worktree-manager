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
		if got, want := commandFor(t, root, s.Event), shellQuote(l.ScriptPath(s.File)); got != want {
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
	if got, want := commandFor(t, readSettingsFile(t, l), CreateEvent), shellQuote(l.ScriptPath(createScript)); got != want {
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

// hasAction reports whether any change carries the action, and returns the
// first one that does.
func hasAction(changes []Change, action string) (Change, bool) {
	for _, c := range changes {
		if c.Action == action {
			return c, true
		}
	}
	return Change{}, false
}

// TestRefreshOnlyCreatesNothingWhenNotInstalled: an upgrade runs this on
// every machine, including ones that never registered the hooks.
// Registering them is the user's opt-in and the upgrade does not make it.
func TestRefreshOnlyCreatesNothingWhenNotInstalled(t *testing.T) {
	l := layoutIn(t)
	changes, err := Install(l, Options{WtBinary: "/opt/wt", RefreshOnly: true, Skill: true})
	if err != nil {
		t.Fatalf("refresh over an absent installation: %v", err)
	}
	if _, ok := hasAction(changes, "skipped"); !ok {
		t.Errorf("changes = %#v, want a skipped entry", changes)
	}
	for _, s := range scripts {
		if _, err := os.Stat(l.ScriptPath(s.File)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refresh created %s on a machine with no installation", s.File)
		}
	}
	if _, err := os.Stat(l.SettingsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refresh wrote settings on a machine with no installation")
	}
}

// TestRefreshOnlyUpdatesAStaleScript is the case the flag exists for: the
// scripts are embedded in the binary, so a new wt carries new copies and
// the ones on disk are whatever version wrote them.
func TestRefreshOnlyUpdatesAStaleScript(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{WtBinary: "/opt/wt"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	path := l.ScriptPath(createScript)
	stale := "#!/bin/sh\n# an older wt wrote this\nexit 0\n\n" +
		managed.StartMarker + "\n" + managed.FieldPrefix + "wt=/opt/wt\n" + managed.EndMarker + "\n"
	if err := os.WriteFile(path, []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}

	changes, err := Install(l, Options{WtBinary: "/opt/wt", RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, ok := hasAction(changes, "replace"); !ok {
		t.Errorf("changes = %#v, want the stale script replaced", changes)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "an older wt wrote this") {
		t.Error("the refresh left the stale body in place")
	}
}

// TestRefreshOnlySkipsAnEditedScriptAndContinues: a binary upgrade does
// not fail over a file the user chose to customise, and the rest of the
// refresh still happens.
func TestRefreshOnlySkipsAnEditedScriptAndContinues(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{WtBinary: "/opt/wt"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	edited := "#!/bin/sh\n# mine now\nexit 0\n"
	if err := os.WriteFile(l.ScriptPath(createScript), []byte(edited), 0o755); err != nil {
		t.Fatal(err)
	}
	// Make the other script stale, so the test can tell that the skip did
	// not abandon the rest of the refresh.
	stale := "#!/bin/sh\n# older\nexit 0\n\n" +
		managed.StartMarker + "\n" + managed.FieldPrefix + "wt=/opt/wt\n" + managed.EndMarker + "\n"
	if err := os.WriteFile(l.ScriptPath(removeScript), []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}

	changes, err := Install(l, Options{WtBinary: "/opt/wt", RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh over an edited script: %v", err)
	}
	skip, ok := hasAction(changes, "skipped")
	if !ok {
		t.Fatalf("changes = %#v, want the edited script skipped", changes)
	}
	if !strings.Contains(skip.Detail, "--force") {
		t.Errorf("the skip does not name the flag that would overwrite it: %q", skip.Detail)
	}
	got, err := os.ReadFile(l.ScriptPath(createScript))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Error("the refresh overwrote a script the user had edited")
	}
	other, err := os.ReadFile(l.ScriptPath(removeScript))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(other), "# older") {
		t.Error("the skip abandoned the rest of the refresh")
	}
}

// TestRefreshOnlySkipsAForeignRegistration: the same rule at the settings
// layer — a registration the user repointed is reported and left alone
// rather than failing the upgrade.
func TestRefreshOnlySkipsAForeignRegistration(t *testing.T) {
	l := layoutIn(t)
	if _, err := Install(l, Options{WtBinary: "/opt/wt"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	existing := `{"hooks": {"WorktreeCreate": [{"hooks": [{"type": "command", "command": "/opt/mine.sh"}]}]}}`
	if err := os.WriteFile(l.SettingsPath(), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := Install(l, Options{WtBinary: "/opt/wt", RefreshOnly: true})
	if err != nil {
		t.Fatalf("refresh over a foreign registration: %v", err)
	}
	if _, ok := hasAction(changes, "skipped"); !ok {
		t.Errorf("changes = %#v, want the refresh skipped", changes)
	}
	if got, want := commandFor(t, readSettingsFile(t, l), CreateEvent), "/opt/mine.sh"; got != want {
		t.Errorf("the refresh repointed the registration to %q, want %q left alone", got, want)
	}
}

// TestInstallMigratesAnUnquotedRegistrationToShellQuoted: an older wt
// registered the bare script path as the command. Claude Code runs a
// registered command as `sh -c <command>` — shell script text — and an
// unquoted Windows path's backslashes are sh's own escape character, so an
// unquoted registration is the bug this package now refuses to write.
// Re-running install over one must bring it up to the quoted form rather
// than reporting it already registered.
func TestInstallMigratesAnUnquotedRegistrationToShellQuoted(t *testing.T) {
	l := layoutIn(t)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	unquoted := l.ScriptPath(createScript)
	commandJSON, err := json.Marshal(unquoted)
	if err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks": {"WorktreeCreate": [{"hooks": [{"type": "command", "command": ` +
		string(commandJSON) + `}]}]}}`
	if err := os.WriteFile(l.SettingsPath(), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(l, Options{}); err != nil {
		t.Fatalf("install over an unquoted registration: %v", err)
	}
	if got, want := commandFor(t, readSettingsFile(t, l), CreateEvent), shellQuote(unquoted); got != want {
		t.Errorf("registered command = %q, want the quoted form %q", got, want)
	}
}

// TestShellQuoteSurvivesAWindowsPath proves the fix directly, independent
// of a shell: a backslash-separated path must round-trip through
// shellQuote and shellUnquote unchanged, because those backslashes are
// exactly what an unquoted registration loses to sh's escape processing.
func TestShellQuoteSurvivesAWindowsPath(t *testing.T) {
	path := `C:\Users\geoff\.claude\hooks\wt-worktree-create.sh`
	quoted := shellQuote(path)
	got, ok := shellUnquote(quoted)
	if !ok {
		t.Fatalf("shellUnquote(%q) reported not ok", quoted)
	}
	if got != path {
		t.Errorf("round-tripped to %q, want %q", got, path)
	}
	if commandPath(quoted) != path {
		t.Errorf("commandPath(%q) = %q, want %q", quoted, commandPath(quoted), path)
	}
	// An older, unquoted registration is still recognised by path.
	if commandPath(path) != path {
		t.Errorf("commandPath of an unquoted path changed it: %q", commandPath(path))
	}
}

// TestInstalledReportsWhatIsThere pins the signal RefreshOnly turns on.
func TestInstalledReportsWhatIsThere(t *testing.T) {
	l := layoutIn(t)
	switch present, err := Installed(l); {
	case err != nil:
		t.Fatalf("Installed on an empty directory: %v", err)
	case present:
		t.Error("Installed = true on a directory with no hooks")
	}
	if _, err := Install(l, Options{WtBinary: "/opt/wt"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	switch present, err := Installed(l); {
	case err != nil:
		t.Fatalf("Installed after install: %v", err)
	case !present:
		t.Error("Installed = false after install wrote both scripts")
	}
}
