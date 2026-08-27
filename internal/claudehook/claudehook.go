// Package claudehook registers worktree-manager as Claude Code's worktree
// creator, which is what `wt claude install` and `wt claude uninstall`
// drive.
//
// Claude Code has two hook events for worktree lifecycle, WorktreeCreate
// and WorktreeRemove. The first is not an observer: when one is registered
// Claude Code calls it instead of running `git worktree add` itself, takes
// the absolute path the hook prints on stdout as the new worktree, and
// fails worktree creation outright if the hook prints nothing. So the
// scripts written here answer for every repository on the machine — the
// adopted ones with `wt init`, and every other one with the plain worktree
// Claude Code would have made anyway.
//
// Nothing here is per-repository. The scripts are byte-identical on every
// machine but for the managed block recording which wt binary they call,
// and they decide what a repository is at run time by looking for its
// wt.yaml — the adoption signal, and the same walk internal/spec makes.
// That is why this is not an internal/artefact file: those are generated
// per repo from spec fields and are the worktree-onboarding skill's
// business, and cmd/wt never imports that package.
//
// The two files this writes are the user's, not a repository's: the
// scripts under <claude-dir>/hooks and two entries in
// <claude-dir>/settings.json. Merging is per event and never wholesale —
// the user's own hooks, and every other setting in the file, survive an
// install and an uninstall. A settings.json that does not parse refuses
// the write rather than being clobbered, and an event already pointing at
// somebody else's script refuses too, naming --force.
package claudehook

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/managed"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

//go:embed templates/worktree-create.sh
var createTemplate []byte

//go:embed templates/worktree-remove.sh
var removeTemplate []byte

// The Claude Code hook events this package registers, and the file each
// event's script is written to.
const (
	CreateEvent  = "WorktreeCreate"
	RemoveEvent  = "WorktreeRemove"
	createScript = "wt-worktree-create.sh"
	removeScript = "wt-worktree-remove.sh"

	// settingsFile is Claude Code's user settings, the file the hook
	// entries are merged into.
	settingsFile = "settings.json"
	// hooksDir is where Claude Code's own convention puts hook scripts.
	hooksDir = "hooks"

	// scriptMode is 0755: a hook Claude Code has to be able to execute.
	scriptMode = 0o755
	// settingsMode is 0644, matching what Claude Code writes itself. The
	// file holds no secret — the hook entries are two paths.
	settingsMode = 0o644
)

// scripts pairs each event with its script file, in the order install
// reports them.
var scripts = []struct {
	Event string
	File  string
	Body  []byte
}{
	{CreateEvent, createScript, createTemplate},
	{RemoveEvent, removeScript, removeTemplate},
}

// Layout locates the files an install or uninstall touches. Dir is the
// Claude Code configuration directory — ~/.claude, or whatever --prefix
// named, so a test never touches the real one.
type Layout struct {
	Dir string
}

// DefaultLayout is ~/.claude, the location Claude Code reads user settings
// and user hooks from.
func DefaultLayout() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("locating the home directory: %w", err)
	}
	return Layout{Dir: filepath.Join(home, ".claude")}, nil
}

// SettingsPath is the settings file the hook entries are merged into.
func (l Layout) SettingsPath() string { return filepath.Join(l.Dir, settingsFile) }

// HooksDir is the directory the scripts are written to.
func (l Layout) HooksDir() string { return filepath.Join(l.Dir, hooksDir) }

// ScriptPath is where one event's script lives.
func (l Layout) ScriptPath(file string) string { return filepath.Join(l.HooksDir(), file) }

// LogPath is the log the scripts append to. Neither install nor uninstall
// writes or removes it; it is reported so a reader knows where a hook's
// own account of a failure went.
func (l Layout) LogPath() string { return filepath.Join(l.HooksDir(), "wt-worktree-hook.log") }

// Change is one thing an install or uninstall did, or — under DryRun —
// would have done.
type Change struct {
	// Action is one of: write, replace, remove, register, deregister,
	// unchanged.
	Action string `json:"action"`
	// Path is the file the action applies to.
	Path string `json:"path"`
	// Detail names the event for a register or deregister, and carries
	// the reason for an unchanged.
	Detail string `json:"detail,omitempty"`
}

// Options are the knobs both operations share.
type Options struct {
	// WtBinary is the wt the scripts call, recorded in their managed
	// block. Empty leaves them resolving wt from PATH.
	WtBinary string
	// Force writes over an event registered to somebody else's script,
	// and removes a script this package did not write.
	Force bool
	// DryRun computes every change and applies none.
	DryRun bool
	// Skill installs (or removes) the worktree-onboarding skill alongside
	// the hooks, so the notice the create hook prints in an unadopted
	// repository names something the reader can run from anywhere.
	Skill bool
}

// ConflictError is the refusal to write over something the user put there:
// an event registered to a script that is not ours, or a script file
// without the managed marker. --force is the way past it, which is what
// the message says.
type ConflictError struct {
	// Path is the file the conflict is in.
	Path string
	// What names the conflicting thing inside the file — an event name.
	// Empty when the file itself is the conflict.
	What string
	// Found is what is there now, as a phrase the message reads into.
	Found string
	// Flag is the flag that would overrule the refusal.
	Flag string
}

func (e *ConflictError) Error() string {
	if e.What == "" {
		return fmt.Sprintf("refusing to overwrite %s: it is %s — pass %s to replace it",
			e.Path, e.Found, e.Flag)
	}
	return fmt.Sprintf("refusing to overwrite %s in %s: it is %s — pass %s to replace it",
		e.What, e.Path, e.Found, e.Flag)
}

// Install writes both scripts and registers both events. It is idempotent:
// a second run over an unchanged installation reports every file
// unchanged and rewrites nothing.
func Install(l Layout, opts Options) ([]Change, error) {
	// Settings are edited last but validated first: a refusal there
	// should not leave scripts behind on the way to it.
	probe := opts
	probe.DryRun = true
	if _, err := updateSettings(l, probe, true); err != nil {
		return nil, err
	}

	var changes []Change

	for _, s := range scripts {
		path := l.ScriptPath(s.File)
		body := renderScript(s.Body, opts.WtBinary)
		existing, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			changes = append(changes, Change{Action: "write", Path: path})
		case err != nil:
			return nil, fmt.Errorf("reading %s: %w", path, err)
		case bytes.Equal(existing, body):
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: "already current"})
			continue
		case !ours(existing) && !opts.Force:
			return nil, &ConflictError{Path: path, Found: "a file wt did not write", Flag: "--force"}
		default:
			changes = append(changes, Change{Action: "replace", Path: path})
		}
		if opts.DryRun {
			continue
		}
		if err := os.MkdirAll(l.HooksDir(), 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", l.HooksDir(), err)
		}
		if err := platform.AtomicWrite(path, body, scriptMode); err != nil {
			return nil, err
		}
		// AtomicWrite renames a temp file created by CreateTemp, whose
		// mode the umask can narrow; a hook Claude Code cannot execute is
		// not a hook, so the mode is set explicitly afterwards.
		if err := os.Chmod(path, scriptMode); err != nil {
			return nil, fmt.Errorf("making %s executable: %w", path, err)
		}
	}

	if opts.Skill {
		skillChanges, err := installSkill(l, opts)
		if err != nil {
			return nil, err
		}
		changes = append(changes, skillChanges...)
	}

	settingsChanges, err := updateSettings(l, opts, true)
	if err != nil {
		return nil, err
	}
	return append(changes, settingsChanges...), nil
}

// Uninstall deregisters both events and removes both scripts. The log is
// left alone, and so is every other setting in settings.json.
func Uninstall(l Layout, opts Options) ([]Change, error) {
	settingsChanges, err := updateSettings(l, opts, false)
	if err != nil {
		return nil, err
	}
	changes := settingsChanges

	for _, s := range scripts {
		path := l.ScriptPath(s.File)
		existing, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: "not present"})
			continue
		case err != nil:
			return nil, fmt.Errorf("reading %s: %w", path, err)
		case !ours(existing) && !opts.Force:
			return nil, &ConflictError{Path: path, Found: "a file wt did not write", Flag: "--force"}
		}
		changes = append(changes, Change{Action: "remove", Path: path})
		if opts.DryRun {
			continue
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing %s: %w", path, err)
		}
	}

	if opts.Skill {
		skillChanges, err := uninstallSkill(l, opts)
		if err != nil {
			return nil, err
		}
		changes = append(changes, skillChanges...)
	}
	return changes, nil
}

// updateSettings merges the two event entries into settings.json, or drops
// them. Every other event and every other top-level setting survives:
// this reads the file, edits the two keys it owns, and writes the rest
// back as it found it.
func updateSettings(l Layout, opts Options, register bool) ([]Change, error) {
	path := l.SettingsPath()
	root, err := readSettings(path)
	if err != nil {
		return nil, err
	}

	hooks, err := mapAt(root, "hooks", path)
	if err != nil {
		return nil, err
	}

	var changes []Change
	dirty := false
	for _, s := range scripts {
		want := l.ScriptPath(s.File)
		found, recognised := registeredCommand(hooks[s.Event])
		// A registration is this tool's when it names the script this
		// layout would write, or one of the same name somewhere else —
		// which is what a moved ~/.claude leaves behind. Anything else
		// is the user's, and is refused rather than replaced.
		mine := recognised && (found == want || filepath.Base(found) == s.File)
		if hooks[s.Event] != nil && !mine && !opts.Force {
			return nil, &ConflictError{Path: path, What: s.Event, Found: describeFound(found), Flag: "--force"}
		}
		switch {
		case register && found == want:
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: s.Event + " already registered"})
		case register:
			hooks[s.Event] = entryFor(want)
			dirty = true
			changes = append(changes, Change{Action: "register", Path: path, Detail: s.Event})
		case hooks[s.Event] == nil:
			changes = append(changes, Change{Action: "unchanged", Path: path, Detail: s.Event + " not registered"})
		default:
			delete(hooks, s.Event)
			dirty = true
			changes = append(changes, Change{Action: "deregister", Path: path, Detail: s.Event})
		}
	}

	if !dirty || opts.DryRun {
		return changes, nil
	}

	// An empty hooks map is noise in a settings file that never had one.
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := platform.AtomicWrite(path, append(data, '\n'), settingsMode); err != nil {
		return nil, err
	}
	return changes, nil
}

// readSettings reads settings.json into a map. A missing file is an empty
// map; a file that does not parse refuses, because the alternative is
// replacing a settings file whose contents could not be read.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("reading %s: the file is not valid JSON: %v — fix or move it, then run this again", path, err)
	}
	return root, nil
}

// mapAt fetches a nested object, treating a missing key as an empty one
// and a key holding something other than an object as a refusal.
func mapAt(root map[string]any, key, path string) (map[string]any, error) {
	raw, ok := root[key]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reading %s: %q is not an object — fix it, then run this again", path, key)
	}
	return m, nil
}

// entryFor is the settings shape one event takes: a single matcher group
// holding a single command hook. WorktreeCreate and WorktreeRemove carry
// no matcher, so none is written.
func entryFor(command string) []any {
	return []any{
		map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		},
	}
}

// registeredCommand reads the single command out of one event's entry. It
// reports the command and whether the entry had the shape this package
// writes — anything richer (several groups, several hooks, a matcher, a
// non-command hook) is somebody else's, and is reported unrecognised so
// the caller refuses rather than flattening it.
func registeredCommand(raw any) (command string, recognised bool) {
	groups, ok := raw.([]any)
	if !ok || len(groups) != 1 {
		return "", false
	}
	group, ok := groups[0].(map[string]any)
	if !ok {
		return "", false
	}
	for k := range group {
		if k != "hooks" {
			return "", false
		}
	}
	entries, ok := group["hooks"].([]any)
	if !ok || len(entries) != 1 {
		return "", false
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		return "", false
	}
	if kind, _ := entry["type"].(string); kind != "command" {
		return "", false
	}
	cmd, ok := entry["command"].(string)
	if !ok {
		return "", false
	}
	return cmd, true
}

// describeFound names what is registered, for the refusal message.
func describeFound(command string) string {
	if command == "" {
		return "registered to something wt did not write"
	}
	return "registered to " + command + ", which wt did not write"
}

// renderScript appends the managed block recording the wt binary the
// script calls. The block is the convention every wt-written file follows
// (internal/managed), which is also how uninstall recognises its own
// files.
func renderScript(body []byte, wtBinary string) []byte {
	fields := map[string]string{}
	if wtBinary != "" {
		fields["wt"] = wtBinary
	}
	var b bytes.Buffer
	b.Write(bytes.TrimRight(body, "\n"))
	b.WriteString("\n\n")
	b.WriteString(managed.StartMarker)
	b.WriteString("\n")
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%s%s=%s\n", managed.FieldPrefix, name, fields[name])
	}
	b.WriteString(managed.EndMarker)
	b.WriteString("\n")
	return b.Bytes()
}

// ours reports whether a file carries the managed marker, which is what
// makes it one this package wrote.
func ours(data []byte) bool {
	return strings.Contains(string(data), managed.StartMarker)
}
