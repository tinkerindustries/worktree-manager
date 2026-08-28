package artefact

// artefact_test.go pins the phase-7 generated surface: every artefact is
// rendered per spec and band with the managed block recording the fields
// it came from; regeneration preserves hand edits outside the block; the
// remove skill stops and asks on exit 3; the briefing refuses to render
// with the descriptor missing; and the two hook scripts behave at runtime
// (the tripwire against a real git worktree, the guard hook against a
// fake wt).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/managed"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// fixtureSpec is the plain-app spec the artefact tests render.
// shellForTest resolves the POSIX shell the way internal/platform does,
// so these scripts run under Git for Windows' sh.exe on Windows rather
// than a /bin/sh that exists on no such machine. A host with no POSIX
// shell skips: the generated hooks are shell scripts, and that host
// cannot run them at all.
func shellForTest(t *testing.T) string {
	t.Helper()
	sh, err := platform.ShellPath()
	if err != nil {
		t.Skipf("no POSIX shell on this machine: %v", err)
	}
	return sh
}

func fixtureSpec(t *testing.T) *spec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "plain-app", "wt.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture spec: %v", err)
	}
	sp, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("parsing the fixture spec: %v", err)
	}
	if err := spec.Validate(sp); err != nil {
		t.Fatalf("the fixture spec does not validate: %v", err)
	}
	return sp
}

// TestRenderProducesEveryArtefact: the seven generated files exist, each
// (apart from the JSON settings) carries the managed block with the field
// records, and the hook scripts are executable.
func TestRenderProducesEveryArtefact(t *testing.T) {
	sp := fixtureSpec(t)
	files, err := Render(sp, map[string]int{"api": 8200}, Options{GuardHook: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(files) != 5 {
		t.Fatalf("files = %d, want 5: %+v", len(files), paths(files))
	}
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	for _, p := range []string{HookTripwire, HookGuard, SettingsPath, ReferencePath, CLAUDEKPath} {
		if _, ok := byPath[p]; !ok {
			t.Errorf("no artefact for %s", p)
		}
	}
	for _, p := range []string{HookTripwire, HookGuard, ReferencePath, CLAUDEKPath} {
		f := byPath[p]
		block := strings.Join(managed.Render(f.Fields, f.BlockContent), "\n")
		if !strings.Contains(block, "# wt-field: app=plain-app") {
			t.Errorf("%s's block lacks the app field:\n%s", p, block)
		}
		if !strings.Contains(block, "# wt-field: band api=8200") {
			t.Errorf("%s's block lacks the band field:\n%s", p, block)
		}
		if !strings.Contains(block, "# wt-field: descriptor=wt-env.json") {
			t.Errorf("%s's block lacks the descriptor field:\n%s", p, block)
		}
		// Where the repository's worktrees go: the effective template,
		// so a repo that says nothing records the default and doctor
		// still reports the day it changes.
		if !strings.Contains(block, "# wt-field: worktrees=.claude/worktrees/{slug}") {
			t.Errorf("%s's block lacks the worktrees field:\n%s", p, block)
		}
	}
	for _, p := range []string{HookTripwire, HookGuard} {
		if byPath[p].Mode != 0o755 {
			t.Errorf("%s mode = %v, want 0755 (a hook must run)", p, byPath[p].Mode)
		}
	}
	// The settings file declares the guard hook only because the render
	// option asked for it (C6: opt-in, confirmed, never a default).
	var settings map[string]any
	if err := json.Unmarshal(byPath[SettingsPath].Body, &settings); err != nil {
		t.Fatalf("settings.json is not JSON: %v", err)
	}
	hooks := settings["hooks"].(map[string]any)
	if _, ok := hooks["SessionStart"]; !ok {
		t.Error("settings.json lacks the SessionStart entry")
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("settings.json lacks the PreToolUse entry despite GuardHook: true")
	}

	filesNoGuard, err := Render(sp, map[string]int{"api": 8200}, Options{})
	if err != nil {
		t.Fatalf("Render without the guard: %v", err)
	}
	for _, f := range filesNoGuard {
		if f.Path != SettingsPath {
			continue
		}
		var s map[string]any
		if err := json.Unmarshal(f.Body, &s); err != nil {
			t.Fatalf("settings.json: %v", err)
		}
		if _, ok := s["hooks"].(map[string]any)["PreToolUse"]; ok {
			t.Error("the PreToolUse hook is declared without the developer's confirmation")
		}
	}
}

// paths lists the file paths for failure messages.
func paths(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// TestApplyRegeneratePreservesHandEdits is exit criterion 4 end to end:
// generate into a repo, hand-edit the create skill, regenerate — the edit
// survives and the block is fresh.
func TestApplyRegeneratePreservesHandEdits(t *testing.T) {
	root := t.TempDir()
	sp := fixtureSpec(t)
	files, err := Render(sp, map[string]int{"api": 8200}, Options{GuardHook: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := Apply(root, files); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The generated reference doc exists, then a developer edits it.
	docPath := filepath.Join(root, filepath.FromSlash(ReferencePath))
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("the reference doc was not written: %v", err)
	}
	edited := strings.Replace(string(data),
		"## Why",
		"## Why\n\nA sentence the developer added by hand.", 1)
	if edited == string(data) {
		t.Fatal("the hand edit did not match the generated text")
	}
	if err := os.WriteFile(docPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("writing the edit: %v", err)
	}

	// Regeneration with a moved band: the block must refresh, the edit
	// must survive.
	files2, err := Render(sp, map[string]int{"api": 8300}, Options{GuardHook: true})
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}
	if err := Apply(root, files2); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	after, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("re-reading the reference doc: %v", err)
	}
	if !strings.Contains(string(after), "A sentence the developer added by hand.") {
		t.Errorf("the hand edit did not survive regeneration:\n%s", after)
	}
	block, ok, err := managed.Parse(after)
	if err != nil {
		t.Fatalf("parsing the regenerated reference doc: %v", err)
	}
	if !ok {
		t.Fatal("the regenerated reference doc lost its managed block")
	}
	if got, _ := block.Lookup("band api"); got != "8300" {
		t.Errorf("band api = %q after regeneration, want 8300 (the block must refresh)", got)
	}
}

// TestBriefingRefusesWithDescriptorMissing is exit criterion 6: the
// briefing refuses to render when wt show reports the descriptor missing.
func TestBriefingRefusesWithDescriptorMissing(t *testing.T) {
	sp := fixtureSpec(t)
	missing := []byte(`{"found":false,"reason":"no descriptor at /x/wt-env.json: this linked worktree has not been initialised — run: wt init","outcome":"linked-worktree","worktree_root":"/x"}`)
	_, err := Briefing(sp, missing)
	if err == nil {
		t.Fatal("the briefing rendered without a descriptor")
	}
	if !strings.Contains(err.Error(), "wt init") {
		t.Errorf("the refusal does not name wt init: %v", err)
	}
	if !strings.Contains(err.Error(), "guessed") {
		t.Errorf("the refusal does not state the no-guessing rule: %v", err)
	}
}

// TestBriefingRendersFromShowValues: with a descriptor, every value comes
// from the show output — the path prefix, the values, the shared list.
func TestBriefingRendersFromShowValues(t *testing.T) {
	sp := fixtureSpec(t)
	// A descriptor's path is a native path, so the fixture's is one too:
	// the briefing joins the descriptor filename onto it with
	// filepath.Join, and a POSIX literal would make the expectation wrong
	// on Windows for no reason of the renderer's.
	root := filepath.FromSlash("/wt/brisk-otter")
	d := &descriptor.Descriptor{
		Version: 1, App: "plain-app", Slug: "brisk-otter", Slot: 2,
		Path: root,
		Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 8202},
			"db":  {Type: "state-path", Value: filepath.FromSlash("/home/u/.plain-app/worktrees/brisk-otter-2/db.sqlite")},
		},
		Shared: []descriptor.Shared{{Name: "shared_db", Impact: "writes are visible to every worktree"}},
	}
	show := map[string]any{"found": true, "worktree_root": root, "descriptor": d}
	data, err := json.Marshal(show)
	if err != nil {
		t.Fatalf("marshalling the show verdict: %v", err)
	}
	out, err := Briefing(sp, data)
	if err != nil {
		t.Fatalf("Briefing: %v", err)
	}
	for _, want := range []string{"brisk-otter", "slot 2", "8202", "db.sqlite", "shared_db", "writes are visible to every worktree", root, "--env " + filepath.Join(root, "wt-env.json"), "Not for this session"} {
		if !strings.Contains(out, want) {
			t.Errorf("the briefing lacks %q:\n%s", want, out)
		}
	}
}

// TestTripwireScriptRuntime runs the rendered SessionStart hook against a
// real git worktree: with a descriptor it prints the one-line summary;
// without one it names wt init; in the primary checkout it prints
// nothing. The script reads its own managed block, so this exercises the
// block-at-runtime rule.
func TestTripwireScriptRuntime(t *testing.T) {
	sp := fixtureSpec(t)
	files, err := Render(sp, map[string]int{"api": 8200}, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var hook string
	for _, f := range files {
		if f.Path == HookTripwire {
			hook = string(f.Body) + "\n\n" + strings.Join(managed.Render(f.Fields, f.BlockContent), "\n") + "\n"
		}
	}
	hookPath := filepath.Join(t.TempDir(), "wt-session-start.sh")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("writing the hook: %v", err)
	}

	// The repository: main checkout plus one linked worktree, the spec
	// committed, the descriptor written into the worktree.
	base := t.TempDir()
	main := filepath.Join(base, "main")
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), []byte(fixtureSpecYAML(t)), 0o644); err != nil {
		t.Fatalf("writing the spec: %v", err)
	}
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "fixture")
	wt := filepath.Join(base, "brisk-otter")
	gitT(t, main, "worktree", "add", "-b", "brisk-otter", wt, "main")

	run := func(dir string) string {
		t.Helper()
		cmd := exec.Command(shellForTest(t), hookPath)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the tripwire failed in %s: %v\n%s", dir, err, out)
		}
		return string(out)
	}

	// No descriptor: the sentence naming wt init.
	if out := run(wt); !strings.Contains(out, "no environment yet") || !strings.Contains(out, "wt init") {
		t.Errorf("without a descriptor the tripwire said: %q", out)
	}
	// Primary checkout: nothing to say.
	if out := run(main); out != "" {
		t.Errorf("the primary checkout printed %q, want nothing", out)
	}

	// With a descriptor: the one-line summary. The descriptor carries the
	// slug, slot and the resolved api port.
	d := &descriptor.Descriptor{
		Version: 1, App: "plain-app", Slug: "brisk-otter", Slot: 2, Path: wt,
		Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 8202},
			"db":  {Type: "state-path", Value: "/home/u/.plain-app/worktrees/brisk-otter-2/db.sqlite"},
		},
		Extras: map[string]any{},
	}
	if err := descriptor.Write(filepath.Join(wt, "wt-env.json"), "json", d); err != nil {
		t.Fatalf("writing the descriptor: %v", err)
	}
	out := run(wt)
	for _, want := range []string{"brisk-otter", "slot 2", "api=8202"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "db=") {
		t.Errorf("the summary listed a non-port resource: %q", out)
	}

	// The shared block arrives on the same breath. It is the part that has
	// to be read before the writes it warns about get made, so it belongs
	// on arrival rather than waiting to be asked for; the impacts are one
	// named command away.
	for _, want := range []string{"not isolated", "shared_db", "wt show --brief"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks the shared block's %q: %q", want, out)
		}
	}
}

// A repository whose spec declares nothing shared says nothing about it:
// the line is a warning, and a warning with nothing in it trains a reader
// to skip the whole summary.
func TestTripwireSaysNothingAboutAnEmptySharedBlock(t *testing.T) {
	sp := fixtureSpec(t)
	sp.Shared = nil
	for i := range sp.Resources {
		if sp.Resources[i].Type == "state-path" {
			sp.Resources[i].Default = nil
		}
	}
	files, err := Render(sp, map[string]int{"api": 8200}, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, f := range files {
		if f.Path != HookTripwire {
			continue
		}
		block := strings.Join(managed.Render(f.Fields, f.BlockContent), "\n")
		if strings.Contains(block, "# wt-field: shared=\n") {
			continue
		}
		if got := f.Fields["shared"]; got != "" {
			t.Errorf("shared field = %q, want empty", got)
		}
	}
}

// TestGuardHookScriptRuntime runs the rendered PreToolUse hook against a
// fake wt: a denial exits 2 with the deny JSON, an allowance exits 0 with
// the allow JSON, and a missing wt fails open.
func TestGuardHookScriptRuntime(t *testing.T) {
	sp := fixtureSpec(t)
	files, err := Render(sp, map[string]int{"api": 8200}, Options{GuardHook: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var hook string
	for _, f := range files {
		if f.Path == HookGuard {
			hook = string(f.Body) + "\n\n" + strings.Join(managed.Render(f.Fields, f.BlockContent), "\n") + "\n"
		}
	}
	binDir := t.TempDir()
	hookPath := filepath.Join(t.TempDir(), "wt-guard.sh")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil {
		t.Fatalf("writing the hook: %v", err)
	}

	// Fake wt: reports whether WT_GUARD_CACHE was set (into the file the
	// test names), and answers per the FAKE_GUARD_EXIT variable.
	report := filepath.Join(t.TempDir(), "cache-report")
	fake := filepath.Join(binDir, "wt")
	fakeBody := `#!/bin/sh
echo "guard cache: ${WT_GUARD_CACHE:-unset}" > "` + report + `"
code="${FAKE_GUARD_EXIT:-0}"
if [ "$code" = "3" ]; then
  echo '{"allowed": false, "reason": "write to /etc/passwd is refused: it lies outside this worktree; the worktree root is /repo/wt"}'
  echo "denied: write to /etc/passwd is refused" >&2
  exit 3
fi
echo '{"allowed": true, "reason": "write to /repo/wt/x is inside this worktree"}'
exit "$code"
`
	if err := os.WriteFile(fake, []byte(fakeBody), 0o755); err != nil {
		t.Fatalf("writing the fake wt: %v", err)
	}

	run := func(env []string, stdin string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(shellForTest(t), hookPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Env = append(cmd.Env, env...)
		cmd.Stdin = strings.NewReader(stdin)
		var out, errb strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("running the hook: %v", err)
		}
		return code, out.String(), errb.String()
	}
	payload := `{"tool_name":"Write","tool_input":{"file_path":"/repo/wt/x"}}`

	// The hook's stdout is the PreToolUse hookSpecificOutput object, and
	// Claude Code reads it by field: hookEventName is the event's name,
	// permissionDecision is its sibling, and a denial's reason field is
	// permissionDecisionReason. A shape that merely contains the decision
	// somewhere is not the contract, so each case is decoded and checked.
	decision := func(t *testing.T, out string) (string, string) {
		t.Helper()
		var got struct {
			HookSpecificOutput struct {
				HookEventName            string `json:"hookEventName"`
				PermissionDecision       string `json:"permissionDecision"`
				PermissionDecisionReason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		line := strings.TrimSpace(out)
		if i := strings.LastIndex(line, "\n"); i >= 0 {
			line = line[i+1:]
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("hook stdout is not the hook output object: %v (%q)", err, out)
		}
		if got.HookSpecificOutput.HookEventName != "PreToolUse" {
			t.Errorf("hookEventName = %q, want PreToolUse", got.HookSpecificOutput.HookEventName)
		}
		return got.HookSpecificOutput.PermissionDecision, got.HookSpecificOutput.PermissionDecisionReason
	}

	// Allowed.
	code, out, _ := run(nil, payload)
	if d, _ := decision(t, out); code != 0 || d != "allow" {
		t.Errorf("allow: exit %d, decision %q", code, d)
	}

	// Denied: exit 2, the deny JSON with the reason, and WT_GUARD_CACHE set.
	code, out, _ = run([]string{"FAKE_GUARD_EXIT=3"}, payload)
	if code != 2 {
		t.Errorf("deny: exit %d, want 2", code)
	}
	d, reason := decision(t, out)
	if d != "deny" {
		t.Errorf("deny: decision %q, want deny", d)
	}
	if !strings.Contains(reason, "worktree root is /repo/wt") {
		t.Errorf("deny: the reason did not reach the model: %q", reason)
	}
	// The fake wt reported whether the hook exported WT_GUARD_CACHE.
	rep, _ := os.ReadFile(report)
	if !strings.Contains(string(rep), "guard cache:") || strings.Contains(string(rep), "unset") {
		t.Errorf("the hook did not set WT_GUARD_CACHE: %q", rep)
	}

	// Guard fails: fail open and say so.
	code, out, errb := run([]string{"FAKE_GUARD_EXIT=7"}, payload)
	if d, _ := decision(t, out); code != 0 || d != "allow" {
		t.Errorf("guard failure: exit %d, decision %q", code, d)
	}
	if !strings.Contains(errb, "failed open") && !strings.Contains(errb, "allowed") {
		t.Errorf("the fail-open note is missing: %q", errb)
	}

	// wt missing: fail open.
	cmd := exec.Command(shellForTest(t), hookPath)
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
	cmd.Stdin = strings.NewReader(payload)
	outb, _ := cmd.CombinedOutput()
	if d, _ := decision(t, string(outb)); d != "allow" {
		t.Errorf("missing wt: decision %q, want allow", d)
	}
}

// gitT runs one git command (mirrors the cli test helper; kept local so
// the artefact tests stand alone).
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
}

// fixtureSpecYAML renders the fixture spec back to YAML for committing.
func fixtureSpecYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "plain-app", "wt.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture spec: %v", err)
	}
	return string(data)
}
