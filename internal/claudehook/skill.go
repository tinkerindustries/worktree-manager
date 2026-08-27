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

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
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
)

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

// installSkill writes the skill's files. A file already holding exactly
// what would be written is left alone; one holding something else is an
// edit the user made, and is refused unless --force.
func installSkill(l Layout, opts Options) ([]Change, error) {
	rels, err := skillFiles()
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
			changes = append(changes, Change{Action: "write", Path: path})
		case err != nil:
			return nil, fmt.Errorf("reading %s: %w", path, err)
		case bytes.Equal(existing, body):
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: "already current"})
			continue
		case !opts.Force:
			return nil, &ConflictError{Path: path, Found: "edited since the worktree-onboarding skill was installed", Flag: "--force"}
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
	}
	return changes, nil
}

// uninstallSkill removes the skill's files and the directories that held
// them, leaving anything the user added in place. An edited file is
// refused unless --force, the same way install refuses to overwrite one.
func uninstallSkill(l Layout, opts Options) ([]Change, error) {
	rels, err := skillFiles()
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
		case !bytes.Equal(existing, body) && !opts.Force:
			return nil, &ConflictError{Path: path, Found: "edited since the worktree-onboarding skill was installed", Flag: "--force"}
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
