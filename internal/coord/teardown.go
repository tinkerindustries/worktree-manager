package coord

// teardown.go is the coordinator's teardown core: the entry lifecycle behind
// the driver sequencing. Phase 5's rm sequences it into the release verb;
// this phase proves the state transitions it drives (plan.md §5, phase 4).

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Teardown runs the teardown of one registry entry: every driver's teardown
// in reverse dependency order over the entry's denormalised resources —
// handles from the registry, never from the working tree (03-drivers.md
// §2.2) — continuing past failures and collecting what survived. The entry
// lifecycle is the B2.3 rail: nothing survives → the entry is dropped and
// the slot frees; anything survives → the entry moves to tearing-down with
// a note listing exactly what survived, and the slot stays held until a
// re-run frees everything.
//
// The spec is required: the dependent projects and the purge refusal are
// computed from it, and a teardown that could not run them would be a
// partial honour of a destructive operation — refused instead (plan.md §3).
// purgeFlags are the CLI purge flags the caller passed; a state-path
// resource whose purge.flag was passed is purged, every other state-path is
// left alone.
func (h *Handler) Teardown(s *Session, ref api.EntryRef, sp *spec.Spec, purgeFlags []string) *api.Response {
	if sp == nil {
		return respErr(3, "teardown needs the spec: without it the dependent projects and the purge refusal cannot be computed",
			"send the spec with the teardown, then re-run")
	}
	if err := spec.Validate(sp); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the teardown is refused whole: %v", err),
			"fix the spec, then re-run the teardown")
	}
	if h.Drivers == nil {
		return respErr(1, "the coordinator has no driver registry installed",
			"restart wtd, then re-run the teardown")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	e, ok, err := h.st.GetEntry(ref.App, ref.Slug)
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	if !ok {
		return respErr(1, fmt.Sprintf("no registry entry for app %q slug %q", ref.App, ref.Slug),
			"allocate the worktree first, then re-run")
	}
	if perr := h.checkOwner(s, e); perr != nil {
		return &api.Response{Error: perr}
	}
	return h.teardownEntry(s, e, sp, purgeFlags, nil)
}

// teardownEntry runs the teardown of one looked-up, ownership-checked
// entry. The caller holds h.mu. Phase 5's rm verb calls it after the reap;
// Teardown is the same path through the verb-shaped front door. keepFlags
// are the CLI keep flags the caller passed (e.g. "--keep-vm"); a machine
// resource whose keep_flag was passed is left up, and the note names the
// manual teardown command.
func (h *Handler) teardownEntry(s *Session, e *store.Entry, sp *spec.Spec, purgeFlags, keepFlags []string) *api.Response {
	env, perr := h.entryEnv(e, sp)
	if perr != nil {
		return &api.Response{Error: perr}
	}
	app, slug := e.App, e.Slug

	// The drivers run with the mutex released: a compose teardown takes as
	// long as docker takes, and every other client would wait behind it.
	// The entry's handles come from the registry copy read before the
	// claim, which is what a teardown works from by design (03-drivers.md
	// §2.2), and the outcome is recorded against a fresh read below.
	var rep driver.TeardownReport
	if cerr := h.runUnlocked(app, slug, func() {
		rep = h.Drivers.TeardownAll(sp, e.Resources, env, purgeFlags, keepFlags)
	}); cerr != nil {
		return &api.Response{Error: cerr}
	}

	_, ok, err := h.st.GetEntry(app, slug)
	if err != nil {
		return h.storeErr("reading the registry", err)
	}

	if rep.Clean() {
		// Nothing survived: the entry drops and the slot frees. A machine
		// kept by --keep-vm is a deliberate survivor — the note says so,
		// naming the documented bypass, so the leftover is never silent
		// (B4.3).
		notes := h.keptMachineNotes(sp, e, keepFlags)
		if !ok {
			return respErr(1, fmt.Sprintf("no registry entry for app %q slug %q", app, slug),
				"the entry went away mid-teardown; re-run 'wt list' to see the current state")
		}
		if err := h.st.DeleteEntry(app, slug); err != nil {
			return h.storeErr("writing the registry", err)
		}
		return &api.Response{Result: mustJSON(api.ReleaseResult{
			App: app, Slug: slug, Removed: true, Notes: notes,
		})}
	}

	// Something survived: tearing-down is a resting state, the note lists
	// exactly what survived, and the slot stays held (B2.3, ARCHITECTURE.md
	// §11.2). The state change, the note and the last-seen move land as one
	// transaction — a tearing-down entry without its note would leave what
	// survived unnamed.
	note := teardownNote(rep)
	seen := time.Now()
	e.State, e.TeardownNote, e.LastSeen = store.StateTearingDown, note, seen.UTC().Format(time.RFC3339Nano)
	if ok {
		if err := h.st.WithTx(func(tx *store.Tx) error {
			if err := tx.UpdateEntryState(app, slug, store.StateTearingDown, note); err != nil {
				return err
			}
			return tx.TouchEntry(app, slug, seen)
		}); err != nil {
			return h.storeErr("writing the registry", err)
		}
	}
	code, msg, remedy := teardownFailure(rep)
	return respErr(code, msg, remedy)
}

// docker returns the handler's docker seam, defaulting to the real runner.
func (h *Handler) docker() driver.Docker {
	if h.Docker != nil {
		return h.Docker
	}
	return driver.NewDocker()
}

// machine returns the handler's VM runner seam, defaulting to the
// platform's real runner.
func (h *Handler) machine() platform.MachineRunner {
	if h.Machine != nil {
		return h.Machine
	}
	return platform.Machine()
}

// entryEnv builds the driver environment for one entry: everything an
// operation needs beyond the resolved value, read fresh from the store at
// call time — the ledger's bases and reservations, the coordinator's home,
// the docker seam, and the machine log directory (<store root>/logs) a
// starting VM's output is written into (1b) — the store's own root is
// the one place already private on both platforms, so the machine driver
// need not invent a second permission model for a directory that carries
// nothing secret today. It is the shared construction behind teardown,
// materialise and the rm verb, so the three cannot drift apart on what an
// operation may see (03-drivers.md §2).
func (h *Handler) entryEnv(e *store.Entry, sp *spec.Spec) (driver.Env, *api.Error) {
	bands, err := h.st.ReadBands()
	if err != nil {
		return driver.Env{}, &api.Error{
			Code: 1, Msg: fmt.Sprintf("reading the band ledger: %v", err),
			Remedy: "check the coordinator's store (WT_HOME) is readable and writable, then re-run",
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return driver.Env{}, &api.Error{
			Code:   4,
			Msg:    fmt.Sprintf("the coordinator cannot determine the home directory for {home}: %v", err),
			Remedy: "set $HOME for the coordinator, then re-run",
		}
	}
	env := driver.Env{
		Spec: sp, App: e.App, Slug: e.Slug, Slot: e.Slot,
		Home: home, Worktree: e.Path,
		Resolved:      e.Resources,
		Docker:        h.docker(),
		Machine:       h.machine(),
		MachineLogDir: filepath.Join(h.st.Root(), "logs"),
	}
	if band := findBand(bands, e.App); band != nil {
		env.Bases = band.Bases
	}
	env.Reservations = bands.Reservations
	return env, nil
}

// teardownNote composes the entry's note: exactly what survived, in the
// report's own words, so a later reader needs no re-derivation.
func teardownNote(rep driver.TeardownReport) string {
	if rep.Clean() {
		return ""
	}
	return "teardown left resources behind; the slot stays held until a re-run frees everything: " + rep.Summary()
}

// teardownFailure maps a report with refusals, unavailables or survivors to
// the wire error: a refusal is code 3 (a safety check refused the
// operation), an unavailable teardown is code 4 (required context
// unavailable), a partial failure is code 1. The message names everything
// outstanding and the remedy tells the user to fix the cause and re-run.
func teardownFailure(rep driver.TeardownReport) (int, string, string) {
	code := 1
	if len(rep.Refusals) > 0 {
		code = 3 // safety first: a refused teardown outranks a failed one
	} else if len(rep.Unavailable) > 0 {
		code = 4
	}
	return code,
		fmt.Sprintf("teardown did not free the slot: %s", rep.Summary()),
		"fix the cause named above, then re-run the teardown; the slot stays held until nothing survives"
}

// keptMachineNotes returns the bounded-coverage notes for machine
// resources the caller kept with their keep flag: the entry drops but the
// VM stays up, and the note names the documented bypass — the exact
// command the driver supplies — so the leftover is never silent (B4.3,
// 03-drivers.md §4.5). The driver's teardown already did nothing for these
// resources; this is the note the report owes. The command comes from the
// runner the teardown itself used (h.machine), so the note cannot drift
// from the driver's own teardown.
func (h *Handler) keptMachineNotes(sp *spec.Spec, e *store.Entry, keepFlags []string) []string {
	var notes []string
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Type != "machine" || r.KeepFlag == nil {
			continue
		}
		kept := false
		for _, f := range keepFlags {
			if f == *r.KeepFlag {
				kept = true
				break
			}
		}
		if !kept {
			continue
		}
		v, ok := e.Resources[r.Name]
		if !ok {
			continue
		}
		name, ok := v.Value.(string)
		if !ok || name == "" {
			continue
		}
		notes = append(notes, fmt.Sprintf("%s kept (%s): the containers and the entry are gone but the VM stays up; tear it down yourself with %q when it is no longer needed",
			name, *r.KeepFlag, h.machine().DeleteCommand(name)))
	}
	return notes
}
