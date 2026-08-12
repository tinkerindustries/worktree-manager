package cli

// init_test.go exercises `wt init` end to end in-process: real git
// worktrees for the classification and the emission, and the recording
// fake coordinator for the allocation half. Each test is named for the
// rail or exit criterion it proves.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/envfile"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// lifecycleFixture builds a git repository with one linked worktree and a
// committed wt.yaml, and returns the main checkout path and the worktree
// root.
func lifecycleFixture(t *testing.T, sp *spec.Spec) (main, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	writeT(t, filepath.Join(main, "file.txt"), "one\n")
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	writeT(t, filepath.Join(main, "wt.yaml"), string(data))
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "initial")
	worktree = filepath.Join(base, "wt-1")
	gitT(t, main, "worktree", "add", "-b", "wt-1", worktree, "main")
	return main, worktree
}

// gitT runs one git command.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", args, strings.TrimSpace(string(out)))
	}
}

// writeT writes a file.
func writeT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// lifecycleSpec is the committed spec the init tests adopt: trivial hooks
// (no docker, no go), a port and a state path, and the .env block.
func lifecycleSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "lifecycle-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
			{Type: "state-path", Name: "db",
				Template: strPtr("{home}/.lifecycle-app/{slug}-{slot}/db.sqlite")},
		},
		Hooks: spec.Hooks{
			Install: &spec.Hook{Run: "true"},
			Prepull: &spec.Hook{Run: "true"},
			Build:   &spec.Hook{Run: "true"},
			Start:   &spec.Hook{Run: "true"},
			Seed:    &spec.Hook{Run: "true"},
			Health:  &spec.Hook{Run: "true"},
		},
		Emit: spec.Emit{
			Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"},
			Env:        &spec.EnvEmit{Path: ".env", Keys: map[string]string{"API_PORT": "{api}"}},
		},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("lifecycle spec does not validate: %v", err)
	}
	return s
}

// recordingCoord is the fake coordinator for the lifecycle verbs: it
// records every request by verb and answers from a per-verb handler map
// the test installs.
type recordingCoord struct {
	requests map[string][]json.RawMessage
}

// newRecordingCoord runs the fake on sock and returns the recorder.
func newRecordingCoord(t *testing.T, sock string, handlers map[string]func(*protocol.Request) *protocol.Response) *recordingCoord {
	t.Helper()
	rc := &recordingCoord{requests: map[string][]json.RawMessage{}}
	// The fake's dispatch is per verb, so the recorder registers itself
	// under every lifecycle verb.
	all := map[string]func(*protocol.Request) *protocol.Response{}
	for _, verb := range []string{"allocate", "materialise", "activate", "release", "rm"} {
		all[verb] = func(req *protocol.Request) *protocol.Response {
			rc.requests[req.Verb] = append(rc.requests[req.Verb], req.Args)
			if h, ok := handlers[req.Verb]; ok {
				return h(req)
			}
			return &protocol.Response{Error: &protocol.Error{Code: 1, Msg: "fake coordinator: no canned response for " + req.Verb, Remedy: "test defect"}}
		}
	}
	fakeCoordServer(t, sock, all)
	return rc
}

// allocateOK is the canned allocate response: the lowest slot, one port
// and one state path, at the given worktree.
func allocateOK(worktree string) *protocol.Response {
	return &protocol.Response{Result: mustJSONT(protocol.AllocateResult{
		App: "lifecycle-app", Slug: "wt-1", Slot: 1, State: "reserving",
		Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 4201},
			"db":  {Type: "state-path", Value: "/home/test/.lifecycle-app/wt-1-1/db.sqlite"},
		},
		Path: worktree,
	})}
}

func mustJSONT(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func cannedT(v any) func(*protocol.Request) *protocol.Response {
	return func(*protocol.Request) *protocol.Response { return &protocol.Response{Result: mustJSONT(v)} }
}

// TestInitAllocatesEmitsActivates: the happy path — the seven steps run,
// the descriptor and the .env block land in the worktree, and the entry is
// activated.
func TestInitAllocatesEmitsActivates(t *testing.T) {
	sp := lifecycleSpec(t)
	main, worktree := lifecycleFixture(t, sp)
	sock := shortSock(t, "init")
	rc := newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate":    func(*protocol.Request) *protocol.Response { return allocateOK(worktree) },
		"materialise": cannedT(&protocol.MaterialiseResult{App: "lifecycle-app", Slug: "wt-1", State: "reserving"}),
		"activate":    cannedT(&protocol.ActivateResult{App: "lifecycle-app", Slug: "wt-1", State: "active"}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, stdout, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitOK {
		t.Fatalf("init exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "wt-1") {
		t.Errorf("stdout = %q, want the slug named", stdout)
	}
	// Step 4's emission: the descriptor and the .env block.
	dpath := filepath.Join(worktree, "wt-env.yaml")
	if _, err := os.Stat(dpath); err != nil {
		t.Fatalf("descriptor not written: %v", err)
	}
	d, err := descriptor.Read(dpath, "yaml")
	if err != nil {
		t.Fatalf("reading the descriptor: %v", err)
	}
	if d.Slot != 1 || d.App != "lifecycle-app" || d.Resources["api"].Value != 4201 {
		t.Errorf("descriptor = %+v, want slot 1 with the allocated port", d)
	}
	envData, err := os.ReadFile(filepath.Join(worktree, ".env"))
	if err != nil {
		t.Fatalf(".env not written: %v", err)
	}
	if !strings.Contains(string(envData), "API_PORT=4201") || !strings.Contains(string(envData), envfile.StartMarker) {
		t.Errorf(".env = %q, want the managed block with API_PORT=4201", envData)
	}
	// The sequence reached activation and ran all six hooks.
	if len(rc.requests["allocate"]) != 1 || len(rc.requests["materialise"]) != 1 || len(rc.requests["activate"]) != 1 {
		t.Errorf("request counts = allocate %d materialise %d activate %d, want 1 1 1",
			len(rc.requests["allocate"]), len(rc.requests["materialise"]), len(rc.requests["activate"]))
	}
	// The gitignore: this fixture commits no .gitignore, so the line went
	// to $GIT_COMMON_DIR/info/exclude and git honours it.
	if out, err := gitOutT(t, worktree, "check-ignore", "wt-env.yaml"); err != nil || !strings.Contains(out, "wt-env.yaml") {
		t.Errorf("git check-ignore = %q (err %v), want the descriptor ignored", out, err)
	}
	_ = main
}

// gitOutT runs git and returns its trimmed stdout.
func gitOutT(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// TestInitRequiresDescription: a missing description fails asking for the
// flag (04-lifecycle.md §3.2) — non-interactive, exit 2.
func TestInitRequiresDescription(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	code, _, stderr := runCLI(t, "init", "--cwd", worktree)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "--description") {
		t.Errorf("stderr = %q, want the flag named", stderr)
	}
}

// TestInitRefusesPrimaryCheckout: the primary checkout is slot 0, never
// managed — exit 3, naming the standalone declaration as the alternative.
func TestInitRefusesPrimaryCheckout(t *testing.T) {
	sp := lifecycleSpec(t)
	main, _ := lifecycleFixture(t, sp)
	code, _, stderr := runCLI(t, "init", "--cwd", main, "--description", "the main checkout")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "slot 0") || !strings.Contains(stderr, "WT_STANDALONE") {
		t.Errorf("stderr = %q, want slot 0 and the standalone alternative named", stderr)
	}
}

// TestInitRefusesNestedWorktree: a tree nested inside another worktree is
// refused with exit 3, naming the nesting.
func TestInitRefusesNestedWorktree(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	nested := filepath.Join(worktree, "nested")
	gitT(t, worktree, "clone", worktree, nested)
	// A clone classifies as a primary checkout without the declaration; the
	// standalone declaration is what makes it a standalone clone, which is
	// the outcome the nesting check applies to.
	t.Setenv("WT_STANDALONE", "1")
	code, _, stderr := runCLI(t, "init", "--cwd", nested, "--description", "the nested clone")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "nested inside another git working tree") {
		t.Errorf("stderr = %q, want the nesting refusal", stderr)
	}
}

// TestInitSlugCollisionWithDifferentPathStops: a slug that already names a
// different path under the same app stops with exit 3, asking for an
// explicit slug (04-lifecycle.md §2.3).
func TestInitSlugCollisionWithDifferentPathStops(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sock := shortSock(t, "collide")
	newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate": func(*protocol.Request) *protocol.Response {
			return &protocol.Response{Result: mustJSONT(protocol.AllocateResult{
				App: "lifecycle-app", Slug: "wt-1", Slot: 1, State: "reserving",
				Resources: map[string]spec.Resolved{"api": {Type: "port", Value: 4201}},
				Path:      other, Existed: true,
			})}
		},
	})
	t.Setenv("WT_SOCKET", sock)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitRefused, stderr)
	}
	if !strings.Contains(stderr, "--slug") {
		t.Errorf("stderr = %q, want the explicit-slug remedy", stderr)
	}
}

// TestInitMaterialiseFailureReleasesTheEntry is exit criterion 3's client
// half: a materialisation failure with a clean rollback releases the
// entry, and nothing was emitted.
func TestInitMaterialiseFailureReleasesTheEntry(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	sock := shortSock(t, "matfail")
	rc := newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate":    func(*protocol.Request) *protocol.Response { return allocateOK(worktree) },
		"materialise": cannedT(&protocol.MaterialiseResult{App: "lifecycle-app", Slug: "wt-1", State: "reserving", Failed: "db", Err: "apply failed on purpose"}),
		"release":     cannedT(&protocol.ReleaseResult{App: "lifecycle-app", Slug: "wt-1", Removed: true}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitFailure, stderr)
	}
	if len(rc.requests["release"]) != 1 {
		t.Errorf("release requests = %d, want 1 (the client drives the rollback)", len(rc.requests["release"]))
	}
	if len(rc.requests["activate"]) != 0 {
		t.Errorf("activate requests = %d, want 0 (nothing past a failed materialisation)", len(rc.requests["activate"]))
	}
	// Nothing was emitted — materialisation precedes emission.
	if _, err := os.Stat(filepath.Join(worktree, "wt-env.yaml")); err == nil {
		t.Error("the descriptor exists despite the materialisation failure")
	}
}

// TestInitMaterialiseFailureWithFailedRollbackKeepsEntry: when the
// rollback itself failed, the entry stays tearing-down and the client must
// not release it — resources are out there (B2.3).
func TestInitMaterialiseFailureWithFailedRollbackKeepsEntry(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	sock := shortSock(t, "matrollback")
	rc := newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate": func(*protocol.Request) *protocol.Response { return allocateOK(worktree) },
		"materialise": cannedT(&protocol.MaterialiseResult{
			App: "lifecycle-app", Slug: "wt-1", State: "tearing-down",
			Failed: "db", Err: "apply failed", RollbackErr: "teardown failed too",
		}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitFailure, stderr)
	}
	if len(rc.requests["release"]) != 0 {
		t.Errorf("release requests = %d, want 0 (the entry stays tearing-down with the slot held)", len(rc.requests["release"]))
	}
	if !strings.Contains(stderr, "tearing-down") {
		t.Errorf("stderr = %q, want the tearing-down state named", stderr)
	}
}

// TestInitHealthFailureKeepsTheWorktreeAllocated is exit criterion 4: a
// failed bring-up leaves the worktree allocated and usable, and says what
// failed — the entry was activated and nothing was released.
func TestInitHealthFailureKeepsTheWorktreeAllocated(t *testing.T) {
	sp := lifecycleSpec(t)
	sp.Hooks.Start = &spec.Hook{Run: "echo stack failed; exit 7"}
	_, worktree := lifecycleFixture(t, sp)
	sock := shortSock(t, "healthfail")
	rc := newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate":    func(*protocol.Request) *protocol.Response { return allocateOK(worktree) },
		"materialise": cannedT(&protocol.MaterialiseResult{App: "lifecycle-app", Slug: "wt-1", State: "reserving"}),
		"activate":    cannedT(&protocol.ActivateResult{App: "lifecycle-app", Slug: "wt-1", State: "active"}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, ExitFailure, stderr)
	}
	if len(rc.requests["activate"]) != 1 {
		t.Errorf("activate requests = %d, want 1 (activation happened before the bring-up phase)", len(rc.requests["activate"]))
	}
	if len(rc.requests["release"]) != 0 {
		t.Errorf("release requests = %d, want 0 (a failed bring-up never releases the allocation)", len(rc.requests["release"]))
	}
	if !strings.Contains(stderr, "exit code 7") {
		t.Errorf("stderr = %q, want the hook failure surfaced", stderr)
	}
	// The descriptor stays: the worktree is allocated and usable.
	if _, err := os.Stat(filepath.Join(worktree, "wt-env.yaml")); err != nil {
		t.Errorf("descriptor missing after the failed bring-up: %v", err)
	}
}

// TestInitReemitsDescriptorFromEntry is attach outcome 3: an entry exists,
// the descriptor is missing, and init re-emits it from the entry and says
// so.
func TestInitReemitsDescriptorFromEntry(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	sock := shortSock(t, "reemit")
	newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate": func(*protocol.Request) *protocol.Response {
			res := allocateOK(worktree)
			var a protocol.AllocateResult
			json.Unmarshal(res.Result, &a)
			a.Existed = true
			res.Result = mustJSONT(a)
			return res
		},
		"materialise": cannedT(&protocol.MaterialiseResult{App: "lifecycle-app", Slug: "wt-1", State: "reserving"}),
		"activate":    cannedT(&protocol.ActivateResult{App: "lifecycle-app", Slug: "wt-1", State: "active"}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "re-emitted") {
		t.Errorf("stderr = %q, want the re-emission stated", stderr)
	}
	if _, err := os.Stat(filepath.Join(worktree, "wt-env.yaml")); err != nil {
		t.Errorf("descriptor missing after the re-emission: %v", err)
	}
}

// TestInitRebuildsRegistryFromDescriptor is attach outcome 4: a descriptor
// exists and no entry does — the client sends the descriptor's slot and
// the coordinator rebuilds the entry at it.
func TestInitRebuildsRegistryFromDescriptor(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	// Plant the descriptor before init runs.
	d := &descriptor.Descriptor{
		Version: descriptor.Version, App: "lifecycle-app", Slug: "wt-1", Slot: 3,
		Path: worktree, Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 4207},
			"db":  {Type: "state-path", Value: "/home/test/.lifecycle-app/wt-1-3/db.sqlite"},
		},
		Extras: map[string]any{},
	}
	if err := descriptor.Write(filepath.Join(worktree, "wt-env.yaml"), "yaml", d); err != nil {
		t.Fatalf("planting the descriptor: %v", err)
	}
	sock := shortSock(t, "rebuild")
	rc := newRecordingCoord(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"allocate": func(*protocol.Request) *protocol.Response {
			res := allocateOK(worktree)
			var a protocol.AllocateResult
			json.Unmarshal(res.Result, &a)
			a.Slot = 3
			a.Resources["api"] = spec.Resolved{Type: "port", Value: 4207}
			res.Result = mustJSONT(a)
			return res
		},
		"materialise": cannedT(&protocol.MaterialiseResult{App: "lifecycle-app", Slug: "wt-1", State: "reserving"}),
		"activate":    cannedT(&protocol.ActivateResult{App: "lifecycle-app", Slug: "wt-1", State: "active"}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "rebuilt") {
		t.Errorf("stderr = %q, want the rebuild stated", stderr)
	}
	// The allocate request must have carried the descriptor's slot.
	if len(rc.requests["allocate"]) != 1 {
		t.Fatalf("allocate requests = %d, want 1", len(rc.requests["allocate"]))
	}
	var args protocol.AllocateArgs
	if err := json.Unmarshal(rc.requests["allocate"][0], &args); err != nil {
		t.Fatalf("decoding the allocate request: %v", err)
	}
	if args.SlotHint != 3 {
		t.Errorf("slot_hint = %d, want the descriptor's 3", args.SlotHint)
	}
	// The descriptor was re-emitted from the rebuilt entry: slot 3.
	nd, err := descriptor.Read(filepath.Join(worktree, "wt-env.yaml"), "yaml")
	if err != nil {
		t.Fatalf("reading the re-emitted descriptor: %v", err)
	}
	if nd.Slot != 3 {
		t.Errorf("re-emitted slot = %d, want 3", nd.Slot)
	}
}

// TestInitDryRunChangesNothing: --dry-run prints the plan and touches
// neither the coordinator nor the tree.
func TestInitDryRunChangesNothing(t *testing.T) {
	sp := lifecycleSpec(t)
	_, worktree := lifecycleFixture(t, sp)
	code, stdout, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the test worktree", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "would allocate") {
		t.Errorf("stderr = %q, want the dry-run plan", stderr)
	}
	if !strings.Contains(stdout, "nothing was changed") {
		t.Errorf("stdout = %q, want the dry-run statement", stdout)
	}
	if _, err := os.Stat(filepath.Join(worktree, "wt-env.yaml")); err == nil {
		t.Error("--dry-run wrote the descriptor")
	}
}

// strPtr is the pointer helper for the test specs.
func strPtr(s string) *string { return &s }

// mustYAML emits the spec as YAML for the fixture write.
func mustYAML(t *testing.T, sp *spec.Spec) []byte {
	t.Helper()
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	return data
}
