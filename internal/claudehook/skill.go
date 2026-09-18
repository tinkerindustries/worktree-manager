package claudehook

// skill.go installs the worktree-onboarding skill into Claude Code's
// user skills directory, so the notice the create hook prints in an
// unadopted repository names something the reader can actually run. The
// skill is a document — the judgment half of the system — and installing
// it makes no policy call: it puts the same three files in
// ~/.claude/skills/worktree-onboarding that this repository holds in
// .claude/skills/worktree-onboarding.
//
// The embedded copy under skill/ is exactly those files; a test compares
// the two and fails on drift, which is how every other generated-and-
// committed artefact in this repository is kept honest.
//
// Ownership — is an installed file wt's own, or the user's? — is recorded
// in a manifest beside the documents rather than in them.
//
// The rest of the tool answers that question with the managed block, and
// the hook scripts do here too. A skill file cannot. These are documents
// copied verbatim, not files rendered from a spec, and one of them —
// references/primitives.md — documents the managed block convention and so
// quotes the markers inside a fenced example. Appending a real block would
// give that file two start markers, which is the nested shape
// managed.MarkersError exists to refuse, and a marker test would read any
// copy explaining the convention as wt's own. So the record goes in a
// sidecar, which also keeps the installed skill byte-identical to the copy
// this repository holds.
//
// What the manifest is for: content equality cannot answer the ownership
// question. A file that differs from what this binary would write is either
// an older release's copy or an edit, and the two want opposite treatment.
// Reading "differs" as "edited" left `wt claude install --refresh-only`
// unable to update this skill at all — every file that legitimately changed
// between releases was skipped as a user edit, which is the one thing
// --refresh-only exists to prevent.

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

//go:embed skill
var skillFS embed.FS

const (
	// skillRoot is the embedded directory the skill files are read from.
	skillRoot = "skill"
	// SkillName is the directory the skill is installed as, which is also
	// the name Claude Code invokes it by. It names what the skill does
	// rather than what it is, because in a user's skills directory it
	// sits beside every other tool's.
	SkillName = "worktree-onboarding"
	// skillsDir is Claude Code's user skills directory, relative to Dir.
	skillsDir = "skills"
	// skillMode is 0644: a document, never executed.
	skillMode = 0o644
	// skillManifestName is the record of what wt last wrote, beside the
	// files it describes. Dotted so it is not mistaken for part of the
	// skill, and removed by uninstall with them.
	skillManifestName = ".wt-installed.json"
	// skillManifestVersion versions that record. A newer one is refused
	// rather than half-read, the way every other versioned thing here is.
	skillManifestVersion = 1
)

// skillManifest records the digest of each file as wt wrote it. A file
// still holding its recorded digest is wt's own, however many releases old;
// anything else is the user's.
type skillManifest struct {
	SchemaVersion int `json:"schema_version"`
	// Files maps a slash-separated relative path to the hex sha256 of the
	// bytes wt wrote there.
	Files map[string]string `json:"files"`
}

// ManifestPath is where the record of the installed skill lives.
func (l Layout) ManifestPath() string { return filepath.Join(l.SkillDir(), skillManifestName) }

// digest is the hex sha256 of one file's bytes.
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readSkillManifest reads the record, or an empty one when there is none.
//
// An absent manifest is the ordinary state of an installation made before
// this record existed, and is not an error: the caller falls back to the
// one thing it can still prove, which is that a file byte-identical to what
// this binary would write is wt's own whoever wrote it.
//
// A manifest that will not parse is treated the same way — as no record —
// because it says nothing about the files and refusing the whole install
// over it would strand an installation on a corrupt sidecar. A manifest
// from a newer schema is different: it is a record this build cannot read
// correctly, so it refuses and names the upgrade.
func readSkillManifest(l Layout) (skillManifest, error) {
	empty := skillManifest{SchemaVersion: skillManifestVersion, Files: map[string]string{}}
	data, err := os.ReadFile(l.ManifestPath())
	if err != nil {
		return empty, nil
	}
	var m skillManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return empty, nil
	}
	if m.SchemaVersion > skillManifestVersion {
		return empty, fmt.Errorf("%s records schema version %d; this wt understands %d — upgrade wt",
			l.ManifestPath(), m.SchemaVersion, skillManifestVersion)
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	m.SchemaVersion = skillManifestVersion
	return m, nil
}

// writeSkillManifest records what was written.
func writeSkillManifest(l Layout, m skillManifest) error {
	m.SchemaVersion = skillManifestVersion
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", l.ManifestPath(), err)
	}
	if err := os.MkdirAll(l.SkillDir(), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", l.SkillDir(), err)
	}
	return platform.AtomicWrite(l.ManifestPath(), append(data, '\n'), skillMode)
}

// oursByManifest reports whether an installed skill file is wt's own to
// replace: its digest is the one wt recorded writing, or it is byte-
// identical to what this binary would write anyway.
//
// The second half is what carries an installation made before the manifest
// existed across this change. Rewriting a file with exactly its own content
// changes nothing a reader would see, so adopting it is safe without
// knowing who put it there.
func oursByManifest(m skillManifest, rel string, existing, body []byte) bool {
	if recorded, ok := m.Files[rel]; ok && recorded == digest(existing) {
		return true
	}
	return bytes.Equal(existing, body)
}

// skillUnrecorded is what install and uninstall found when a skill file is
// neither the one wt recorded writing nor the one it would write now. It is
// deliberately not "edited": with no record, wt cannot tell an older
// release's copy from a document the user rewrote, and naming one would be
// inventing an answer.
const skillUnrecorded = "not the copy wt recorded installing: either an edit, or a copy from a release that kept no record"

// SkillDir is where the worktree-onboarding skill is installed.
func (l Layout) SkillDir() string { return filepath.Join(l.Dir, skillsDir, SkillName) }

// skillFiles lists the embedded skill's files by their path relative to
// the skill directory, in a stable order.
func skillFiles() ([]string, error) {
	var rel []string
	err := fs.WalkDir(skillFS, skillRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		r, err := filepath.Rel(skillRoot, path)
		if err != nil {
			return err
		}
		rel = append(rel, filepath.ToSlash(r))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the embedded onboarding skill: %w", err)
	}
	sort.Strings(rel)
	return rel, nil
}

// skillBody reads one embedded skill file.
func skillBody(rel string) ([]byte, error) {
	data, err := skillFS.ReadFile(skillRoot + "/" + rel)
	if err != nil {
		return nil, fmt.Errorf("reading the embedded onboarding skill: %w", err)
	}
	return data, nil
}

// installSkill writes the skill's files.
//
// A file already holding what would be written is left alone. One that wt
// recorded writing is its own to replace, however stale — which is what
// makes an upgrade reach this skill at all. Anything else is the user's and
// is refused unless --force.
func installSkill(l Layout, opts Options) ([]Change, error) {
	rels, err := skillFiles()
	if err != nil {
		return nil, err
	}
	manifest, err := readSkillManifest(l)
	if err != nil {
		return nil, err
	}
	// Built up as the files are written, then recorded once at the end, so
	// a run that refuses part way leaves the old record rather than a
	// half-written one claiming files it did not write.
	written := map[string]string{}
	var changes []Change
	for _, rel := range rels {
		body, err := skillBody(rel)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(l.SkillDir(), filepath.FromSlash(rel))
		existing, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			changes = append(changes, Change{Action: "write", Path: path})
		case err != nil:
			return nil, fmt.Errorf("reading %s: %w", path, err)
		case bytes.Equal(existing, body) && manifest.Files[rel] == digest(body):
			// Current, and already recorded: nothing at all to do.
			written[rel] = digest(body)
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: "already current"})
			continue
		case oursByManifest(manifest, rel, existing, body):
			if bytes.Equal(existing, body) {
				// Current but unrecorded — an installation from before the
				// record existed. The file needs no write; the record does.
				written[rel] = digest(body)
				changes = append(changes, Change{Action: "unchanged", Path: path, Detail: "already current"})
				continue
			}
			changes = append(changes, Change{Action: "replace", Path: path})
		case !opts.Force:
			ce := &ConflictError{Path: path, Found: skillUnrecorded, Flag: "--force"}
			if opts.RefreshOnly {
				changes = append(changes, Change{Action: "skipped", Path: path, Detail: skipDetail(ce)})
				continue
			}
			return nil, ce
		default:
			changes = append(changes, Change{Action: "replace", Path: path})
		}
		if opts.DryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
		}
		if err := platform.AtomicWrite(path, body, skillMode); err != nil {
			return nil, err
		}
		written[rel] = digest(body)
	}
	if opts.DryRun {
		return changes, nil
	}
	// A file this run skipped keeps whatever the old record said about it,
	// so a later run still recognises a copy wt wrote two releases ago.
	for rel, d := range manifest.Files {
		if _, ok := written[rel]; !ok {
			written[rel] = d
		}
	}
	if err := writeSkillManifest(l, skillManifest{Files: written}); err != nil {
		return nil, err
	}
	return changes, nil
}

// uninstallSkill removes the skill's files and the directories that held
// them, leaving anything the user added in place. Ownership is read exactly
// as install reads it — the recorded digest, or a file identical to what
// this binary would write — so a copy from an older release is removed and
// one the user made their own is refused unless --force.
func uninstallSkill(l Layout, opts Options) ([]Change, error) {
	rels, err := skillFiles()
	if err != nil {
		return nil, err
	}
	manifest, err := readSkillManifest(l)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, rel := range rels {
		body, err := skillBody(rel)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(l.SkillDir(), filepath.FromSlash(rel))
		existing, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: "not present"})
			continue
		case err != nil:
			return nil, fmt.Errorf("reading %s: %w", path, err)
		case !oursByManifest(manifest, rel, existing, body) && !opts.Force:
			return nil, &ConflictError{Path: path, Found: skillUnrecorded, Flag: "--force"}
		}
		changes = append(changes, Change{Action: "remove", Path: path})
		if opts.DryRun {
			continue
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing %s: %w", path, err)
		}
	}
	if opts.DryRun {
		return changes, nil
	}
	// The record goes with the files it described. It is removed last, so a
	// run that refused above leaves it describing what is still there.
	if err := os.Remove(l.ManifestPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("removing %s: %w", l.ManifestPath(), err)
	}
	// Remove the skill's own directories, deepest first, and only while
	// they are empty: a file the user added there keeps its directory.
	pruneEmpty(l.SkillDir())
	return changes, nil
}

// pruneEmpty removes dir and its empty subdirectories, deepest first,
// stopping at the first that still holds something. Failures are not
// reported: an un-removed empty directory is not a failed uninstall.
func pruneEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			pruneEmpty(filepath.Join(dir, e.Name()))
		}
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		os.Remove(dir)
	}
}
