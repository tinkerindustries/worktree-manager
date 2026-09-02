package driver

// statepath.go is the state-path driver: a per-worktree filesystem path,
// applied by creating the directory and seeding per mode, purged on
// teardown by the spec's policy, and guarded by the structural purge
// refusal — a purge whose resolved path is the shared source, or any
// ancestor of it, is refused and the refusal names the path
// (03-drivers.md §4.4, B6.3).
//
// The policy is the repository's: a store survives a teardown that did not
// select it, unless the resource declares purge.on_teardown: always, which
// deletes it as part of the teardown and leaves purge.keep_flag as the
// one-run way to say otherwise. spec.Purges is the decision, so what
// `wt rm` reports and what this driver deletes come from one rule.
//
// A seeded store is a snapshot taken at creation that diverges as work
// continues, not a live mirror (B6.2): the driver records that seeding
// occurred and when in a marker file beside the target, and verify reports
// it for phase 7's briefing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// StatePath is the state-path driver.
type StatePath struct{}

// Type reports the resource type.
func (*StatePath) Type() string { return "state-path" }

// HasApply reports that the driver creates the directory and seeds it.
func (*StatePath) HasApply() bool { return true }

// HasTeardown reports that the driver purges — by the spec's purge policy,
// which is flag-only unless the resource declares on_teardown: always.
func (*StatePath) HasTeardown() bool { return true }

// GatesAllocation reports that a path is not a shared resource: a per-slot
// path cannot be held by anything else, so the probe never skips a slot.
func (*StatePath) GatesAllocation() bool { return false }

// Derive returns the resolved path, exactly as spec.Resolve computes it.
func (*StatePath) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	v, ok := table[r.Name]
	if !ok {
		return nil, fmt.Errorf("state-path driver: no resolved value for resource %q", r.Name)
	}
	return v.Value, nil
}

// Probe reports the path free: the derivation embeds slot and slug, so no
// other worktree can hold it. A path that already exists is not a collision
// — re-running init reconciles, it never reallocates (ARCHITECTURE.md §8.6
// rule 5).
func (*StatePath) Probe(r *spec.Resource, value any, env Env) ProbeResult {
	return ProbeFree
}

// Apply creates the directory and seeds per mode: seeded (a snapshot of the
// live shared source), empty (a fresh directory, no copy) or shared (no
// isolation at all). A resource with no seed declaration gets its parent
// directory created and nothing else — the app owns the file.
func (sp *StatePath) Apply(r *spec.Resource, value any, env Env) (ApplyResult, error) {
	var res ApplyResult
	path, err := sp.path(value)
	if err != nil {
		return res, err
	}
	dirTarget := strings.HasSuffix(fmt.Sprint(value), "/")
	mode := sp.mode(r, env)

	switch mode {
	case "shared":
		res.Note("mode shared: no isolation — %s is the shared store itself; nothing is created", path)
		return res, nil
	case "":
		dir := path
		if !dirTarget {
			dir = filepath.Dir(path)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return res, fmt.Errorf("creating %s: %w", dir, err)
		}
		res.Note("created %s", dir)
		return res, nil
	case "empty":
		if err := os.MkdirAll(path, 0o755); err != nil {
			return res, fmt.Errorf("creating %s: %w", path, err)
		}
		res.Note("created %s empty (mode empty: no copy)", path)
		return res, nil
	case "seeded":
		from, err := sp.resolveFrom(r, env)
		if err != nil {
			return res, err
		}
		if dirTarget {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return res, fmt.Errorf("creating %s: %w", path, err)
			}
		} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return res, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
		}
		if err := sp.seed(path, dirTarget, from, &res); err != nil {
			return res, err
		}
		if err := sp.writeMarker(r.Name, path, from); err != nil {
			return res, err
		}
		res.Note("seeded %s from %s at %s — a snapshot taken at creation that diverges as work continues, not a live mirror",
			path, from, time.Now().UTC().Format(time.RFC3339))
		return res, nil
	default:
		return res, fmt.Errorf("resource %q: unknown seed mode %q", r.Name, mode)
	}
}

// seed copies the shared source into the target per the seeded mode. An
// absent source is a legitimate first-run state — the shared store has not
// been created yet — so the target is created empty and the note says so
// (bounded coverage is stated, never a silent empty).
func (sp *StatePath) seed(path string, dirTarget bool, from string, res *ApplyResult) error {
	fi, err := os.Stat(from)
	if err != nil {
		if os.IsNotExist(err) {
			if !dirTarget {
				// The file target's directory exists; the app creates the
				// file on first run.
				res.Note("the shared source %s does not exist yet; %s starts empty until the app creates it", from, path)
				return nil
			}
			res.Note("the shared source %s does not exist yet; %s starts empty", from, path)
			return nil
		}
		return fmt.Errorf("stat the shared source %s: %w", from, err)
	}
	if fi.IsDir() {
		if !dirTarget {
			return fmt.Errorf("the shared source %s is a directory but %s is a file target (the template does not end with '/')", from, path)
		}
		return copyTree(from, path, res)
	}
	if dirTarget {
		return fmt.Errorf("the shared source %s is a file but %s is a directory target (the template ends with '/')", from, path)
	}
	return copyFile(from, path)
}

// Teardown purges the path when the spec's purge policy says to: the run
// selected the resource, or the resource declares on_teardown: always and
// the run did not pass its keep flag (03-drivers.md §4.4). The purge
// refusal is structural: a purge whose resolved path is
// the shared source or any ancestor of it is refused, naming the resolved
// path, because deleting the shared store would wipe every project's data.
// The check runs on the resolved, symlink-realised path using
// identity.Contains, reached through a symlink as well as directly (M1
// §3.1).
func (sp *StatePath) Teardown(r *spec.Resource, value any, env Env) error {
	path, err := sp.path(value)
	if err != nil {
		return err
	}
	if !spec.Purges(r, env.PurgeFlags, env.KeepFlags) {
		return nil
	}
	if sp.mode(r, env) == "shared" && !spec.PurgeSelected(r, env.PurgeFlags) {
		// Mode shared: the resolved path is the shared store rather than
		// this worktree's copy of it, and apply created nothing. A purge
		// the run did not ask for never deletes it; one that names the
		// resource still faces the structural refusal below.
		return nil
	}
	if r.Seed != nil {
		from, err := sp.resolveFrom(r, env)
		if err != nil {
			// A purge whose safety check cannot run fails closed: without
			// the resolved shared source there is no way to know the purge
			// target is not it, so nothing is deleted.
			return fmt.Errorf("cannot check the purge target %s against the shared source: %w", path, err)
		}
		if identity.Contains(path, from) {
			realized, rerr := platform.RealPath(path)
			if rerr != nil {
				realized = path
			}
			// The refusal is read by a person, so the realised path leaves
			// the process in the spelling they would type: on Windows
			// RealPath answers \\?\C:\..., which names a path nobody
			// recognises and no other tool accepts.
			realized = platform.ExternalPath(realized)
			return &RefusalError{Reason: fmt.Sprintf(
				"refusing to purge %s: it is the shared source %s, or an ancestor of it — deleting it would wipe every project's data",
				realized, from)}
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("purging %s: %w", path, err)
	}
	sp.removeMarker(path)
	return nil
}

// Verify reports whether the path exists, is writable, and — for the seeded
// mode — when it was seeded. Verify changes nothing: the writability probe
// is an open-for-append on a file or a create-and-remove of one probe file
// in a directory.
func (sp *StatePath) Verify(r *spec.Resource, value any, env Env) ([]Finding, error) {
	path, err := sp.path(value)
	if err != nil {
		return nil, err
	}
	mode := sp.mode(r, env)
	var findings []Finding

	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if mode == "shared" {
				findings = append(findings, Finding{Resource: r.Name, Kind: "shared-path-absent", Level: LevelInfo,
					Message: fmt.Sprintf("shared path %s does not exist yet (nothing has created it)", path)})
			} else {
				findings = append(findings, Finding{Resource: r.Name, Kind: "path-missing", Level: LevelError,
					Message: fmt.Sprintf("path %s does not exist (apply has not run, or the directory was deleted by hand); re-run wt init", path)})
			}
			return findings, nil
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	if fi.IsDir() {
		probe := filepath.Join(path, ".wt-write-probe")
		f, perr := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if perr != nil {
			findings = append(findings, Finding{Resource: r.Name, Kind: "path-not-writable", Level: LevelError,
				Message: fmt.Sprintf("%s is not writable: %v", path, perr)})
		} else {
			f.Close()
			os.Remove(probe)
		}
	} else if f, perr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); perr != nil {
		findings = append(findings, Finding{Resource: r.Name, Kind: "path-not-writable", Level: LevelError,
			Message: fmt.Sprintf("%s is not writable: %v", path, perr)})
	} else {
		f.Close()
	}

	if mode == "seeded" {
		at, from, ok := sp.readMarker(path)
		if !ok {
			findings = append(findings, Finding{Resource: r.Name, Kind: "seed-marker-missing", Level: LevelWarning,
				Message: fmt.Sprintf("no seed marker found beside %s; when it was seeded is unknown (seeded by hand, or before markers existed)", path)})
		} else {
			findings = append(findings, Finding{Resource: r.Name, Kind: "seeded", Level: LevelInfo,
				Message: fmt.Sprintf("seeded from %s at %s — a snapshot taken at creation, not a live mirror", from, at)})
		}
	}
	return findings, nil
}

// BlastRadius is the shared-block prose for a state-path resource.
func (*StatePath) BlastRadius(r *spec.Resource, s *spec.Spec) string {
	return "state is not isolated: every worktree reads and writes the same path, and writes are visible to every worktree and the main checkout"
}

// path validates the resolved value and cleans it.
func (sp *StatePath) path(value any) (string, error) {
	raw, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("state-path driver: value is %T, want a resolved path string", value)
	}
	if raw == "" {
		return "", fmt.Errorf("state-path driver: resolved path is empty")
	}
	return filepath.Clean(raw), nil
}

// mode returns the seed mode for a resource: the per-run override from env,
// else the spec's seed.default, else the schema default. A resource with no
// seed declaration has no mode (""), and apply creates the directory only.
func (sp *StatePath) mode(r *spec.Resource, env Env) string {
	if m, ok := env.SeedModes[r.Name]; ok {
		return m
	}
	if r.Seed == nil {
		return ""
	}
	if r.Seed.Default != nil {
		return *r.Seed.Default
	}
	return spec.DefaultSeedMode
}

// resolveFrom resolves the seed.from template against the same context the
// values were derived with.
func (sp *StatePath) resolveFrom(r *spec.Resource, env Env) (string, error) {
	if r.Seed == nil || r.Seed.From == "" {
		return "", fmt.Errorf("resource %q declares no seed.from; there is no shared source to check", r.Name)
	}
	ctx := spec.Context{
		App: env.App, Slug: env.Slug, Slot: env.Slot,
		Home: env.Home, Worktree: env.Worktree, Bases: env.Bases,
	}
	return spec.Substitute(r.Seed.From, ctx, env.Resolved)
}

// seedMarker is the marker file recording that seeding occurred and when —
// the fact phase 7 renders into the briefing (B6.2).
type seedMarker struct {
	Resource string `json:"resource"`
	Mode     string `json:"mode"`
	From     string `json:"from"`
	At       string `json:"at"`
}

// markerPath is the marker's location: a dotfile beside the target, so a
// directory target stays clean and a file target needs no wrapper.
func markerPath(path string) string {
	return filepath.Join(filepath.Dir(path), ".wt-seed-"+filepath.Base(path)+".json")
}

// writeMarker records the seeding fact atomically.
func (sp *StatePath) writeMarker(resource, path, from string) error {
	m := seedMarker{Resource: resource, Mode: "seeded", From: from, At: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encoding the seed marker: %w", err)
	}
	target := markerPath(path)
	tmp, err := os.CreateTemp(filepath.Dir(target), ".wt-seed-*.tmp")
	if err != nil {
		return fmt.Errorf("writing the seed marker: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing the seed marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing the seed marker: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("writing the seed marker: %w", err)
	}
	return nil
}

// readMarker returns the recorded seeded-at time and source, or ok=false
// when no marker exists or it cannot be read.
func (sp *StatePath) readMarker(path string) (at, from string, ok bool) {
	data, err := os.ReadFile(markerPath(path))
	if err != nil {
		return "", "", false
	}
	var m seedMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", false
	}
	return m.At, m.From, true
}

// removeMarker drops the marker beside a purged path.
func (sp *StatePath) removeMarker(path string) {
	os.Remove(markerPath(path))
}

// copyFile copies one regular file, preserving nothing but the bytes.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("opening %s: %w", from, err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("creating %s: %w", to, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s to %s: %w", from, to, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("copying %s to %s: %w", from, to, err)
	}
	return nil
}

// copyTree copies a directory tree. Non-regular entries are skipped with a
// note — a snapshot of the live store's files, and a symlink or device in
// it is the store's own business, not something the tool follows.
func copyTree(from, to string, res *ApplyResult) error {
	return filepath.Walk(from, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dest := filepath.Join(to, rel)
		switch {
		case fi.IsDir():
			if rel == "." {
				return nil
			}
			return os.MkdirAll(dest, fi.Mode().Perm())
		case fi.Mode().IsRegular():
			return copyFile(p, dest)
		default:
			res.Note("skipped %s in the shared source: not a regular file", rel)
			return nil
		}
	})
}

// isRefusal reports whether err is a RefusalError.
func isRefusal(err error) bool {
	var re *RefusalError
	return errors.As(err, &re)
}
