package driver

// statepath_test.go exercises the state-path driver: apply per mode, the
// purge refusal on the resolved symlink-realised path, teardown only when
// the purge flag is given, and verify. All paths are built from t.TempDir()
// roots; where an expectation is a path the driver prints, it comes from
// realisedName, because t.TempDir's spelling is not the one the driver
// realises — macOS answers /var where the realised form is /private/var,
// and Windows answers an 8.3 short name where the realised form is long.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// realisedName is the spelling the driver's messages give a path: realised,
// then converted back to the form a person reads. Asserting on the caller's
// own spelling instead is what made these tests pass on macOS by accident —
// "/private/var/x" contains "/var/x" — and fail on Windows, where the short
// and long names share no such substring.
func realisedName(t *testing.T, path string) string {
	t.Helper()
	real, err := platform.RealPath(path)
	if err != nil {
		t.Fatalf("realising %s: %v", path, err)
	}
	return platform.ExternalPath(real)
}

// statePathEnv builds the Env a state-path operation needs: a spec carrying
// the given resource, a temp home, and the given seed-mode and purge-flag
// choices.
func statePathEnv(t *testing.T, home string, res *spec.Resource, modes map[string]string, purgeFlags ...string) (Env, *spec.Spec) {
	t.Helper()
	max := 32
	s := &spec.Spec{
		Version: 1, App: "plain-app",
		Slots:     spec.Slots{Max: &max},
		Resources: []spec.Resource{*res},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("test spec does not validate: %v", err)
	}
	ctx := spec.Context{
		App: "plain-app", Slug: "wt-1", Slot: 1,
		Home: home, Worktree: filepath.Join(home, "worktrees", "wt-1"),
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the test spec: %v", err)
	}
	env := Env{
		Spec: s, App: "plain-app", Slug: "wt-1", Slot: 1,
		Home: home, Worktree: ctx.Worktree,
		Resolved:   table,
		SeedModes:  modes,
		PurgeFlags: purgeFlags,
	}
	return env, s
}

// statePathValue resolves the resource's path for the test env.
func statePathValue(t *testing.T, s *spec.Spec, env Env, name string) string {
	t.Helper()
	ctx := spec.Context{
		App: "plain-app", Slug: "wt-1", Slot: 1,
		Home: env.Home, Worktree: env.Worktree,
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	v := table[name].Value
	p, ok := v.(string)
	if !ok {
		t.Fatalf("resolved value %#v is not a string", v)
	}
	return filepath.Clean(p)
}

func TestStatePathApplyNoSeedCreatesParent(t *testing.T) {
	home := t.TempDir()
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "db.sqlite"))}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "db")

	ar, err := (&StatePath{}).Apply(res, path, env)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		t.Fatalf("the parent %s was not created: %v", filepath.Dir(path), err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file target itself must not be created: %v", err)
	}
	if len(ar.Notes) == 0 {
		t.Error("Apply returned no notes; a created directory must be stated")
	}
}

func TestStatePathApplyEmpty(t *testing.T) {
	home := t.TempDir()
	res := &spec.Resource{Type: "state-path", Name: "cache",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "cache") + "/"),
		Seed:     &spec.Seed{From: filepath.Join(home, "cache") + "/", Modes: []string{"empty"}, Default: strPtr("empty")},
	}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "cache")

	if _, err := (&StatePath{}).Apply(res, path, env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("the empty directory %s was not created: %v", path, err)
	}
}

// TestStatePathApplySeededSnapshot pins the B6.2 fact: a seeded store is a
// snapshot taken at creation — the copy carries the source's bytes, the
// marker records when, and later divergence is the point, not a bug.
func TestStatePathApplySeededSnapshot(t *testing.T) {
	home := t.TempDir()
	from := filepath.Join(home, "db.sqlite")
	if err := os.WriteFile(from, []byte("the live store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "db.sqlite")),
		Seed:     &spec.Seed{From: "{home}/db.sqlite", Modes: []string{"seeded", "empty", "shared"}, Default: strPtr("seeded")},
	}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "db")

	ar, err := (&StatePath{}).Apply(res, path, env)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "the live store\n" {
		t.Fatalf("the snapshot does not carry the source's bytes: %q, %v", data, err)
	}
	findings, err := (&StatePath{}).Verify(res, path, env)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var seededAt string
	for _, f := range findings {
		if strings.Contains(f.Message, "seeded from") {
			seededAt = f.Message
		}
	}
	if seededAt == "" {
		t.Fatalf("Verify findings %+v do not report when the store was seeded", findings)
	}
	if !strings.Contains(seededAt, "snapshot") {
		t.Errorf("the seeded-at finding must carry the snapshot-not-mirror fact: %q", seededAt)
	}
	if !strings.Contains(strings.Join(ar.Notes, " "), "snapshot") {
		t.Errorf("the apply notes must state the snapshot fact: %v", ar.Notes)
	}
}

func TestStatePathApplySeededAbsentSource(t *testing.T) {
	home := t.TempDir()
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "db.sqlite")),
		Seed:     &spec.Seed{From: "{home}/db.sqlite", Modes: []string{"seeded"}, Default: strPtr("seeded")},
	}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "db")

	ar, err := (&StatePath{}).Apply(res, path, env)
	if err != nil {
		t.Fatalf("Apply with an absent source must succeed (first run): %v", err)
	}
	joined := strings.Join(ar.Notes, " ")
	if !strings.Contains(joined, "does not exist") {
		t.Errorf("an absent source must be stated, not silent: %v", ar.Notes)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the target's directory must still be created: %v", err)
	}
}

func TestStatePathApplySharedIsNoOp(t *testing.T) {
	home := t.TempDir()
	res := &spec.Resource{Type: "state-path", Name: "shared_db",
		Template: strPtr(filepath.Join(home, "shared", "db.sqlite")),
		Default:  strPtr("shared"),
		Seed:     &spec.Seed{From: "{home}/shared/db.sqlite", Modes: []string{"shared"}, Default: strPtr("shared")},
	}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "shared_db")

	ar, err := (&StatePath{}).Apply(res, path, env)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("shared mode must create nothing: %v", err)
	}
	if !strings.Contains(strings.Join(ar.Notes, " "), "no isolation") {
		t.Errorf("shared mode must state the no-isolation fact: %v", ar.Notes)
	}
}

// TestStatePathTeardownOnlyWhenPurgeGiven: teardown without the purge flag
// leaves the path alone; with it, the path and the seed marker go.
func TestStatePathTeardownOnlyWhenPurgeGiven(t *testing.T) {
	home := t.TempDir()
	from := filepath.Join(home, "db.sqlite")
	os.WriteFile(from, []byte("live"), 0o644)
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "db.sqlite")),
		Seed:     &spec.Seed{From: "{home}/db.sqlite", Modes: []string{"seeded"}, Default: strPtr("seeded")},
		Purge:    &spec.Purge{Flag: "--purge-db"},
	}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "db")
	if _, err := (&StatePath{}).Apply(res, path, env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := (&StatePath{}).Teardown(res, path, env); err != nil {
		t.Fatalf("Teardown without the purge flag: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the path must survive a teardown without the purge flag: %v", err)
	}

	purgeEnv := env
	purgeEnv.PurgeFlags = []string{"--purge-db"}
	if err := (&StatePath{}).Teardown(res, path, purgeEnv); err != nil {
		t.Fatalf("Teardown with the purge flag: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the path must be gone after a purge: %v", err)
	}
	if _, err := os.Stat(markerPath(path)); !os.IsNotExist(err) {
		t.Errorf("the seed marker must go with the purge: %v", err)
	}
}

// TestStatePathTeardownPurgesOnTeardownWhenTheSpecSaysAlways: a resource
// declaring purge.on_teardown: always has its store deleted by a teardown
// that passes nothing, and kept by one that passes the resource's
// keep_flag — the machine's --keep-vm shape, applied to a store.
func TestStatePathTeardownPurgesOnTeardownWhenTheSpecSaysAlways(t *testing.T) {
	home := t.TempDir()
	res := &spec.Resource{Type: "state-path", Name: "userdata",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "userdata") + "/"),
		Purge:    &spec.Purge{OnTeardown: spec.PurgeOnTeardownAlways, KeepFlag: "--keep-userdata"},
	}

	for _, tc := range []struct {
		name      string
		keepFlags []string
		wantGone  bool
	}{
		{"no flags", nil, true},
		{"the keep flag", []string{"--keep-userdata"}, false},
		{"another resource's keep flag", []string{"--keep-vm"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, s := statePathEnv(t, home, res, nil)
			env.KeepFlags = tc.keepFlags
			path := statePathValue(t, s, env, "userdata")
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatalf("creating the store: %v", err)
			}
			if err := (&StatePath{}).Teardown(res, path, env); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			_, err := os.Stat(path)
			if tc.wantGone && !os.IsNotExist(err) {
				t.Errorf("the store must be gone after the teardown: %v", err)
			}
			if !tc.wantGone && err != nil {
				t.Errorf("the keep flag must leave the store in place: %v", err)
			}
		})
	}
}

// TestStatePathDefaultPurgeSkipsASharedMode: a run whose seed mode is
// shared holds the shared store rather than a copy of it, and apply
// created nothing. A purge nobody asked for leaves it alone.
func TestStatePathDefaultPurgeSkipsASharedMode(t *testing.T) {
	home := t.TempDir()
	res := &spec.Resource{Type: "state-path", Name: "userdata",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "userdata") + "/"),
		Seed:     &spec.Seed{From: "{home}/shared/", Modes: []string{"seeded", "shared"}, Default: strPtr("seeded")},
		Purge:    &spec.Purge{OnTeardown: spec.PurgeOnTeardownAlways, KeepFlag: "--keep-userdata"},
	}
	env, s := statePathEnv(t, home, res, map[string]string{"userdata": "shared"})
	path := statePathValue(t, s, env, "userdata")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	if err := (&StatePath{}).Teardown(res, path, env); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mode shared must survive a purge nobody asked for: %v", err)
	}
}

// TestStatePathPurgeRefusalNamesThePath is exit criterion 7, the direct
// case: a purge whose resolved path is the shared source itself is refused
// and the refusal names the resolved path.
func TestStatePathPurgeRefusalNamesThePath(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "shared", "db.sqlite")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	os.WriteFile(shared, []byte("every project's data"), 0o644)
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(shared),
		Default:  strPtr("shared"),
		Seed:     &spec.Seed{From: "{home}/shared/db.sqlite", Modes: []string{"shared"}, Default: strPtr("shared")},
		Purge:    &spec.Purge{Flag: "--purge-db"},
	}
	env, s := statePathEnv(t, home, res, nil, "--purge-db")
	path := statePathValue(t, s, env, "db")
	if path != shared {
		t.Fatalf("test setup: resolved path %s != shared source %s", path, shared)
	}

	err := (&StatePath{}).Teardown(res, path, env)
	if !isRefusal(err) {
		t.Fatalf("purging the shared source itself must be refused, got %v", err)
	}
	if want := realisedName(t, shared); !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal must name the resolved path %s: %v", want, err)
	}
	if _, serr := os.Stat(shared); serr != nil {
		t.Fatalf("the shared source must survive the refusal: %v", serr)
	}
}

// TestStatePathPurgeRefusalAncestor: a purge whose resolved path is an
// ancestor of the shared source is refused too — deleting the worktrees
// directory would wipe the store nested under it.
func TestStatePathPurgeRefusalAncestor(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "worktrees", "db.sqlite")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	os.WriteFile(shared, []byte("data"), 0o644)
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(filepath.Join(home, "worktrees")), // an ancestor of the source
		Seed:     &spec.Seed{From: "{home}/worktrees/db.sqlite", Modes: []string{"seeded"}, Default: strPtr("seeded")},
		Purge:    &spec.Purge{Flag: "--purge-db"},
	}
	env, s := statePathEnv(t, home, res, nil, "--purge-db")
	path := statePathValue(t, s, env, "db")

	err := (&StatePath{}).Teardown(res, path, env)
	if !isRefusal(err) {
		t.Fatalf("purging an ancestor of the shared source must be refused, got %v", err)
	}
	if _, serr := os.Stat(shared); serr != nil {
		t.Fatalf("the shared source must survive: %v", serr)
	}
}

// TestStatePathPurgeRefusalThroughSymlink is exit criterion 7's second
// reach: the same refusal through a symlink, naming the resolved path — the
// check runs on the symlink-realised path via identity.Contains, not on the
// template (M1 §3.1).
func TestStatePathPurgeRefusalThroughSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "real-store")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "db.sqlite"), []byte("data"), 0o644)
	link := filepath.Join(home, "link-store")
	symlinkOrSkip(t, real, link)
	// The purge target is the symlink; the shared source sits inside the
	// real directory the symlink names.
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(link),
		Seed:     &spec.Seed{From: "{home}/real-store/db.sqlite", Modes: []string{"seeded"}, Default: strPtr("seeded")},
		Purge:    &spec.Purge{Flag: "--purge-db"},
	}
	env, s := statePathEnv(t, home, res, nil, "--purge-db")
	path := statePathValue(t, s, env, "db")
	if path != link {
		t.Fatalf("test setup: resolved path %s != symlink %s", path, link)
	}

	err := (&StatePath{}).Teardown(res, path, env)
	if !isRefusal(err) {
		t.Fatalf("a purge reached through a symlink must be refused, got %v", err)
	}
	if want := realisedName(t, real); !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal must name the resolved (symlink-realised) path %s: %v", want, err)
	}
	if _, serr := os.Stat(filepath.Join(real, "db.sqlite")); serr != nil {
		t.Fatalf("the shared source must survive: %v", serr)
	}
}

// TestStatePathVerifyReportsState: verify reports missing, writable and
// seeded-at, and changes nothing (the writability probe leaves no file
// behind).
func TestStatePathVerifyReportsState(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "db.sqlite"), []byte("live"), 0o644)
	res := &spec.Resource{Type: "state-path", Name: "db",
		Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "db.sqlite")),
		Seed:     &spec.Seed{From: "{home}/db.sqlite", Modes: []string{"seeded"}, Default: strPtr("seeded")},
	}
	env, s := statePathEnv(t, home, res, nil)
	path := statePathValue(t, s, env, "db")

	findings, err := (&StatePath{}).Verify(res, path, env)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(findings) != 1 || findings[0].Level != LevelError || !strings.Contains(findings[0].Message, "does not exist") {
		t.Fatalf("verify of a missing path = %+v, want one error naming the missing path", findings)
	}

	if _, err := (&StatePath{}).Apply(res, path, env); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	findings, err = (&StatePath{}).Verify(res, path, env)
	if err != nil {
		t.Fatalf("Verify after apply: %v", err)
	}
	joined := strings.Join(messages(findings), " ")
	if !strings.Contains(joined, "seeded from") || !strings.Contains(joined, "snapshot") {
		t.Errorf("verify after a seeded apply must report when it was seeded: %+v", findings)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".wt-write-probe")); !os.IsNotExist(err) {
		t.Errorf("verify must leave no probe file behind: %v", err)
	}
}

func messages(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Message
	}
	return out
}

// TestStatePathBlastRadiusNamesSharing: the shared-block prose says what
// sharing a state path costs.
func TestStatePathBlastRadiusNamesSharing(t *testing.T) {
	br := (&StatePath{}).BlastRadius(&spec.Resource{Name: "db"}, nil)
	if !strings.Contains(br, "every worktree") || !strings.Contains(br, "writes") {
		t.Errorf("BlastRadius = %q, want prose naming shared writes", br)
	}
}

// symlinkOrSkip creates a symlink, skipping the test when the platform
// will not let it.
//
// Creating a symlink on Windows needs SeCreateSymbolicLinkPrivilege, which
// an ordinary account holds only with Developer Mode on; without it
// os.Symlink fails with "A required privilege is not held by the client".
// A test about symlink semantics cannot run there, and a skip naming the
// reason is the honest answer — the same call the mapped-drive probe in
// realpath_windows_test.go makes. Every other platform, and Windows CI,
// creates the link and runs the test.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create a symlink on this machine (%v); creating one needs SeCreateSymbolicLinkPrivilege, which Developer Mode grants", err)
		}
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}
