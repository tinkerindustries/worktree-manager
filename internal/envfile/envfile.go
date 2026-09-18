// Package envfile is the .env delivery channel (05-delivery.md §3): the
// wt-managed block inside a repo's .env, replaced on every run so hand
// edits above and below survive.
//
// Three behaviours are load-bearing here, all from 05-delivery.md §3 and
// §8:
//
//   - A re-run replaces only the managed block. The block is marked, and a
//     re-run swaps the marked lines and nothing else.
//   - Duplicates are stripped, not ordered. Any definition of a managed key
//     found outside the block is removed rather than left to be shadowed,
//     and the report says which keys were stripped. Ordering cannot be made
//     correct: dotenv parsers disagree about which duplicate wins (one
//     keeps the first, docker compose keeps the last), so two definitions
//     is a bug in both directions.
//   - A first write seeds the new worktree's .env from the main checkout's,
//     unmanaged content only — tokens and API credentials carry over, but
//     managed keys come from this worktree's allocation, never from slot 0.
//     A missing source file seeds nothing and is not an error.
//
// Unbalanced or nested managed markers refuse the write, naming the line
// numbers (05-delivery.md §8).
package envfile

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/managed"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// StartMarker and EndMarker delimit the wt-managed block. The block is the
// only thing a re-run touches; anything above or below survives. They are
// internal/managed's markers: one convention for every wt-managed block in
// every file the tool writes, declared once so a change to one cannot
// strand the files the other wrote.
const (
	StartMarker = managed.StartMarker
	EndMarker   = managed.EndMarker
)

// Report is what an Update did, so the caller can say so: whether anything
// was written, whether a first write seeded from the main checkout (and
// from where), why seeding did not happen when it did not, and which
// managed keys were stripped from outside the block. Bounded coverage is
// stated, never silent (plan.md §3).
type Report struct {
	Path        string   `json:"path"`
	Wrote       bool     `json:"wrote"`
	SeededFrom  string   `json:"seeded_from,omitempty"`
	SeedSkipped string   `json:"seed_skipped,omitempty"`
	Stripped    []string `json:"stripped,omitempty"`
}

// MarkersError is the refusal to write when the managed markers are
// unbalanced or nested (05-delivery.md §8): it names the offending line
// numbers. It is internal/managed's refusal, with the .env's own wording
// carried in the Action field.
type MarkersError = managed.MarkersError

// Update rewrites path's wt-managed block. env is the spec's emit.env
// section: the file's keys (each a template over the same variables as the
// resources) and whether a first write seeds from the main checkout. ctx
// and resolved are the allocation this worktree owns — never the main
// checkout's, which is slot 0 and would hand the new worktree its ports.
// seedFrom is the main checkout's .env path, reached through the
// classification (identity.Classification.MainCheckoutPath) — the one
// legitimate consumer of that path (05-delivery.md §3.3); it is used only
// when the target file does not exist yet and env.Seed is set.
func Update(path string, env *spec.EnvEmit, ctx spec.Context, resolved map[string]spec.Resolved, seedFrom string) (Report, error) {
	rep := Report{Path: path}

	// The content: the existing file, or — on a first write, when seeding
	// is on — the main checkout's, unmanaged content only. A missing
	// source seeds nothing and is not an error.
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		if env.Seed && seedFrom != "" {
			seed, serr := os.ReadFile(seedFrom)
			if serr == nil {
				content = seed
				rep.SeededFrom = seedFrom
			} else if !os.IsNotExist(serr) {
				return rep, fmt.Errorf("seeding %s from %s: %w", path, seedFrom, serr)
			} else {
				rep.SeedSkipped = fmt.Sprintf("the main checkout's .env at %s does not exist; nothing was seeded", seedFrom)
			}
		} else if env.Seed {
			rep.SeedSkipped = "no main checkout .env path supplied; nothing was seeded"
		}
	default:
		return rep, fmt.Errorf("reading %s: %w", path, err)
	}

	lines := managed.SplitLines(content)
	starts, ends := managed.FindMarkers(lines)
	if !managed.ValidMarkers(starts, ends) {
		return rep, &MarkersError{StartLines: starts, EndLines: ends, Action: "write the .env"}
	}

	// The block: replace it in place so hand edits above and below
	// survive. 1-based lines; strip them.
	kept := lines
	if len(starts) == 1 && len(ends) == 1 {
		kept = append(lines[:starts[0]-1], lines[ends[0]:]...)
	}

	// Strip every definition of a managed key found outside the block —
	// removed, not shadowed (05-delivery.md §3.2). The report names the
	// keys.
	strippedSet := map[string]bool{}
	var out []string
	for _, line := range kept {
		if key := managedKeyDef(line, env.Keys); key != "" {
			strippedSet[key] = true
			continue
		}
		out = append(out, line)
	}
	stripped := make([]string, 0, len(strippedSet))
	for k := range strippedSet {
		stripped = append(stripped, k)
	}
	sort.Strings(stripped)
	rep.Stripped = stripped

	// The fresh block, from this worktree's allocation: every key, in a
	// deterministic order, each value the resolved template.
	names := make([]string, 0, len(env.Keys))
	for name := range env.Keys {
		names = append(names, name)
	}
	sort.Strings(names)
	block := make([]string, 0, len(names)+2)
	block = append(block, StartMarker)
	for _, name := range names {
		value, err := spec.Substitute(env.Keys[name], ctx, resolved)
		if err != nil {
			return rep, fmt.Errorf("resolving the managed key %s: %w", name, err)
		}
		block = append(block, name+"="+value)
	}
	block = append(block, EndMarker)

	// Join: the surviving lines, then the block. The block goes last for a
	// fresh file; on a re-run it sits where the old block sat.
	joined := append(out, block...)
	data := []byte(strings.Join(joined, "\n"))
	if len(joined) > 0 {
		data = append(data, '\n')
	}

	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := platform.AtomicWrite(path, data, perm); err != nil {
		return rep, fmt.Errorf("writing %s: %w", path, err)
	}
	rep.Wrote = true
	return rep, nil
}

// OutsideBlockKeys is the read-only half of the duplicate strip: which
// managed keys are defined outside the managed block. Phase 6's doctor uses
// it to report the drift before the next init strips it (D6, 05-delivery.md
// §3.2). It changes nothing; a missing file has no keys outside any block.
func OutsideBlockKeys(path string, keys map[string]string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	lines := managed.SplitLines(content)
	starts, ends := managed.FindMarkers(lines)
	kept := lines
	if len(starts) == 1 && len(ends) == 1 {
		kept = append(lines[:starts[0]-1], lines[ends[0]:]...)
	}
	set := map[string]bool{}
	var out []string
	for _, line := range kept {
		if key := managedKeyDef(line, keys); key != "" && !set[key] {
			set[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, nil
}

// managedKeyDef reports which managed key the line defines, if any: an
// assignment of the key (with optional leading whitespace and an optional
// `export` prefix, the two spellings dotenv files actually use). A
// commented-out definition is a comment, not a definition.
func managedKeyDef(line string, keys map[string]string) string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "export ")
	trimmed = strings.TrimSpace(trimmed)
	for name := range keys {
		if strings.HasPrefix(trimmed, name) {
			rest := trimmed[len(name):]
			if strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, " =") {
				return name
			}
		}
	}
	return ""
}
