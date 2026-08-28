package coord

// drift.go is the phase-7 half of doctor: the generated-artefact drift
// check (09-onboarding.md §5, plan.md §5 phase 7). Every file the
// onboarding skill generates carries a managed block recording the spec
// fields it came from; doctor compares the recorded fields against the
// current spec and the band ledger and reports a mismatch naming the
// generated file and the field that moved — a moved port band or a renamed
// resource becomes a finding rather than a stale sentence in a skill
// nobody reads closely.
//
// The scan is a convention, not inference: any tracked file whose content
// carries the managed markers is a generated file, and its block is
// parsed. The bound is stated: the scan reads the repo's tracked files up
// to a cap and reports the cap when it trips (a silent degrade reads as
// success, plan.md §3).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/artefact"
	"github.com/mrgeoffrich/worktree-manager/internal/managed"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Drift scan bounds, stated when tripped: how many tracked files are
// scanned per repo and how large a file may be to be read. A generated
// artefact is a skill, a hook or a doc — small files — so the caps only
// trip on a repository that is not the scan's target, and the note says
// so.
const (
	driftScanCap = 10000
	driftFileCap = 1 << 20 // 1 MiB
)

// doctorDrift runs the generated-artefact drift check for one repository,
// given its main checkout path and the current band ledger. The spec is
// found by the walk-up rule; a repo with no spec has nothing to compare
// and is skipped.
func (h *Handler) doctorDrift(main string, bands store.BandsFile, findings *[]api.DoctorFinding, notes *[]string) {
	specPath, err := spec.FindSpecPath(main)
	if err != nil {
		return // not adopted: nothing generated can be honest, and nothing is
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		*notes = append(*notes, fmt.Sprintf("the generated-artefact drift check could not read %s: %v", specPath, err))
		return
	}
	sp, err := spec.Parse(data)
	if err != nil {
		*notes = append(*notes, fmt.Sprintf("the generated-artefact drift check could not parse %s: %v", specPath, err))
		return
	}
	if err := spec.Validate(sp); err != nil {
		*notes = append(*notes, fmt.Sprintf("the generated-artefact drift check skipped %s: the spec does not validate: %v", specPath, err))
		return
	}
	band := map[string]int{}
	if b := findBand(bands, sp.App); b != nil {
		band = b.Bases
	}

	out, err := gitOut("-C", main, "ls-files", "-z")
	if err != nil {
		*notes = append(*notes, fmt.Sprintf("the generated-artefact drift check could not list %s's files: %v", main, err))
		return
	}
	scanned := 0
	nested, quoted := 0, 0
	for _, file := range strings.Split(string(out), "\x00") {
		if file == "" {
			continue
		}
		scanned++
		if scanned > driftScanCap {
			*notes = append(*notes, fmt.Sprintf(
				"the generated-artefact drift check stopped after %d tracked files of %s (the cap); a generated file past the cap is not checked — the artefact paths are .claude/skills, .claude/hooks, docs/wt.md and CLAUDE.md", driftScanCap, main))
			break
		}
		full := filepath.Join(main, filepath.FromSlash(file))
		fi, serr := os.Stat(full)
		if serr != nil || !fi.Mode().IsRegular() || fi.Size() > driftFileCap {
			continue
		}
		content, rerr := os.ReadFile(full)
		if rerr != nil {
			continue
		}
		block, ok, perr := managed.Parse(content)
		if perr != nil {
			*notes = append(*notes, fmt.Sprintf("%s: unreadable managed markers: %v", file, perr))
			continue
		}
		if !ok || len(block.Fields) == 0 {
			continue
		}
		// The spec that governs the file is the one the walk-up rule finds
		// from the file's own directory, which is not always the repo's:
		// a repository can hold another adopted repository's tree — a test
		// fixture with its own committed wt.yaml is the usual case. Those
		// files are that spec's artefacts, and comparing them against this
		// one reports every field of every one of them as moved.
		if governing, gerr := spec.FindSpecPath(filepath.Dir(full)); gerr != nil || governing != specPath {
			nested++
			continue
		}
		// A generated file's block closes the file: managed.Replace either
		// appends it or replaces it where it was appended. A marker pair
		// with more of the file after it is the convention being quoted —
		// the onboarding skill's own reference documents the block by
		// showing one — and its recorded values are the documentation's
		// placeholders, not this repo's allocations.
		if !blockClosesFile(content) {
			quoted++
			continue
		}
		h.doctorDriftFile(main, file, block, sp, band, findings)
	}
	// Both bounds are stated rather than silently applied: a file this scan
	// declined to compare is a file whose drift nobody is checking.
	if nested > 0 {
		*notes = append(*notes, fmt.Sprintf(
			"the generated-artefact drift check skipped %d tracked file(s) of %s governed by a nested wt.yaml; they are that repository's artefacts, checked when doctor scans it", nested, main))
	}
	if quoted > 0 {
		*notes = append(*notes, fmt.Sprintf(
			"the generated-artefact drift check skipped %d tracked file(s) of %s whose managed markers do not close the file; a block with more of the file after it is the convention being quoted, not a generated block", quoted, main))
	}
}

// blockClosesFile reports whether the file's managed block is the last
// thing in it, blank lines aside — the invariant every file wt writes
// holds, and the one that separates a generated block from a quoted
// example of one.
func blockClosesFile(content []byte) bool {
	lines := managed.SplitLines(content)
	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
			break
		}
	}
	return last >= 0 && lines[last] == managed.EndMarker
}

// doctorDriftFile compares one generated file's recorded fields against
// the current spec and band. One file is one finding, naming the generated
// file, with the field that moved in the details — the two facts the
// phase-7 exit criterion demands (plan.md §5 phase 7). A file whose whole
// block predates a rename moves every field it records at once, and six
// findings carrying one remedy between them is the same problem told six
// times.
func (h *Handler) doctorDriftFile(main, file string, block *managed.Block, sp *spec.Spec, band map[string]int, findings *[]api.DoctorFinding) {
	current := artefact.FieldsFor(sp, band)
	var details []string

	// Fields the file records that the current spec cannot produce: the
	// field moved out of existence (a renamed resource, a descriptor
	// filename change, an app rename).
	for _, f := range block.Fields {
		want, known := current[f.Name]
		if !known {
			details = append(details, fmt.Sprintf(
				"%s: recorded as %q; the spec no longer produces this field — a resource was renamed or removed, or the app changed", f.Name, f.Value))
			continue
		}
		if f.Value != want {
			details = append(details, fmt.Sprintf(
				"%s: recorded as %q; it is now %q", f.Name, f.Value, want))
		}
	}
	// Fields the current spec produces that the file does not record: the
	// file predates the field (a resource added since generation).
	missing := make([]string, 0, len(current))
	for name := range current {
		if _, ok := block.Lookup(name); !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		details = append(details, fmt.Sprintf(
			"%s: not recorded; it is now %q — the field was added after the file was generated", name, current[name]))
	}

	if len(details) == 0 {
		return
	}
	moved := "1 field has moved"
	if len(details) > 1 {
		moved = fmt.Sprintf("%d fields have moved", len(details))
	}
	*findings = append(*findings, api.DoctorFinding{
		Kind:    "generated-file-drift",
		App:     sp.App,
		Level:   "warning",
		Summary: fmt.Sprintf("%s is out of date (%s)", file, moved),
		Message: fmt.Sprintf("the generated file %s records spec fields that no longer match the spec or the band ledger; %s since it was generated", file, moved),
		Remedy:  fmt.Sprintf("re-run the onboarding skill's generate phase to regenerate %s (hand edits outside the managed block survive)", file),
		Repo:    main,
		Path:    filepath.Join(main, filepath.FromSlash(file)),
		Details: details,
	})
}
