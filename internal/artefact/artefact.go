// Package artefact renders the phase-7 generated artefacts (M7,
// 07-agent-surface.md): the `worktree-create` and `worktree-remove`
// skills, the SessionStart tripwire, the opt-in PreToolUse guard hook,
// the `.claude/settings.json` entries, the reference doc and the
// CLAUDE.md tripwire. Every file is generated per adopted repo — the
// templates under templates/ are the stable instruction text, and the
// managed block each file closes with records the spec fields it came
// from (app, descriptor filename, resources, band bases, shared names).
//
// The block is the machine-owned half: regeneration replaces only it, so
// hand edits outside the block survive (plan.md §5 phase 7,
// 09-onboarding.md §5), and `wt doctor` compares the recorded fields
// against the current spec and band ledger, reporting the generated file
// and the field that moved. Anything a rendered file needs at runtime —
// the tripwire's descriptor filename, the skills' facts — is therefore
// read from the block, never baked into the body.
//
// This package is not a verb: the onboarding skill (a document) describes
// the same files, and the acceptance tests drive this renderer as the
// skill's generate phase. The client binary never imports it.
package artefact

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/managed"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// The embedded templates: the stable bodies of the generated files. The
// managed block is appended by Render.
//
//go:embed templates/skill-create.md
var skillCreateTemplate []byte

//go:embed templates/skill-remove.md
var skillRemoveTemplate []byte

//go:embed templates/hook-session-start.sh
var hookSessionStartTemplate []byte

//go:embed templates/hook-guard.sh
var hookGuardTemplate []byte

//go:embed templates/reference.md
var referenceTemplate []byte

// File is one generated artefact: a path relative to the repo root, the
// body written on a fresh file, the fields and content lines of the
// managed block, and the mode (executable hooks). Merge, when non-nil,
// replaces the whole-file semantics: the file is merged into the existing
// one rather than block-replaced (the JSON settings file, which carries no
// managed block).
type File struct {
	Path string
	// Body is the stable instruction text, written only when the file
	// does not exist yet.
	Body []byte
	// Fields are the machine-compared records of the managed block.
	Fields map[string]string
	// BlockContent are the human-readable fact lines inside the block.
	BlockContent []string
	// Mode, when non-zero, is applied on write (0755 for the hooks).
	Mode os.FileMode
	// Merge, when non-nil, merges the fresh content into the existing
	// file. It receives the existing bytes (nil when absent) and returns
	// the merged file.
	Merge func(existing []byte) ([]byte, error)
}

// Options are the render choices the skill makes during its policy phase.
type Options struct {
	// GuardHook declares the opt-in PreToolUse hook in settings.json —
	// the C6 enforcement choice, confirmed by the developer, never a
	// default.
	GuardHook bool
}

// paths of the generated artefacts, relative to the repo root. The skill
// and doctor share these through this package; the settings file is the
// one JSON artefact and carries no managed block.
const (
	SkillCreatePath = ".claude/skills/worktree-create/SKILL.md"
	SkillRemovePath = ".claude/skills/worktree-remove/SKILL.md"
	HookTripwire    = ".claude/hooks/wt-session-start.sh"
	HookGuard       = ".claude/hooks/wt-guard.sh"
	SettingsPath    = ".claude/settings.json"
	ReferencePath   = "docs/wt.md"
	CLAUDEKPath     = "CLAUDE.md"
)

// Render produces every generated artefact for a spec and its band. band
// maps port resource name to the band base the ledger holds — the value
// `wt bands list --json` reports, and the fact doctor compares against
// when a band moves. A port resource with no base is rendered with the
// fact stated as unregistered; the skill reserves before generating, so
// this is the honest rendering of a repo whose band is not on this
// machine.
func Render(sp *spec.Spec, band map[string]int, opts Options) ([]File, error) {
	fields := FieldsFor(sp, band)

	files := []File{
		{
			Path:   SkillCreatePath,
			Body:   bytes.TrimSpace(skillCreateTemplate),
			Fields: fields,
		},
		{
			Path:   SkillRemovePath,
			Body:   bytes.TrimSpace(skillRemoveTemplate),
			Fields: fields,
		},
		{
			Path:   HookTripwire,
			Body:   bytes.TrimSpace(hookSessionStartTemplate),
			Fields: fields,
			Mode:   0o755,
		},
		{
			Path:   HookGuard,
			Body:   bytes.TrimSpace(hookGuardTemplate),
			Fields: fields,
			Mode:   0o755,
		},
		{
			Path:         ReferencePath,
			Body:         bytes.TrimSpace(referenceTemplate),
			Fields:       fields,
			BlockContent: factSheet(sp, band),
		},
		{
			Path:         CLAUDEKPath,
			Body:         []byte("# CLAUDE.md\n\nThis repository's working instructions. The tripwire below is generated: it is replaced on regeneration, and everything above it is this repository's own."),
			Fields:       fields,
			BlockContent: tripwireLines(),
		},
	}

	settings, err := SettingsJSON(opts.GuardHook)
	if err != nil {
		return nil, err
	}
	files = append(files, File{
		Path:  SettingsPath,
		Body:  settings,
		Merge: mergeSettings(settings),
	})
	return files, nil
}

// FieldsFor records the spec fields a generated file came from: the
// machine-compared provenance `wt doctor` checks (09-onboarding.md §5).
// The coordinator's drift check uses the same function, so the recorded
// field set and the compared field set cannot disagree.
func FieldsFor(sp *spec.Spec, band map[string]int) map[string]string {
	fields := map[string]string{
		"app":        sp.App,
		"descriptor": sp.Emit.Descriptor.Filename,
	}
	names := make([]string, 0, len(sp.Resources))
	for i := range sp.Resources {
		names = append(names, sp.Resources[i].Name)
	}
	fields["resources"] = strings.Join(names, ", ")
	portNames := make([]string, 0)
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Type == "port" {
			portNames = append(portNames, r.Name)
		}
	}
	sort.Strings(portNames)
	for _, name := range portNames {
		fields["band "+name] = fmt.Sprintf("%d", band[name])
	}
	fields["shared"] = strings.Join(sharedNames(sp), ", ")
	return fields
}

// sharedNames lists the shared block's names: the hand-authored entries
// plus every resource with default: shared, which contributes itself to
// the descriptor's shared block (03-drivers.md §6).
func sharedNames(sp *spec.Spec) []string {
	names := make([]string, 0, len(sp.Shared)+len(sp.Resources))
	for _, s := range sp.Shared {
		names = append(names, s.Name)
	}
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Type == "state-path" && r.Default != nil && *r.Default == "shared" {
			names = append(names, r.Name)
		}
	}
	return names
}

// factSheet is the reference doc's block content: the repo's facts as
// recorded at generation time.
func factSheet(sp *spec.Spec, band map[string]int) []string {
	lines := []string{
		"This repository's facts, recorded when these artefacts were generated:",
		fmt.Sprintf("- descriptor: %s (%s)", sp.Emit.Descriptor.Filename, sp.Emit.Descriptor.Format),
	}
	for i := range sp.Resources {
		r := &sp.Resources[i]
		switch r.Type {
		case "port":
			base, ok := band[r.Name]
			if !ok {
				lines = append(lines, fmt.Sprintf("- %s: port, band base unregistered on this machine — reserve one with 'wt bands reserve'", r.Name))
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: port, band base %d", r.Name, base))
		case "state-path":
			mode := "isolated"
			if r.Default != nil && *r.Default == "shared" {
				mode = "shared by default"
			} else if r.Seed != nil && r.Seed.Default != nil {
				mode = "seeded mode " + *r.Seed.Default
			}
			lines = append(lines, fmt.Sprintf("- %s: state-path, %s", r.Name, mode))
		case "namespace":
			lines = append(lines, fmt.Sprintf("- %s: namespace (%s)", r.Name, namespaceKind(r)))
		default:
			lines = append(lines, fmt.Sprintf("- %s: %s", r.Name, r.Type))
		}
	}
	lines = append(lines, "- shared: "+strings.Join(sharedNames(sp), ", "))
	if h := startHookRun(sp); h != "" {
		lines = append(lines, "- manual start (the spec's start hook): "+h)
	}
	return lines
}

// startHookRun is the spec's start hook command, or "" when the spec has
// none.
func startHookRun(sp *spec.Spec) string {
	if sp.Hooks.Start != nil && sp.Hooks.Start.Run != "" {
		return sp.Hooks.Start.Run
	}
	return ""
}

// namespaceKind applies the kind default (compose).
func namespaceKind(r *spec.Resource) string {
	if r.Kind != nil {
		return *r.Kind
	}
	return spec.DefaultNamespaceKind
}

// tripwireLines is the CLAUDE.md tripwire's block content: the three facts
// A17's bar for inclusion demands — this repo uses per-worktree
// environments; never hardcode a port or path, read them with wt show;
// something else creates the worktree, wt init attaches to it.
func tripwireLines() []string {
	return []string{
		"This repository uses per-worktree environments.",
		"- Never hardcode a port or a path: read them with `wt show`.",
		"- Something else creates the worktree; `wt init` attaches to it.",
		"See docs/wt.md for the full reference.",
	}
}

// SettingsJSON renders .claude/settings.json: the SessionStart entry
// (always — the tripwire is part of the adopted surface, and the phase-7
// decision is that it belongs in the repo's committed settings, because
// committing a one-sentence notice is not committing a policy) and, when
// guard is true, the opt-in PreToolUse entry pointing at the guard hook.
// The guard entry is added only on the developer's confirmation during
// the skill's policy phase (C6); a clone that removes it loses the
// enforcement, never the notice.
func SettingsJSON(guard bool) ([]byte, error) {
	hooks := map[string]any{
		"SessionStart": []any{
			map[string]any{
				"matcher": "",
				"hooks": []any{
					map[string]any{"type": "command", "command": HookTripwire},
				},
			},
		},
	}
	if guard {
		hooks["PreToolUse"] = []any{
			map[string]any{
				"matcher": "Edit|Write|MultiEdit",
				"hooks": []any{
					map[string]any{"type": "command", "command": HookGuard},
				},
			},
		}
	}
	data, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering .claude/settings.json: %w", err)
	}
	return append(data, '\n'), nil
}

// mergeSettings merges the generated hooks entries into an existing
// .claude/settings.json, preserving every other setting the repository or
// the user added. An existing file that is not valid JSON refuses the
// merge — never clobbered.
func mergeSettings(fresh []byte) func(existing []byte) ([]byte, error) {
	return func(existing []byte) ([]byte, error) {
		if len(bytes.TrimSpace(existing)) == 0 {
			return fresh, nil
		}
		var root map[string]any
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("merging the hook entries into .claude/settings.json: the existing file is not valid JSON: %v — fix or remove it, then re-run the skill's generate phase", err)
		}
		var freshRoot map[string]any
		if err := json.Unmarshal(fresh, &freshRoot); err != nil {
			return nil, fmt.Errorf("rendering .claude/settings.json: %v", err)
		}
		root["hooks"] = freshRoot["hooks"]
		data, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("merging .claude/settings.json: %w", err)
		}
		return append(data, '\n'), nil
	}
}

// Apply writes the generated files into root. An existing file has only
// its managed block replaced — hand edits outside the block survive — and
// a file without markers gets the block appended (the CLAUDE.md tripwire
// joining a repo's own text). A missing file is written whole. Files with
// a Merge handler are merged instead of block-replaced (settings.json).
func Apply(root string, files []File) error {
	for _, f := range files {
		path := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
		}
		if f.Merge != nil {
			existing, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("reading %s: %w", path, err)
			}
			out, merr := f.Merge(existing)
			if merr != nil {
				return merr
			}
			if err := writeFile(path, out, f.Mode); err != nil {
				return err
			}
			continue
		}
		existing, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			content := append(append([]byte{}, f.Body...), '\n', '\n')
			block := managed.Render(f.Fields, f.BlockContent)
			content = append(content, []byte(strings.Join(block, "\n")+"\n")...)
			if err := writeFile(path, content, f.Mode); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		out, rerr := managed.Replace(existing, f.Fields, f.BlockContent)
		if rerr != nil {
			return fmt.Errorf("%s: %w", f.Path, rerr)
		}
		if err := writeFile(path, out, f.Mode); err != nil {
			return err
		}
	}
	return nil
}

// writeFile writes content with the given mode, preserving an existing
// file's mode when Mode is zero.
func writeFile(path string, content []byte, mode os.FileMode) error {
	perm := os.FileMode(0o644)
	if mode != 0 {
		perm = mode
	} else if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return os.WriteFile(path, content, perm)
}

// sortFiles orders the generated files for listing and tests.
func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}
