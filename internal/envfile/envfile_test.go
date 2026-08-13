package envfile

// The .env managed block, tested at the exit-criterion level: the stripped
// duplicate (criterion 4), the first-write seed from the main checkout
// (criterion 5) and the unbalanced-or-nested marker refusal (criterion 6),
// plus the replace-only-the-block re-run that hand edits above and below
// survive.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/managed"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"runtime"
)

// plainEnv is the emit.env shape of the plain-app fixture: managed keys
// templated over the resolved resources.
func plainEnv() *spec.EnvEmit {
	return &spec.EnvEmit{
		Path: ".env",
		Seed: true,
		Keys: map[string]string{
			"API_PORT": "{api}",
			"DB_PATH":  "{db}",
		},
	}
}

func plainCtx() (spec.Context, map[string]spec.Resolved) {
	ctx := spec.Context{App: "plain-app", Slug: "brisk-otter", Slot: 3, Home: "/Users/geoff", Worktree: "/Users/geoff/wt/brisk-otter"}
	resolved := map[string]spec.Resolved{
		"api": {Type: "port", Value: 4203},
		"db":  {Type: "state-path", Value: "/Users/geoff/.plain-app/worktrees/brisk-otter-3/db.sqlite"},
	}
	return ctx, resolved
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestManagedKeyAboveBlockIsStripped is exit criterion 4: a managed key
// defined above the block comes back with one definition, and the report
// says which key was stripped. Duplicates are stripped, never ordered —
// two definitions is a bug in both directions (05-delivery.md §3.2).
func TestManagedKeyAboveBlockIsStripped(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	writeFile(t, path, `GITHUB_TOKEN=abc
API_PORT=9999
# --- managed by wt; edits below are overwritten ---
API_PORT=4203
DB_PATH=/Users/geoff/.plain-app/worktrees/brisk-otter-3/db.sqlite
# --- end ---
DB_PATH=/stale
FOO=bar
`)
	ctx, resolved := plainCtx()
	rep, err := Update(path, plainEnv(), ctx, resolved, "")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(rep.Stripped) != 2 || rep.Stripped[0] != "API_PORT" || rep.Stripped[1] != "DB_PATH" {
		t.Errorf("stripped = %v, want [API_PORT DB_PATH]", rep.Stripped)
	}
	got := readFile(t, path)
	if strings.Count(got, "API_PORT=") != 1 {
		t.Errorf("API_PORT is defined %d times:\n%s", strings.Count(got, "API_PORT="), got)
	}
	if strings.Count(got, "DB_PATH=") != 1 {
		t.Errorf("DB_PATH is defined %d times:\n%s", strings.Count(got, "DB_PATH="), got)
	}
	// The unmanaged content above and below survives.
	if !strings.Contains(got, "GITHUB_TOKEN=abc") || !strings.Contains(got, "FOO=bar") {
		t.Errorf("unmanaged content did not survive:\n%s", got)
	}
	// The values come from this worktree's allocation.
	if !strings.Contains(got, "API_PORT=4203") {
		t.Errorf("the block lacks this worktree's API_PORT:\n%s", got)
	}
}

// TestReRunReplacesOnlyTheBlock: a re-run swaps the marked lines and
// nothing else — hand edits above and below survive two writes.
func TestReRunReplacesOnlyTheBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	writeFile(t, path, "# top comment\nGITHUB_TOKEN=abc\n")
	ctx, resolved := plainCtx()
	if _, err := Update(path, plainEnv(), ctx, resolved, ""); err != nil {
		t.Fatal(err)
	}
	ctx.Slot = 4
	resolved["api"] = spec.Resolved{Type: "port", Value: 4204}
	if _, err := Update(path, plainEnv(), ctx, resolved, ""); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "# top comment") || !strings.Contains(got, "GITHUB_TOKEN=abc") {
		t.Errorf("hand edits above the block did not survive the re-run:\n%s", got)
	}
	if !strings.Contains(got, "API_PORT=4204") {
		t.Errorf("the re-run did not replace the block with the new allocation:\n%s", got)
	}
	if strings.Contains(got, "API_PORT=4203") {
		t.Errorf("the old block survived the re-run:\n%s", got)
	}
	if strings.Count(got, StartMarker) != 1 || strings.Count(got, EndMarker) != 1 {
		t.Errorf("exactly one block expected:\n%s", got)
	}
}

// TestFirstWriteSeedsFromMainCheckout is exit criterion 5's first half: a
// first write seeds the new worktree's .env from the main checkout's,
// copying unmanaged content only — managed keys come from this worktree's
// allocation, never from slot 0.
func TestFirstWriteSeedsFromMainCheckout(t *testing.T) {
	dir := t.TempDir()
	mainEnv := filepath.Join(dir, "main", ".env")
	wtEnv := filepath.Join(dir, "wt", ".env")
	if err := os.MkdirAll(filepath.Dir(mainEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(wtEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	// The main checkout is slot 0: its API_PORT and DB_PATH are slot 0's
	// and must never carry over.
	writeFile(t, mainEnv, `GITHUB_TOKEN=secret-token
API_PORT=8080
DB_PATH=/slot-zero/db.sqlite
# a comment survives
`)
	ctx, resolved := plainCtx()
	rep, err := Update(wtEnv, plainEnv(), ctx, resolved, mainEnv)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rep.SeededFrom != mainEnv {
		t.Errorf("seeded_from = %q, want %q", rep.SeededFrom, mainEnv)
	}
	got := readFile(t, wtEnv)
	if !strings.Contains(got, "GITHUB_TOKEN=secret-token") || !strings.Contains(got, "# a comment survives") {
		t.Errorf("unmanaged content did not seed:\n%s", got)
	}
	if strings.Contains(got, "API_PORT=8080") || strings.Contains(got, "DB_PATH=/slot-zero") {
		t.Errorf("slot 0's managed values carried over:\n%s", got)
	}
	if !strings.Contains(got, "API_PORT=4203") {
		t.Errorf("this worktree's allocation is missing:\n%s", got)
	}
	if strings.Count(got, "API_PORT=") != 1 || strings.Count(got, "DB_PATH=") != 1 {
		t.Errorf("exactly one definition of each managed key expected:\n%s", got)
	}
}

// TestFirstWriteSeedsNothingWhenSourceMissing is exit criterion 5's second
// half: a missing source file seeds nothing and is not an error.
func TestFirstWriteSeedsNothingWhenSourceMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	ctx, resolved := plainCtx()
	rep, err := Update(path, plainEnv(), ctx, resolved, filepath.Join(t.TempDir(), "nope", ".env"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rep.SeededFrom != "" {
		t.Errorf("seeded_from = %q, want empty", rep.SeededFrom)
	}
	if !strings.Contains(rep.SeedSkipped, "does not exist") {
		t.Errorf("seed_skipped = %q, want it to name the missing source", rep.SeedSkipped)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "API_PORT=4203") {
		t.Errorf("the block was still written without a seed:\n%s", got)
	}
}

// TestNoSeedWhenDisabled: emit.env.seed off means no seeding even with the
// main checkout's file present.
func TestNoSeedWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	mainEnv := filepath.Join(dir, "main", ".env")
	wtEnv := filepath.Join(dir, "wt", ".env")
	if err := os.MkdirAll(filepath.Dir(mainEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(wtEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mainEnv, "GITHUB_TOKEN=secret-token\n")
	env := plainEnv()
	env.Seed = false
	ctx, resolved := plainCtx()
	rep, err := Update(wtEnv, env, ctx, resolved, mainEnv)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SeededFrom != "" {
		t.Errorf("seeded_from = %q with seed off", rep.SeededFrom)
	}
	if strings.Contains(readFile(t, wtEnv), "secret-token") {
		t.Error("content was seeded with seed off")
	}
}

// TestUnbalancedMarkersRefused is exit criterion 6's unbalanced half: the
// write is refused and the line numbers reported.
func TestUnbalancedMarkersRefused(t *testing.T) {
	ctx, resolved := plainCtx()
	cases := []struct {
		name    string
		content string
		want    string // a fragment of the refusal
	}{
		{"unclosed block", "# top\n" + StartMarker + "\nAPI_PORT=1\n", "line 2"},
		{"stray end", "API_PORT=1\n" + EndMarker + "\n", "line 2"},
		{"nested start", StartMarker + "\nAPI_PORT=1\n" + StartMarker + "\n# --- end ---\n", "line 1, line 3"},
		{"duplicated blocks", StartMarker + "\n# --- end ---\n" + StartMarker + "\n# --- end ---\n", "line 1, line 3"},
		{"end before start", EndMarker + "\n" + StartMarker + "\n", "line 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			writeFile(t, path, tc.content)
			before := readFile(t, path)
			_, err := Update(path, plainEnv(), ctx, resolved, "")
			var me *MarkersError
			if !errors.As(err, &me) {
				t.Fatalf("error = %v, want *MarkersError", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q does not name %q", err, tc.want)
			}
			if after := readFile(t, path); after != before {
				t.Errorf("the refused file was modified:\nbefore: %q\nafter:  %q", before, after)
			}
		})
	}
}

// TestCommentedManagedKeyIsNotStripped: a commented-out definition is a
// comment, not a definition — only real definitions are stripped.
func TestCommentedManagedKeyIsNotStripped(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	writeFile(t, path, "# API_PORT=9999\n")
	ctx, resolved := plainCtx()
	rep, err := Update(path, plainEnv(), ctx, resolved, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Stripped) != 0 {
		t.Errorf("stripped = %v, want none", rep.Stripped)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "# API_PORT=9999") {
		t.Errorf("the comment did not survive:\n%s", got)
	}
}

// TestNewFileModeIsPrivate: a fresh .env may carry credentials (the seed
// copies tokens), so it is written 0600; an existing file keeps its mode.
func TestNewFileModeIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	ctx, resolved := plainCtx()
	if _, err := Update(path, plainEnv(), ctx, resolved, ""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Mode bits are the permission model on unix; on Windows they are not
	// meaningful (08-platform.md §4.6).
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("new .env mode = %o, want 600", fi.Mode().Perm())
	}
}

// TestOutsideBlockKeys is doctor's read-only half of the duplicate strip:
// the managed keys defined outside the managed block are reported without
// touching the file, and a key defined only inside the block is not.
func TestOutsideBlockKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	keys := map[string]string{"API_PORT": "{api}", "DB_PATH": "{db}"}

	// A key above the block, one below it, and one inside it.
	content := "API_PORT=9999\nOTHER=keep\n" + StartMarker + "\nDB_PATH=/x\n" + EndMarker + "\nDB_PATH=/dup\n"
	writeTestFile(t, path, content)
	outside, err := OutsideBlockKeys(path, keys)
	if err != nil {
		t.Fatalf("OutsideBlockKeys: %v", err)
	}
	if len(outside) != 2 || outside[0] != "API_PORT" || outside[1] != "DB_PATH" {
		t.Errorf("outside = %v, want [API_PORT DB_PATH] sorted", outside)
	}

	// A missing file has no keys outside any block.
	missing := filepath.Join(dir, "absent.env")
	outside, err = OutsideBlockKeys(missing, keys)
	if err != nil || len(outside) != 0 {
		t.Errorf("missing file = %v, %v; want none, nil", outside, err)
	}

	// The file is byte-identical afterwards: doctor's check writes nothing.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("the .env changed under a read-only check")
	}
}

// writeTestFile writes a test file.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestMarkersAreTheOneConvention pins the phase-7 rule literally: the
// generated artefacts and the .env block share one marker convention, and
// they share it by sharing the declaration — never a second one (plan.md §5
// phase 7).
func TestMarkersAreTheOneConvention(t *testing.T) {
	if StartMarker != managed.StartMarker || EndMarker != managed.EndMarker {
		t.Errorf("markers differ from the managed block's: %q/%q vs %q/%q",
			StartMarker, EndMarker, managed.StartMarker, managed.EndMarker)
	}
}
