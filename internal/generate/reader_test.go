package generate

// The generated reader is proved by building it: the source is written into
// a temp Go module, compiled with the real toolchain, and the resulting
// binary is run across every row of the resolution table (exit criterion 8)
// and the refusals (criterion 9). The module has no dependencies — the
// reader must parse the descriptor with the standard library alone.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// tempDir is t.TempDir() with symlinks resolved: on macOS the raw temp
// root sits under /var, a symlink to /private/var, and git reports the
// resolved path — the same trap the generated reader's own path
// comparisons must survive.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := platform.RealPath(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixtureSpec loads the compose-app fixture spec, the one that declares a
// reader.
func fixtureSpec(t *testing.T) *spec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "compose-app", "wt.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture spec: %v", err)
	}
	s, err := spec.Parse(data)
	if err != nil {
		t.Fatalf("parsing the fixture spec: %v", err)
	}
	if s.Emit.Reader == nil {
		t.Fatal("the compose-app fixture spec declares no emit.reader")
	}
	return s
}

// buildGeneratedReader writes the generated source into a fresh temp module
// and compiles it, returning the runnable binary's path.
func buildGeneratedReader(t *testing.T, s *spec.Spec) string {
	t.Helper()
	src, err := Reader(s)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	mod := tempDir(t)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(mod, s.Emit.Reader.Path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/wtgen\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, s.Emit.Reader.Path), src, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"example.com/wtgen/internal/env"
)

// runner exercises the generated reader: it resolves with the given --env
// value and explicit values and prints the Env as JSON, exiting 1 on
// refusal. The working directory is set by the caller.
func main() {
	flagEnv := ""
	explicit := map[string]string{}
	if len(os.Args) > 1 {
		flagEnv = os.Args[1]
	}
	if len(os.Args) > 2 {
		for _, kv := range strings.Split(os.Args[2], ",") {
			if kv == "" {
				continue
			}
			key, val, ok := strings.Cut(kv, "=")
			if !ok {
				fmt.Fprintf(os.Stderr, "bad explicit value %q\n", kv)
				os.Exit(2)
			}
			explicit[key] = val
		}
	}
	e, err := env.Resolve(flagEnv, explicit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.Marshal(e)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
`
	if err := os.MkdirAll(filepath.Join(mod, "runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "runner", "main.go"), []byte(runner), 0o644); err != nil {
		t.Fatal(err)
	}
	// The compile proof: the whole module builds — the generated package
	// with nothing but the standard library, at its declared path.
	build := exec.Command("go", "build", "./...")
	build.Dir = mod
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./... of the generated reader failed: %v\n%s", err, out)
	}
	bin := filepath.Join(mod, "runner-bin")
	buildBin := exec.Command("go", "build", "-o", bin, "./runner")
	buildBin.Dir = mod
	if out, err := buildBin.CombinedOutput(); err != nil {
		t.Fatalf("building the runner failed: %v\n%s", err, out)
	}
	return bin
}

// run resolves from dir with the given flagEnv, explicit values and extra
// environment, returning the parsed Env or the stderr on refusal.
func run(t *testing.T, bin, dir, flagEnv, explicit string, extraEnv ...string) (map[string]any, string) {
	t.Helper()
	cmd := exec.Command(bin, flagEnv, explicit)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(out)
	}
	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("the runner printed non-JSON: %v\n%s", err, out)
	}
	return env, ""
}

// adoptedRepo is a repository that has adopted the tooling: a committed
// wt.yaml (the compose-app spec) and a linked worktree.
type adoptedRepo struct {
	main string
	wt1  string
}

func buildAdoptedRepo(t *testing.T) *adoptedRepo {
	t.Helper()
	root := tempDir(t)
	ar := &adoptedRepo{main: filepath.Join(root, "repo"), wt1: filepath.Join(root, "wt1")}
	if err := os.MkdirAll(ar.main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, ar.main, "init", "-q", "-b", "main", ".")
	gitIn(t, ar.main, "config", "user.email", "fixture@localhost")
	gitIn(t, ar.main, "config", "user.name", "fixture")
	specData, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "compose-app", "wt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ar.main, "wt.yaml"), specData, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, ar.main, "add", "wt.yaml")
	gitIn(t, ar.main, "commit", "-q", "-m", "adopt")
	gitIn(t, ar.main, "worktree", "add", "-q", ar.wt1, "-b", "w1")
	return ar
}

// worktreeDescriptor writes a real descriptor into the worktree with this
// phase's own writer, so the test proves writer output parses through the
// generated reader.
func worktreeDescriptor(t *testing.T, root string) string {
	t.Helper()
	d := &descriptor.Descriptor{
		Version: 1,
		App:     "compose-app",
		Slug:    "w1",
		Slot:    3,
		Path:    root,
		Resources: map[string]spec.Resolved{
			"proxy":        {Type: "port", Value: 4202},
			"api":          {Type: "port", Value: 4203},
			"compose":      {Type: "namespace", Value: "compose-app-w1-3"},
			"compose_test": {Type: "namespace", Value: "compose-app-w1-3-test"},
			"db":           {Type: "state-path", Value: "/home/u/.compose-app/worktrees/w1-3/db.sqlite"},
		},
	}
	path := filepath.Join(root, "wt-env.yaml")
	if err := descriptor.Write(path, "yaml", d); err != nil {
		t.Fatalf("descriptor.Write: %v", err)
	}
	return path
}

// TestGeneratedReaderCompilesAndRuns is exit criterion 8: the generated
// reader is built in a temp directory with the real toolchain — a
// generator whose output was never built is a generator that does not work
// — and the binary exercises all four rows of the resolution table.
func TestGeneratedReaderCompilesAndRuns(t *testing.T) {
	s := fixtureSpec(t)
	bin := buildGeneratedReader(t, s)

	ar := buildAdoptedRepo(t)
	descPath := worktreeDescriptor(t, ar.wt1)

	// Row 1: not a repo, or no spec → the legacy default (A3).
	plainDir := filepath.Join(tempDir(t), "plain")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, refusal := run(t, bin, plainDir, "", "")
	if refusal != "" {
		t.Fatalf("row 1 (no repo, no spec): refused: %s", refusal)
	}
	if env["env_source"] != "legacy" {
		t.Errorf("row 1 (no repo, no spec): env_source = %v, want legacy", env["env_source"])
	}

	// Row 1 again: a git repository with no wt.yaml has not adopted the
	// tooling.
	plainRepo := filepath.Join(tempDir(t), "plain-repo")
	if err := os.MkdirAll(plainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, plainRepo, "init", "-q", "-b", "main", ".")
	gitIn(t, plainRepo, "config", "user.email", "fixture@localhost")
	gitIn(t, plainRepo, "config", "user.name", "fixture")
	if err := os.WriteFile(filepath.Join(plainRepo, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, plainRepo, "add", "f.txt")
	gitIn(t, plainRepo, "commit", "-q", "-m", "init")
	env, refusal = run(t, bin, plainRepo, "", "")
	if refusal != "" {
		t.Fatalf("row 1 (repo, no spec): refused: %s", refusal)
	}
	if env["env_source"] != "legacy" {
		t.Errorf("row 1 (repo, no spec): env_source = %v, want legacy", env["env_source"])
	}

	// Row 1 again: not a repo, but a wt.yaml sits in the directory — git
	// cannot classify it, so it is still the legacy default, never a
	// linked-worktree refusal.
	specDir := filepath.Join(tempDir(t), "spec-no-git")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	specData, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "compose-app", "wt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "wt.yaml"), specData, 0o644); err != nil {
		t.Fatal(err)
	}
	env, refusal = run(t, bin, specDir, "", "")
	if refusal != "" {
		t.Fatalf("row 1 (spec, no git): refused: %s", refusal)
	}
	if env["env_source"] != "legacy" {
		t.Errorf("row 1 (spec, no git): env_source = %v, want legacy", env["env_source"])
	}

	// Row 2: the primary checkout of an adopted repo → the legacy default;
	// slot 0 keeps the committed defaults.
	env, refusal = run(t, bin, ar.main, "", "")
	if refusal != "" {
		t.Fatalf("row 2 (primary checkout): refused: %s", refusal)
	}
	if env["env_source"] != "legacy" {
		t.Errorf("row 2 (primary checkout): env_source = %v, want legacy", env["env_source"])
	}

	// Row 3: a linked worktree with a descriptor → the descriptor, written
	// by this phase's own writer.
	env, refusal = run(t, bin, ar.wt1, "", "")
	if refusal != "" {
		t.Fatalf("row 3 (linked worktree with descriptor): refused: %s", refusal)
	}
	if env["env_source"] != "descriptor" {
		t.Errorf("row 3: env_source = %v, want descriptor", env["env_source"])
	}
	if env["env_path"] != descPath {
		t.Errorf("row 3: env_path = %v, want %v", env["env_path"], descPath)
	}
	res := env["resources"].(map[string]any)
	if res["api"] != "4203" || res["proxy"] != "4202" || res["compose"] != "compose-app-w1-3" {
		t.Errorf("row 3: resources = %v", res)
	}
	if env["slug"] != "w1" || env["slot"] != float64(3) || env["app"] != "compose-app" {
		t.Errorf("row 3: identity = %v %v %v", env["slug"], env["slot"], env["app"])
	}

	// Row 4: a linked worktree of an adopted repo with no descriptor →
	// refuse loudly, naming wt init — the row people argue with
	// (05-delivery.md §4.3).
	ar2 := buildAdoptedRepo(t)
	_, refusal = run(t, bin, ar2.wt1, "", "")
	if refusal == "" {
		t.Fatal("row 4 (linked worktree, no descriptor): resolved instead of refusing")
	}
	if !strings.Contains(refusal, "wt init") {
		t.Errorf("row 4: refusal does not name wt init: %s", refusal)
	}
	if !strings.Contains(refusal, "wt-env.yaml") {
		t.Errorf("row 4: refusal does not name the descriptor path: %s", refusal)
	}
}

// TestGeneratedReaderEnvOverride exercises precedence level 2: a
// per-invocation --env beats the app-scoped variable, a missing override
// is never a fall-through, and an override naming another app's descriptor
// is refused naming both apps.
func TestGeneratedReaderEnvOverride(t *testing.T) {
	s := fixtureSpec(t)
	bin := buildGeneratedReader(t, s)

	ar := buildAdoptedRepo(t)
	descPath := worktreeDescriptor(t, ar.wt1)

	// --env names the descriptor.
	env, refusal := run(t, bin, ar.main, descPath, "")
	if refusal != "" {
		t.Fatalf("--env: refused: %s", refusal)
	}
	if env["env_source"] != "env" || env["env_path"] != descPath {
		t.Errorf("--env: source/path = %v/%v, want env/%s", env["env_source"], env["env_path"], descPath)
	}
	if env["resources"].(map[string]any)["api"] != "4203" {
		t.Errorf("--env: resources = %v", env["resources"])
	}

	// The app-scoped variable names the descriptor.
	env, refusal = run(t, bin, ar.main, "", "", "COMPOSE_APP_ENV="+descPath)
	if refusal != "" {
		t.Fatalf("env var: refused: %s", refusal)
	}
	if env["env_source"] != "env" {
		t.Errorf("env var: env_source = %v, want env", env["env_source"])
	}

	// --env beats the variable.
	other := worktreeDescriptor(t, ar.wt1)
	env, refusal = run(t, bin, ar.main, other, "", "COMPOSE_APP_ENV="+descPath)
	if refusal != "" {
		t.Fatalf("--env vs var: refused: %s", refusal)
	}
	if env["env_path"] != other {
		t.Errorf("--env vs var: env_path = %v, want %v (the flag must win)", env["env_path"], other)
	}

	// A missing override is an error, never a fall-through.
	_, refusal = run(t, bin, ar.main, filepath.Join(ar.main, "nope.yaml"), "")
	if refusal == "" || !strings.Contains(refusal, "nope.yaml") {
		t.Errorf("missing override: refusal = %q, want it to name the path", refusal)
	}

	// An override naming another app's descriptor is refused, naming both
	// apps.
	foreign := filepath.Join(ar.wt1, "foreign.yaml")
	if err := os.WriteFile(foreign, []byte("version: 1\napp: \"other-app\"\nslug: \"x\"\nslot: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, refusal = run(t, bin, ar.main, foreign, "")
	if refusal == "" || !strings.Contains(refusal, "other-app") || !strings.Contains(refusal, "compose-app") {
		t.Errorf("foreign app: refusal = %q, want both apps named", refusal)
	}
}

// TestGeneratedReaderShortCircuitsExplicitFlags exercises B18.2: when the
// most specific inputs are fully supplied, the descriptor read is
// short-circuited entirely — a broken override cannot take down an
// explicit call, even in a worktree whose descriptor is missing.
func TestGeneratedReaderShortCircuitsExplicitFlags(t *testing.T) {
	s := fixtureSpec(t)
	bin := buildGeneratedReader(t, s)
	ar := buildAdoptedRepo(t)

	explicit := "proxy=9998,api=9999,compose=custom,compose_test=custom-test,db=/tmp/db.sqlite"
	env, refusal := run(t, bin, ar.wt1, "", explicit, "COMPOSE_APP_ENV="+filepath.Join(ar.main, "missing.yaml"))
	if refusal != "" {
		t.Fatalf("explicit flags: refused: %s", refusal)
	}
	if env["env_source"] != "flags" {
		t.Errorf("explicit flags: env_source = %v, want flags", env["env_source"])
	}
	res := env["resources"].(map[string]any)
	if res["api"] != "9999" {
		t.Errorf("explicit flags: api = %v, want 9999", res["api"])
	}

	// Partially supplied explicit values still resolve through the
	// descriptor, with the explicit values winning per key.
	descPath := worktreeDescriptor(t, ar.wt1)
	env, refusal = run(t, bin, ar.wt1, "", "api=7777", "COMPOSE_APP_ENV="+descPath)
	if refusal != "" {
		t.Fatalf("partial explicit: refused: %s", refusal)
	}
	res = env["resources"].(map[string]any)
	if res["api"] != "7777" || res["proxy"] != "4202" {
		t.Errorf("partial explicit: api/proxy = %v/%v, want 7777/4202", res["api"], res["proxy"])
	}
}

// TestGeneratedReaderRefusesNewerVersion is exit criterion 9: the reader
// refuses a descriptor whose schema version is newer than it understands,
// rather than reading the fields it recognises.
func TestGeneratedReaderRefusesNewerVersion(t *testing.T) {
	s := fixtureSpec(t)
	bin := buildGeneratedReader(t, s)
	ar := buildAdoptedRepo(t)

	newer := filepath.Join(ar.wt1, "wt-env.yaml")
	d := &descriptor.Descriptor{
		Version:   2,
		App:       "compose-app",
		Slug:      "w1",
		Slot:      3,
		Resources: map[string]spec.Resolved{"api": {Type: "port", Value: 4203}},
	}
	if err := descriptor.Write(newer, "yaml", d); err != nil {
		t.Fatal(err)
	}
	_, refusal := run(t, bin, ar.wt1, "", "")
	if refusal == "" {
		t.Fatal("a version-2 descriptor was read cleanly")
	}
	if !strings.Contains(refusal, "version 2") {
		t.Errorf("refusal does not name the version: %s", refusal)
	}

	// The same refusal on the env-override path.
	_, refusal = run(t, bin, ar.main, newer, "")
	if refusal == "" || !strings.Contains(refusal, "version 2") {
		t.Errorf("env-override path: refusal = %q, want the version named", refusal)
	}
}

// TestReaderGenerationRefusals: generation is a function of the spec; a
// spec without a reader, or with a non-go language, is refused rather than
// generating something wrong.
func TestReaderGenerationRefusals(t *testing.T) {
	s := fixtureSpec(t)
	s.Emit.Reader = nil
	if _, err := Reader(s); err == nil {
		t.Error("a spec without emit.reader generated a reader")
	}
	s = fixtureSpec(t)
	s.Emit.Reader.Language = "python"
	if _, err := Reader(s); err == nil || !strings.Contains(err.Error(), "go") {
		t.Errorf("a non-go language error = %v, want it to name go", err)
	}
}

// TestGeneratedReaderParsesTheEmittedDescriptor: the yaml subset parser
// round-trips exactly what the emitter writes, in both the map-heavy shape
// (extras, state) and the sequence shape (shared) — the two shapes the
// emitted output actually contains.
func TestGeneratedReaderParsesTheEmittedDescriptor(t *testing.T) {
	s := fixtureSpec(t)
	bin := buildGeneratedReader(t, s)
	ar := buildAdoptedRepo(t)

	d := &descriptor.Descriptor{
		Version:     1,
		App:         "compose-app",
		Slug:        "no",
		Slot:        1,
		Path:        ar.wt1,
		Standalone:  false,
		Description: "fix dispatch lease race",
		Resources: map[string]spec.Resolved{
			"proxy":        {Type: "port", Value: 4202},
			"api":          {Type: "port", Value: 4203},
			"compose":      {Type: "namespace", Value: "compose-app-no-1"},
			"compose_test": {Type: "namespace", Value: "compose-app-no-1-test"},
			"db":           {Type: "state-path", Value: "/Users/geoff/.compose-app/worktrees/no-1/db.sqlite"},
		},
		State: map[string]*descriptor.Isolation{"db": {}},
		Shared: []descriptor.Shared{{
			Name:   "/Users/geoff/.compose-app/db.sqlite",
			Impact: "writes are visible to every worktree and the main checkout",
		}},
		Extras: map[string]any{"harness.notes": "seeded from prod snapshot", "count": 3},
	}
	if err := descriptor.Write(filepath.Join(ar.wt1, "wt-env.yaml"), "yaml", d); err != nil {
		t.Fatal(err)
	}
	env, refusal := run(t, bin, ar.wt1, "", "")
	if refusal != "" {
		t.Fatalf("the emitted descriptor did not parse: %s", refusal)
	}
	if env["env_source"] != "descriptor" || env["slug"] != "no" {
		t.Errorf("source/slug = %v/%v, want descriptor/no", env["env_source"], env["slug"])
	}
	if env["resources"].(map[string]any)["api"] != "4203" {
		t.Errorf("resources = %v", env["resources"])
	}
}

// TestGeneratedReaderParsesJSONFormat: a json-format descriptor (plain-app
// declares one) reads through the same reader. The spec's format is baked
// at generation, so the reader is generated with json here.
func TestGeneratedReaderParsesJSONFormat(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "plain-app", "wt.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := spec.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// plain-app declares no reader; give it one so the generation path is
	// the same one the fixture wiring uses.
	s.Emit.Reader = &spec.Reader{Language: "go", Path: "internal/env/env.go", Package: "env", EnvVar: "PLAIN_APP_ENV"}
	if s.Emit.Descriptor.Format != "json" {
		t.Fatalf("plain-app format = %q, want json", s.Emit.Descriptor.Format)
	}
	bin := buildGeneratedReader(t, s)

	ar := buildAdoptedRepo(t)
	// The plain-app spec has no port bases in the test context, so give
	// the worktree a descriptor written by the real writer.
	d := &descriptor.Descriptor{
		Version: 1,
		App:     "plain-app",
		Slug:    "w1",
		Slot:    2,
		Path:    ar.wt1,
		Resources: map[string]spec.Resolved{
			"api":       {Type: "port", Value: 4201},
			"db":        {Type: "state-path", Value: "/home/u/db.sqlite"},
			"cache":     {Type: "state-path", Value: "/home/u/cache/"},
			"shared_db": {Type: "state-path", Value: "/home/u/shared/db.sqlite"},
		},
	}
	if err := descriptor.Write(filepath.Join(ar.wt1, "wt-env.json"), "json", d); err != nil {
		t.Fatal(err)
	}
	env, refusal := run(t, bin, ar.wt1, "", "")
	if refusal != "" {
		t.Fatalf("the json descriptor did not parse: %s", refusal)
	}
	if env["env_source"] != "descriptor" {
		t.Errorf("env_source = %v, want descriptor", env["env_source"])
	}
	if env["resources"].(map[string]any)["api"] != "4201" {
		t.Errorf("resources = %v", env["resources"])
	}
}

// TestGeneratedReaderRefusesUnknownFields: a descriptor carrying a field
// this schema does not know (the deleted view field) is refused whole,
// never read selectively.
func TestGeneratedReaderRefusesUnknownFields(t *testing.T) {
	s := fixtureSpec(t)
	bin := buildGeneratedReader(t, s)
	ar := buildAdoptedRepo(t)

	path := filepath.Join(ar.wt1, "wt-env.yaml")
	if err := os.WriteFile(path, []byte("version: 1\napp: \"compose-app\"\nslug: \"w1\"\nslot: 1\nview: \"2f9c...\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, refusal := run(t, bin, ar.wt1, "", "")
	if refusal == "" || !strings.Contains(refusal, "view") {
		t.Errorf("refusal = %q, want the unknown field named", refusal)
	}
}

// TestGeneratedReaderFormatIsBaked: the generated source carries the
// spec's declared format and app-scoped env var — the constants the
// fixture wiring and the status output rely on.
func TestGeneratedReaderFormatIsBaked(t *testing.T) {
	s := fixtureSpec(t)
	src, err := Reader(s)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{
		`App = "compose-app"`,
		`EnvVar = "COMPOSE_APP_ENV"`,
		`DescriptorFilename = "wt-env.yaml"`,
		`DescriptorFormat   = "yaml"`,
		`package env`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated source lacks %q", want)
		}
	}
	if !strings.Contains(text, fmt.Sprintf("%q", "compose_test")) {
		t.Error("the generated source does not bake the resource names (the short-circuit needs them)")
	}
}
