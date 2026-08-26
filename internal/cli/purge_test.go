//go:build !acceptance

package cli

// purge_test.go covers `wt rm`'s purge surface: the two spellings that
// select a state store for deletion, the refusal of a value that selects
// nothing, and the report that says which stores a teardown deleted.
//
// The bug these tests pin: `--purge` used to take the literal flag string
// from the spec and match it by string equality, so `--purge db` — the
// resource's own name — parsed, matched no resource, and exited 0 having
// deleted nothing. The spec's declared purge.flag was meanwhile never
// registered as a flag at all, so the one spelling a reader of wt.yaml
// would try was rejected as undefined.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// purgeSpecRepo writes a one-file repository whose wt.yaml is the
// lifecycle spec with the given mutation applied. It is enough for every
// refusal that lands before rm dials the coordinator.
func purgeSpecRepo(t *testing.T, mutate func(*spec.Spec)) string {
	t.Helper()
	sp := lifecycleSpec(t)
	mutate(sp)
	dir := t.TempDir()
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	writeT(t, filepath.Join(dir, "wt.yaml"), string(data))
	return dir
}

// withPurgeDB gives the lifecycle spec's db resource a purge block.
func withPurgeDB(sp *spec.Spec) {
	for i := range sp.Resources {
		if sp.Resources[i].Name == "db" {
			sp.Resources[i].Purge = &spec.Purge{Flag: "--purge-db"}
		}
	}
}

// TestRmPurgeValueMatchingNothingIsRefused: a --purge value that selects
// no resource stops rm with a usage error naming what can be purged. It
// used to parse, select nothing and exit 0 having deleted nothing.
func TestRmPurgeValueMatchingNothingIsRefused(t *testing.T) {
	dir := purgeSpecRepo(t, withPurgeDB)
	code, _, stderr := runCLI(t, "rm", "--cwd", dir, "--slug", "gone", "--purge", "builds")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr:\n%s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "names nothing this repository can purge") {
		t.Errorf("the refusal does not say the value selected nothing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--purge-db") || !strings.Contains(stderr, "db") {
		t.Errorf("the refusal does not name what can be purged:\n%s", stderr)
	}
}

// TestRmPurgeRefusalWhenNothingIsPurgeable: a repository whose state paths
// declare no purge block says so, rather than naming an empty list.
func TestRmPurgeRefusalWhenNothingIsPurgeable(t *testing.T) {
	dir := purgeSpecRepo(t, func(*spec.Spec) {})
	code, _, stderr := runCLI(t, "rm", "--cwd", dir, "--slug", "gone", "--purge", "db")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d (usage); stderr:\n%s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "declares a purge") {
		t.Errorf("the refusal does not say the repository declares no purge block:\n%s", stderr)
	}
}

// TestRmRegistersTheSpecsPurgeFlag: the flag the spec declares is a flag
// rm accepts. It used to be rejected as undefined, which is why the spec
// and the binary disagreed about the tool's own surface.
func TestRmRegistersTheSpecsPurgeFlag(t *testing.T) {
	dir := purgeSpecRepo(t, withPurgeDB)
	_, _, stderr := runCLI(t, "rm", "--cwd", dir, "--slug", "gone", "--purge-db")
	if strings.Contains(stderr, "flag provided but not defined") {
		t.Errorf("the spec declares purge.flag --purge-db and rm rejects it:\n%s", stderr)
	}
}

// TestRmRefusesASpecFlagCollidingWithItsOwn: a spec may not redefine one
// of rm's own flags. The flag package panics on a redefinition, so this is
// a refusal rather than a crash.
func TestRmRefusesASpecFlagCollidingWithItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  string
		mutate func(*spec.Spec)
	}{
		{"purge.flag", "purge.flag", func(sp *spec.Spec) {
			for i := range sp.Resources {
				if sp.Resources[i].Name == "db" {
					sp.Resources[i].Purge = &spec.Purge{Flag: "--force"}
				}
			}
		}},
		{"keep_flag", "keep_flag", func(sp *spec.Spec) {
			sp.Resources = append(sp.Resources, spec.Resource{
				Type: "machine", Name: "vm", Template: strPtr("{app}-{slug}"),
				KeepFlag: strPtr("--json")})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := purgeSpecRepo(t, tc.mutate)
			code, _, stderr := runCLI(t, "rm", "--cwd", dir, "--slug", "gone")
			if code != ExitUsage {
				t.Fatalf("exit = %d, want %d (usage); stderr:\n%s", code, ExitUsage, stderr)
			}
			if !strings.Contains(stderr, tc.field) {
				t.Errorf("the refusal does not name the spec field %s:\n%s", tc.field, stderr)
			}
		})
	}
}

// TestRmPurgeEndToEnd drives the real coordinator and the real state-path
// driver over three worktrees of the adopted fixture: no purge flag leaves
// the store, the resource name deletes it, and the spec's declared flag
// deletes it too. The three run in one environment because the setup —
// coordinator, repository copy, shared sources — is the slow part.
func TestRmPurgeEndToEnd(t *testing.T) {
	env := newAdoptionEnv(t, true)
	installFakeGh(t)
	chdir(t, env.main)
	if code, _, stderr := runCLI(t, "bands", "reserve", "--base", fmt.Sprintf("api=%d", adoptionBand)); code != ExitOK {
		t.Fatalf("bands reserve exit = %d; stderr: %s", code, stderr)
	}

	for _, tc := range []struct {
		slug      string
		args      []string
		wantGone  bool
		wantNamed bool // the report names db as purged
	}{
		{slug: "pg-keep", args: nil, wantGone: false},
		{slug: "pg-name", args: []string{"--purge", "db"}, wantGone: true, wantNamed: true},
		{slug: "pg-flag", args: []string{"--purge-db"}, wantGone: true, wantNamed: true},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			wt := env.worktree(tc.slug)
			env.initWorktree(wt, tc.slug)
			db := fmt.Sprint(env.readDescriptor(wt).Resources["db"].Value)
			if _, err := os.Stat(db); err != nil {
				t.Fatalf("the db store was not created: %v", err)
			}

			args := append([]string{"rm", "--cwd", env.main, "--slug", tc.slug, "--json"}, tc.args...)
			code, stdout, stderr := runCLI(t, args...)
			if code != ExitOK {
				t.Fatalf("rm exit = %d; stderr:\n%s", code, stderr)
			}
			var res rmResult
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
				t.Fatalf("rm stdout is not one JSON object: %v\n%s", err, stdout)
			}

			_, serr := os.Stat(db)
			if tc.wantGone && serr == nil {
				t.Errorf("rm %v exited 0 but the db store %s survives", tc.args, db)
			}
			if !tc.wantGone && serr != nil {
				t.Errorf("rm with no purge flag deleted the db store %s: %v", db, serr)
			}
			named := len(res.Purged) == 1 && res.Purged[0] == "db"
			if named != tc.wantNamed {
				t.Errorf("purged = %v, want named=%v", res.Purged, tc.wantNamed)
			}
		})
	}
}

// TestRmDryRunSeparatesTeardownFromPurge: the preview says which state
// stores would be deleted, so "would tear down the resources" is no longer
// the only line a reader can take for file deletion.
func TestRmDryRunSeparatesTeardownFromPurge(t *testing.T) {
	env := newAdoptionEnv(t, true)
	installFakeGh(t)
	chdir(t, env.main)
	if code, _, stderr := runCLI(t, "bands", "reserve", "--base", fmt.Sprintf("api=%d", adoptionBand)); code != ExitOK {
		t.Fatalf("bands reserve exit = %d; stderr: %s", code, stderr)
	}
	wt := env.worktree("pg-dry")
	env.initWorktree(wt, "pg-dry")

	_, _, stderr := runCLI(t, "rm", "--cwd", env.main, "--slug", "pg-dry", "--dry-run")
	if !strings.Contains(stderr, "would purge the state stores: none") {
		t.Errorf("a dry run without --purge must say no store would be deleted:\n%s", stderr)
	}
	_, _, stderr = runCLI(t, "rm", "--cwd", env.main, "--slug", "pg-dry", "--dry-run", "--purge", "db")
	if !strings.Contains(stderr, "would purge the state stores: db") {
		t.Errorf("a dry run with --purge db must name db:\n%s", stderr)
	}
	db := fmt.Sprint(env.readDescriptor(wt).Resources["db"].Value)
	if _, err := os.Stat(db); err != nil {
		t.Errorf("a dry run deleted the db store %s: %v", db, err)
	}
}
