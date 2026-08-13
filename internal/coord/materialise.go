package coord

// materialise.go is the coordinator half of init's step 3 (ARCHITECTURE.md
// §9.1): driver apply in dependency order over the entry's denormalised
// resources, with the reverse-order rollback the client leans on. The
// client drives the rollback's outcome: a clean rollback leaves the entry
// reserving for the client to drop with release; a rollback that itself
// failed leaves resources out there, so the entry moves to tearing-down
// with the note and the slot stays held (B2.3) — releasing what survived
// would orphan it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// verbMaterialise is the wire name of the materialise verb.
const verbMaterialise = "materialise"

// materialise implements the materialise verb: run every resource's driver
// apply in dependency order over the entry's resources. The entry must
// exist and belong to the caller; it stays reserving until the client
// activates it.
func (h *Handler) materialise(s *Session, req *protocol.Request) *protocol.Response {
	var args protocol.MaterialiseArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed materialise request: %v", err), "upgrade wt: this coordinator expects an app, slug and spec")
	}
	if err := spec.Validate(&args.Spec); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the materialisation is refused whole: %v", err),
			"fix the spec, then re-run the materialisation")
	}
	if h.Drivers == nil {
		return respErr(1, "the coordinator has no driver registry installed",
			"restart wtd, then re-run the materialisation")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	reg, err := h.st.ReadRegistry()
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	e := registryEntry(reg, args.App, args.Slug)
	if e == nil {
		return respErr(1, fmt.Sprintf("no registry entry for app %q slug %q", args.App, args.Slug),
			"allocate the worktree first, then re-run")
	}
	if perr := h.checkOwner(s, e); perr != nil {
		return &protocol.Response{Error: perr}
	}

	env, perr := h.entryEnv(e, &args.Spec)
	if perr != nil {
		return &protocol.Response{Error: perr}
	}
	env.SeedModes = args.SeedModes

	// The drivers run with the mutex released: the machine driver starts a
	// VM, which its own documentation measures in minutes, and every other
	// client would wait behind it for `wt list`. The entry is reserving,
	// which is the claim; runUnlocked refuses a second operation on it.
	var rep driver.ApplyReport
	if cerr := h.runUnlocked(args.App, args.Slug, func() {
		rep = h.Drivers.ApplyAll(&args.Spec, e.Resources, env)
	}); cerr != nil {
		return &protocol.Response{Error: cerr}
	}

	out := &protocol.MaterialiseResult{
		App: args.App, Slug: args.Slug,
		State:      store.StateReserving,
		Failed:     rep.Failed,
		Err:        errString(rep.Err),
		RolledBack: rep.RolledBack,
	}
	for _, oc := range rep.Outcomes {
		out.Outcomes = append(out.Outcomes, protocol.MaterialiseOutcome{Resource: oc.Resource, Notes: oc.Notes})
	}

	if rep.Failed == "" {
		// Applied in full; the entry stays reserving and the client
		// activates it (step 6). Nothing to write: the entry is untouched.
		return &protocol.Response{Result: mustJSON(out)}
	}

	// A refusal or an unavailability is a protocol-level error with the
	// exit code the caller must see — the capacity guard's refusal is exit
	// 3, an unavailable runner (no Colima on this platform) is exit 4 —
	// rather than an in-band apply failure. ApplyAll already tore the
	// applied resources down in reverse; a refusal at the machine driver
	// is the first apply (machine is forced first), so the rollback
	// covers it. The entry stays reserving and the client releases it,
	// exactly like the clean-rollback path below.
	var refusal *driver.RefusalError
	var unavailable *driver.ErrUnavailable
	if errors.As(rep.Err, &refusal) {
		return &protocol.Response{Error: &protocol.Error{
			Code:   3,
			Msg:    fmt.Sprintf("materialising %s failed: %s", rep.Failed, rep.Err),
			Remedy: "the refusal above names what is running and how to tear one down; fix it, then re-run wt init",
		}}
	}
	if errors.As(rep.Err, &unavailable) {
		return &protocol.Response{Error: &protocol.Error{
			Code:   4,
			Msg:    fmt.Sprintf("materialising %s failed: %s", rep.Failed, rep.Err),
			Remedy: "the error above names the missing context; install it (or move to the platform that has it), then re-run wt init",
		}}
	}

	// Apply failed part-way through; ApplyAll already tore the applied
	// resources down in reverse (03-drivers.md §5). If the rollback itself
	// failed, resources survived: the entry moves to tearing-down with the
	// note and the slot stays held, and the client must not release it.
	if rep.RollbackErr != nil {
		out.State = store.StateTearingDown
		out.RollbackErr = rep.RollbackErr.Error()
		// The registry is re-read: the copy above was taken before the
		// drivers ran without the lock, and another client may have
		// written since.
		reg, err := h.st.ReadRegistry()
		if err != nil {
			return h.storeErr("reading the registry", err)
		}
		fresh := registryEntry(reg, args.App, args.Slug)
		if fresh == nil {
			return respErr(1, fmt.Sprintf("no registry entry for app %q slug %q", args.App, args.Slug),
				"the entry went away mid-materialisation; re-run 'wt list' to see the current state")
		}
		fresh.State = store.StateTearingDown
		fresh.TeardownNote = fmt.Sprintf("materialisation of %s failed and its rollback left resources behind: %v; the slot stays held until a re-run frees everything",
			rep.Failed, rep.RollbackErr)
		fresh.LastSeen = time.Now().UTC().Format(time.RFC3339Nano)
		if err := h.st.WriteRegistry(reg); err != nil {
			return h.storeErr("writing the registry", err)
		}
		return &protocol.Response{Result: mustJSON(out)}
	}

	// Clean rollback: the entry stays reserving and the client drops it
	// with release — the rollback that covers init's steps up to
	// activation.
	return &protocol.Response{Result: mustJSON(out)}
}

// errString renders an error for the wire, "" when nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
