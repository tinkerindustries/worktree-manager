package descriptor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// fullDescriptor is a descriptor in the shape of 05-delivery.md §2.3, minus
// the deleted view field, with the resources map holding spec.Resolved
// entries — the shape the emitter writes and the reader parses. The slug is
// deliberately `no`, the YAML 1.1 boolean trap.
func fullDescriptor() *Descriptor {
	isolated := false
	return &Descriptor{
		Version:     1,
		App:         "compose-app",
		Slug:        "no",
		Slot:        7,
		Path:        "/Users/alex/Repos/compose-app/.claude/worktrees/no",
		Standalone:  false,
		Description: "fix dispatch lease race",
		Resources: map[string]spec.Resolved{
			"api":          {Type: "port", Value: 5407},
			"compose":      {Type: "namespace", Value: "compose-app-no-7"},
			"db":           {Type: "state-path", Value: "/Users/alex/.compose-app/worktrees/no-7/db.sqlite"},
			"compose_test": {Type: "namespace", Value: "compose-app-no-7-test"},
		},
		State: map[string]*Isolation{
			"db": {Isolated: &isolated},
		},
		Shared: []Shared{{
			Name:   "/Users/alex/.compose-app/db.sqlite",
			Impact: "writes are visible to every worktree and the main checkout",
		}},
		Extras: map[string]any{
			"harness.notes": "seeded from prod snapshot",
			"count":         3,
		},
	}
}

// TestWriteYAMLQuotesTheSlug is exit criterion 1: a descriptor emitted as
// YAML round-trips a worktree slugged `no` without turning it into false.
// The emission is unconditional, so `on`, `off`, `yes` and `y` are covered
// by the same rule.
func TestWriteYAMLQuotesTheSlug(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		for _, slug := range []string{"no", "on", "off", "yes", "y"} {
			t.Run(format+"/"+slug, func(t *testing.T) {
				d := fullDescriptor()
				d.Slug = slug
				path := filepath.Join(t.TempDir(), "descriptor."+format)
				if err := Write(path, format, d); err != nil {
					t.Fatalf("Write: %v", err)
				}
				back, err := Read(path, format)
				if err != nil {
					t.Fatalf("Read: %v", err)
				}
				if back.Slug != slug {
					t.Errorf("slug round-tripped as %q, want %q (a YAML 1.1 parser would coerce it to a boolean)", back.Slug, slug)
				}
			})
		}
	}
}

// TestWriteRoundTrip writes and reads back a full descriptor in both
// formats: the record survives the emitter unchanged apart from the
// extras map's numeric kinds (YAML decodes a bare number as uint64), which
// is the round-trip contract of the extras section.
func TestWriteRoundTrip(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			d := fullDescriptor()
			path := filepath.Join(t.TempDir(), "descriptor."+format)
			if err := Write(path, format, d); err != nil {
				t.Fatalf("Write: %v", err)
			}
			back, err := Read(path, format)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if back.Version != 1 || back.App != "compose-app" || back.Slug != "no" || back.Slot != 7 {
				t.Errorf("header = %+v", back)
			}
			if back.Resources["api"].Value != 5407 {
				t.Errorf("api = %#v, want int 5407", back.Resources["api"].Value)
			}
			if back.Resources["compose"].Value != "compose-app-no-7" {
				t.Errorf("compose = %#v", back.Resources["compose"].Value)
			}
			if back.State["db"] == nil || back.State["db"].Isolated == nil || *back.State["db"].Isolated {
				t.Errorf("state.db = %+v, want isolated: false", back.State["db"])
			}
			if len(back.Shared) != 1 || back.Shared[0].Name != "/Users/alex/.compose-app/db.sqlite" {
				t.Errorf("shared = %+v", back.Shared)
			}
			if back.Extras["harness.notes"] != "seeded from prod snapshot" {
				t.Errorf("extras = %+v, want the round-tripped key", back.Extras)
			}
		})
	}
}

// TestWriteLandsAtWorktreeRootUndotted is exit criterion 2's location half:
// the descriptor lands at the path the emitter is given — the worktree
// root joined with emit.descriptor.filename, which carries no leading dot
// (01-identity.md §4.3) — and the file exists there after the write.
func TestWriteLandsAtWorktreeRootUndotted(t *testing.T) {
	root := t.TempDir()
	filename := "wt-env.yaml" // emit.descriptor.filename, undotted
	path := filepath.Join(root, filename)
	if err := Write(path, "yaml", fullDescriptor()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the descriptor did not land at %s: %v", path, err)
	}
	if strings.HasPrefix(filename, ".") {
		t.Error("the descriptor filename carries a leading dot; plain ls must show it")
	}
}

// TestWriteIsAtomic is exit criterion 2's atomicity half: the write goes
// through a temp file in the same directory and a rename, so no partial
// descriptor is ever visible at the target path, and no temp file is left
// behind.
func TestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wt-env.yaml")
	if err := Write(path, "yaml", fullDescriptor()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "wt-env.yaml" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("after the write the directory holds %v; want exactly wt-env.yaml (no temp files)", names)
	}
}

// TestWriteUnknownFormat: the format comes from the spec, never sniffed.
func TestWriteUnknownFormat(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), "d"), "toml", fullDescriptor())
	if err == nil || !strings.Contains(err.Error(), "toml") {
		t.Errorf("error = %v, want the unknown format named", err)
	}
}

// sharedSpec is a spec whose shared block exercises both halves: a
// state-path resource with default: shared (the generated half) and a
// hand-authored entry whose name is a template over a resource.
const sharedSpec = `version: 1
app: plain-app
slots:
  max: 8
resources:
  - type: port
    name: api
  - type: state-path
    name: shared_db
    template: "{home}/.plain-app/shared/db.sqlite"
    default: shared
    seed:
      from: "{home}/.plain-app/shared/db.sqlite"
      modes: [shared]
      default: shared
shared:
  - name: "https://{app}.example.com/v1"
    impact: third-party API tenant, shared by every worktree
emit:
  descriptor:
    filename: wt-env.json
    format: json
`

// TestBuildSharedCarriesBothHalves is exit criterion 3: the generated half
// (a default: shared resource contributes itself with its resolved path and
// its impact text) and the hand-authored half (the spec's shared entries
// resolve their templates) both land in one block.
func TestBuildSharedCarriesBothHalves(t *testing.T) {
	s, err := spec.Parse([]byte(sharedSpec))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	ctx := spec.Context{Slug: "brisk-otter", Slot: 3, Home: "/Users/alex", Worktree: "/Users/alex/wt/brisk-otter"}
	resolved, err := spec.Resolve(s, spec.Context{
		App: s.App, Slug: ctx.Slug, Slot: ctx.Slot, Home: ctx.Home, Worktree: ctx.Worktree,
		Bases: map[string]int{"api": 4200},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The impact text is the driver's blast-radius prose, which only the
	// coordinator side can reach; the builder receives it as a callback.
	impact := func(r *spec.Resource) string {
		if r.Name == "shared_db" {
			return "state is not isolated: every worktree reads and writes the same path, and writes are visible to every worktree and the main checkout"
		}
		t.Errorf("impact asked for unexpected resource %q", r.Name)
		return ""
	}
	block, err := BuildShared(s, ctx, resolved, impact)
	if err != nil {
		t.Fatalf("BuildShared: %v", err)
	}
	if len(block) != 2 {
		t.Fatalf("block has %d entries, want 2 (one generated, one hand-authored): %+v", len(block), block)
	}
	// The generated half: the resolved path and the impact text.
	if block[0].Name != "/Users/alex/.plain-app/shared/db.sqlite" {
		t.Errorf("generated entry name = %q", block[0].Name)
	}
	if !strings.Contains(block[0].Impact, "not isolated") {
		t.Errorf("generated entry impact = %q", block[0].Impact)
	}
	// The hand-authored half: the template resolved against the context
	// and the resolved table.
	if block[1].Name != "https://plain-app.example.com/v1" {
		t.Errorf("hand-authored entry name = %q, want the resolved template", block[1].Name)
	}
	if block[1].Impact != "third-party API tenant, shared by every worktree" {
		t.Errorf("hand-authored entry impact = %q", block[1].Impact)
	}
}

// TestBuildSharedRefusesSilentOmission: a default: shared resource with no
// resolved value, or no impact text, is an error — a stale shared block is
// worse than none because it is believed (03-drivers.md §6).
func TestBuildSharedRefusesSilentOmission(t *testing.T) {
	s, err := spec.Parse([]byte(sharedSpec))
	if err != nil {
		t.Fatal(err)
	}
	ctx := spec.Context{Slug: "brisk-otter", Slot: 3, Home: "/Users/alex", Worktree: "/Users/alex/wt/brisk-otter"}
	resolved, err := spec.Resolve(s, spec.Context{
		App: s.App, Slug: ctx.Slug, Slot: ctx.Slot, Home: ctx.Home, Worktree: ctx.Worktree,
		Bases: map[string]int{"api": 4200},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildShared(s, ctx, map[string]spec.Resolved{}, func(*spec.Resource) string { return "x" }); err == nil {
		t.Error("empty resolved table produced a block instead of an error")
	}
	if _, err := BuildShared(s, ctx, resolved, func(*spec.Resource) string { return "" }); err == nil {
		t.Error("an empty impact text produced a block instead of an error")
	}
}

// TestBuildStateCarriesTheIsolationDecisions: one entry per state-path
// resource; default: shared is not isolated, everything else is. Non
// state-path resources do not appear.
func TestBuildStateCarriesTheIsolationDecisions(t *testing.T) {
	s, err := spec.Parse([]byte(sharedSpec))
	if err != nil {
		t.Fatal(err)
	}
	state := BuildState(s)
	if len(state) != 1 {
		t.Fatalf("state = %+v, want exactly the one state-path resource", state)
	}
	entry, ok := state["shared_db"]
	if !ok {
		t.Fatalf("state lacks shared_db: %+v", state)
	}
	if entry.Isolated == nil || *entry.Isolated {
		t.Errorf("shared_db isolated = %+v, want false (default: shared is not isolated)", entry.Isolated)
	}
	if _, ok := state["api"]; ok {
		t.Error("api (a port) appears in the state block")
	}

	// An isolated resource records isolated: true.
	spec2 := strings.Replace(sharedSpec, `    default: shared
`, "", 1)
	s2, err := spec.Parse([]byte(spec2))
	if err != nil {
		t.Fatal(err)
	}
	state2 := BuildState(s2)
	if state2["shared_db"].Isolated == nil || !*state2["shared_db"].Isolated {
		t.Errorf("default-isolated resource isolated = %+v, want true", state2["shared_db"].Isolated)
	}
}

// TestWriteEmitsThroughTheReader: the emitted YAML is parseable by this
// package's own reader with unknown fields refused — the writer and the
// reader share one contract (05-delivery.md §2.3).
func TestWriteEmitsThroughTheReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wt-env.yaml")
	d := fullDescriptor()
	d.Extras = nil
	if err := Write(path, "yaml", d); err != nil {
		t.Fatal(err)
	}
	back, err := Read(path, "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if back.App != d.App || back.Slug != d.Slug || back.Slot != d.Slot {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}
