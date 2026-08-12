package artefact

// briefing.go renders the briefing block of 07-agent-surface.md §5: the
// paste-into-a-session block the create skill's report produces. Every
// value is read from `wt show --json` output — never guessed (A11, the
// rule a briefing that says "the UI is on 3100" is correct once exists to
// enforce). When the descriptor is missing, the briefing refuses to
// render: a briefing with guessed values is worse than none
// (07-agent-surface.md §10).

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// showVerdict is the shape of `wt show --json` the briefing reads: the
// found/not-found verdict with the descriptor on success.
type showVerdict struct {
	Found        bool                   `json:"found"`
	Reason       string                 `json:"reason,omitempty"`
	Outcome      string                 `json:"outcome,omitempty"`
	WorktreeRoot string                 `json:"worktree_root,omitempty"`
	Descriptor   *descriptor.Descriptor `json:"descriptor,omitempty"`
}

// Briefing renders the briefing block from `wt show --json` output. It
// refuses — with the show verdict's own reason and the remedy — when the
// descriptor is missing, which is exactly the un-initialised worktree
// case: no values to read, and a briefing that guessed them would be
// believed.
func Briefing(sp *spec.Spec, showJSON []byte) (string, error) {
	var v showVerdict
	if err := json.Unmarshal(showJSON, &v); err != nil {
		return "", fmt.Errorf("the briefing reads `wt show --json` output; this is not one: %v", err)
	}
	if !v.Found {
		reason := v.Reason
		if reason == "" {
			reason = "no descriptor"
		}
		return "", fmt.Errorf("refusing to render the briefing: %s — run 'wt init' in this worktree first, then re-render (a briefing with guessed values is worse than none)", reason)
	}
	d := v.Descriptor
	if d == nil {
		return "", fmt.Errorf("refusing to render the briefing: `wt show --json` reported found but carried no descriptor — upgrade wt, then re-run")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## This environment — %s/%s (slot %d)\n\n", d.App, d.Slug, d.Slot)
	fmt.Fprintf(&b, "- cwd: %s\n", v.WorktreeRoot)

	// What is isolated, with values.
	fmt.Fprintf(&b, "\nIsolated (this worktree's own):\n")
	names := make([]string, 0, len(d.Resources))
	for name := range d.Resources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := d.Resources[name]
		fmt.Fprintf(&b, "- %s (%s): %v\n", name, r.Type, r.Value)
	}

	// What is shared, with the blast radius of each.
	if len(d.Shared) > 0 {
		fmt.Fprintf(&b, "\nShared — writes escape this worktree:\n")
		for _, s := range d.Shared {
			impact := s.Impact
			if impact == "" {
				impact = "shared with every worktree"
			}
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, impact)
		}
	}

	// The snapshot-versus-live caveat where a state path was seeded: a
	// seeded store is a snapshot taken at creation that diverges as work
	// continues (B6.2, 03-drivers.md §4.4).
	fmt.Fprintf(&b, "\nEvery Read, Edit and Write in this session must start with the path prefix: %s\n", d.Path)
	descPath := filepath.Join(d.Path, sp.Emit.Descriptor.Filename)
	fmt.Fprintf(&b, "- reach this environment from outside: --env %s\n", descPath)
	if d.Description != "" {
		fmt.Fprintf(&b, "- description: %s\n", d.Description)
	}
	fmt.Fprintf(&b, "\nNot for this session: 'wt init' (already attached), 'wt rm' (teardown), and any command that writes a shared resource above.\n")
	fmt.Fprintf(&b, "\nWhich binary is which: the worktree's own build is for smoke-testing; the system `wt` on PATH is for every close-out and bookkeeping call.\n")
	return b.String(), nil
}
