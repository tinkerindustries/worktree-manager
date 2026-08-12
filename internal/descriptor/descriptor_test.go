package descriptor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sample is a descriptor in the shape of 05-delivery.md §2.3, minus the view
// field revision 2 deleted, with the resources map holding spec.Resolved
// entries — the shape phase 0 fixed for the descriptor's resource map. YAML
// and JSON carry the same values.
const sampleYAML = `version: 1
app: bacio
slug: brisk-otter
slot: 7
path: /Users/geoff/Repos/bacio/.claude/worktrees/brisk-otter
standalone: false
description: fix dispatch lease race
resources:
  api:
    type: port
    value: 5407
  compose:
    type: namespace
    value: bacio-brisk-otter
  db:
    type: state-path
    value: /Users/geoff/.bacio/db.sqlite
state:
  db:
    isolated: false
  seeded: null
shared:
  - name: /Users/geoff/.bacio/db.sqlite
    impact: writes are visible to every worktree and the main checkout
extras:
  harness.notes: seeded from prod snapshot
`

const sampleJSON = `{
  "version": 1,
  "app": "bacio",
  "slug": "brisk-otter",
  "slot": 7,
  "path": "/Users/geoff/Repos/bacio/.claude/worktrees/brisk-otter",
  "standalone": false,
  "description": "fix dispatch lease race",
  "resources": {
    "api": {"type": "port", "value": 5407},
    "compose": {"type": "namespace", "value": "bacio-brisk-otter"},
    "db": {"type": "state-path", "value": "/Users/geoff/.bacio/db.sqlite"}
  },
  "state": {"db": {"isolated": false}, "seeded": null},
  "shared": [{"name": "/Users/geoff/.bacio/db.sqlite", "impact": "writes are visible to every worktree and the main checkout"}],
  "extras": {"harness.notes": "seeded from prod snapshot"}
}
`

func writeSample(t *testing.T, content, format string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "descriptor."+format)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func checkSample(t *testing.T, d *Descriptor) {
	t.Helper()
	if d.Version != 1 || d.App != "bacio" || d.Slug != "brisk-otter" || d.Slot != 7 {
		t.Errorf("header = %+v", d)
	}
	if d.Path != "/Users/geoff/Repos/bacio/.claude/worktrees/brisk-otter" || d.Standalone || d.Description != "fix dispatch lease race" {
		t.Errorf("identity fields = %+v", d)
	}
	if d.Resources["api"].Value != 5407 {
		t.Errorf("api = %#v (%T), want int 5407", d.Resources["api"].Value, d.Resources["api"].Value)
	}
	if d.Resources["compose"].Value != "bacio-brisk-otter" {
		t.Errorf("compose = %#v", d.Resources["compose"].Value)
	}
	if d.State["db"] == nil || d.State["db"].Isolated == nil || *d.State["db"].Isolated {
		t.Errorf("state.db = %+v, want isolated: false", d.State["db"])
	}
	if d.State["seeded"] != nil {
		t.Errorf("state.seeded = %+v, want null", d.State["seeded"])
	}
	if len(d.Shared) != 1 || d.Shared[0].Name != "/Users/geoff/.bacio/db.sqlite" || !strings.Contains(d.Shared[0].Impact, "every worktree") {
		t.Errorf("shared = %+v", d.Shared)
	}
	if d.Extras["harness.notes"] != "seeded from prod snapshot" {
		t.Errorf("extras = %+v, want the round-tripped key", d.Extras)
	}
}

func TestReadYAML(t *testing.T) {
	d, err := Read(writeSample(t, sampleYAML, "yaml"), "yaml")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	checkSample(t, d)
}

func TestReadJSON(t *testing.T) {
	d, err := Read(writeSample(t, sampleJSON, "json"), "json")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	checkSample(t, d)
}

// TestReadNewerVersion is 05-delivery.md §8's first row: a descriptor whose
// schema version is newer than this binary understands is refused to be read,
// naming the upgrade.
func TestReadNewerVersion(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		content := sampleYAML
		if format == "json" {
			content = sampleJSON
		}
		content = strings.Replace(content, `"version": 1`, `"version": 2`, 1)
		content = strings.Replace(content, "version: 1", "version: 2", 1)
		_, err := Read(writeSample(t, content, format), format)
		var ve *VersionError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: error = %v, want *VersionError", format, err)
		}
		if ve.Found != 2 {
			t.Errorf("%s: Found = %d, want 2", format, ve.Found)
		}
		if !strings.Contains(err.Error(), "newer") || !strings.Contains(err.Error(), "upgrade wt") {
			t.Errorf("%s: refusal does not name the upgrade: %v", format, err)
		}
	}
}

// TestReadUnparseable is 05-delivery.md §8's second row: a descriptor that
// will not parse is reported with `wt init` named as the rebuild — the
// reader reports the parse failure, and the caller names wt init; nothing
// here ever overwrites it.
func TestReadUnparseable(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		_, err := Read(writeSample(t, "version: 1\n  broken: [", format), format)
		if err == nil {
			t.Errorf("%s: unparseable descriptor read cleanly", format)
		}
	}
}

// TestReadMissingVersion: a descriptor without a version is refused whole.
func TestReadMissingVersion(t *testing.T) {
	content := strings.Replace(sampleYAML, "version: 1\n", "", 1)
	if _, err := Read(writeSample(t, content, "yaml"), "yaml"); err == nil {
		t.Error("versionless descriptor read cleanly")
	}
}

// TestReadUnknownFormat: the format comes from the spec, never sniffed.
func TestReadUnknownFormat(t *testing.T) {
	if _, err := Read(writeSample(t, sampleYAML, "yaml"), "toml"); err == nil {
		t.Error("unknown format read cleanly")
	}
}

// TestReadUnknownField: a field from a newer schema version is refused whole
// rather than silently ignored.
func TestReadUnknownField(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		content := sampleYAML + "view: 2f9c...\n"
		if format == "json" {
			content = strings.Replace(sampleJSON, `"extras": {`, `"view": "2f9c...", "extras": {`, 1)
		}
		if _, err := Read(writeSample(t, content, format), format); err == nil {
			t.Errorf("%s: unknown field read cleanly", format)
		}
	}
}

// TestReadPortNormalization: JSON numbers come back as the int the emitter
// wrote, so both formats agree on the shape consumers read.
func TestReadPortNormalization(t *testing.T) {
	content := `{"version": 1, "app": "a", "slug": "s", "slot": 1,
		"path": "/p", "standalone": false, "description": "",
		"resources": {"api": {"type": "port", "value": 5407}},
		"state": {}, "shared": [], "extras": {}}`
	d, err := Read(writeSample(t, content, "json"), "json")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	v, ok := d.Resources["api"].Value.(int)
	if !ok || v != 5407 {
		t.Errorf("api value = %#v (%T), want int 5407", d.Resources["api"].Value, d.Resources["api"].Value)
	}
}

// TestReadMissingFile: the file is not there; the error names the path.
func TestReadMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nope.yaml"), "yaml")
	if err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("error = %v, want it to name the path", err)
	}
}
