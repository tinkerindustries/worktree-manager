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
		h.doctorDriftFile(file, block, sp, band, findings)
	}
}

// doctorDriftFile compares one generated file's recorded fields against
// the current spec and band. Every mismatch is one finding naming the
// generated file and the field that moved — the two facts the phase-7
// exit criterion demands (plan.md §5 phase 7).
func (h *Handler) doctorDriftFile(file string, block *managed.Block, sp *spec.Spec, band map[string]int, findings *[]api.DoctorFinding) {
	current := artefact.FieldsFor(sp, band)

	// Fields the file records that the current spec cannot produce: the
	// field moved out of existence (a renamed resource, a descriptor
	// filename change, an app rename).
	for _, f := range block.Fields {
		want, known := current[f.Name]
		if !known {
			*findings = append(*findings, api.DoctorFinding{
				Level: "warning",
				Message: fmt.Sprintf("the generated file %s records field %q as %q, but the spec no longer produces that field — a resource was renamed or removed, or the app changed",
					file, f.Name, f.Value),
				Remedy: fmt.Sprintf("re-run the onboarding skill's generate phase to regenerate %s (hand edits outside the managed block survive)", file),
			})
			continue
		}
		if f.Value != want {
			*findings = append(*findings, api.DoctorFinding{
				Level: "warning",
				Message: fmt.Sprintf("the generated file %s records field %q as %q; it is now %q — the spec or the band ledger moved",
					file, f.Name, f.Value, want),
				Remedy: fmt.Sprintf("re-run the onboarding skill's generate phase to regenerate %s (hand edits outside the managed block survive)", file),
			})
		}
	}
	// Fields the current spec produces that the file does not record: the
	// file predates the field (a resource added since generation).
	for name, value := range current {
		if _, ok := block.Lookup(name); ok {
			continue
		}
		*findings = append(*findings, api.DoctorFinding{
			Level: "warning",
			Message: fmt.Sprintf("the generated file %s records no field %q (now %q) — the field was added after the file was generated",
				file, name, value),
			Remedy: fmt.Sprintf("re-run the onboarding skill's generate phase to regenerate %s (hand edits outside the managed block survive)", file),
		})
	}
}
