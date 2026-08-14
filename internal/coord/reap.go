package coord

// reap.go is the reaper (04-lifecycle.md §6, B7.1): the coordinator-side
// step of rm that stops orphaned processes bound to a worktree's ports
// before any filesystem mutation, because a survivor keeps heartbeating a
// shared lease and can starve the instance the user is actually looking at.
//
// The rails, all enforced here:
//
//   - Only binaries the spec names are signalled. The spec's reaper.binaries
//     field (added in phase 6) is the allowlist, read through the ReapBinaries
//     seam; it defaults to empty, so a spec that names nothing signals
//     nothing — the safe side of the rail: a process that merely grabbed the
//     port is probably the developer's own instance (B1.6 applied to
//     processes).
//   - SIGTERM, wait about three seconds, then SIGKILL.
//   - Reserved and legacy default ports are never touched: the exclusion
//     list is the spec's reserved block plus the ledger's host-global
//     reservations — the same list allocation uses, never a second one
//     (B8.3).
//   - Never pkill -f <appname>: every signal is by pid, and only pids
//     discovery produced for this entry's ports (B7.2).
//   - Discovery failure is non-fatal: a note names it, teardown completes,
//     and the user is told to kill manually (B7.1).
//   - In a container, discovery sees the container's namespaces and finds
//     nothing: reported as unavailable with the remedy named (run on the
//     host), never as a successful reap of zero processes (04-lifecycle.md
//     §6).
//   - --keep-processes opts out; --dry-run lists what would be signalled.

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// verbRm is the wire name of the rm verb.
const verbRm = "rm"

// reapGraceWait is the "wait about three seconds" of B7.1 between SIGTERM
// and SIGKILL. A variable so the escalation path is testable without
// sleeping three seconds.
var reapGraceWait = 3 * time.Second

// ReapBinaries is the seam where the spec's named binaries plug into the
// reaper: "only binaries the spec names are signalled" (04-lifecycle.md
// §6). The default reads reaper.binaries from the spec — the field phase 6
// added — and a spec that names nothing signals nothing, which is the safe
// side of the rail: a process that merely grabbed the port is probably the
// developer's own instance (B1.6 applied to processes). The returned names
// are compared against the base name of each holder's command; a test can
// install a fake to drive the signalling path without touching the spec.
func (h *Handler) reapBinaries(sp *spec.Spec) []string {
	if h.ReapBinaries != nil {
		return h.ReapBinaries(sp)
	}
	if sp == nil {
		return nil
	}
	return sp.Reaper.Binaries
}

// reap runs the reaper over one entry: collect the entry's port values,
// subtract the exclusion list, discover the LISTEN holders, classify them
// against the spec's binaries, and signal the named ones with the
// TERM-wait-KILL escalation. It never fails the teardown: every limit is a
// note with the remedy, and teardown completes regardless (B7.1).
//
// It runs with h.mu released, under the entry's claim: discovery shells out
// and the escalation waits out a grace period. The ledger read it starts
// with is the store read that has to be tolerated there — a reservation
// added mid-reap is not seen until the next call, and the consequence is
// bounded, since a reserved port's holder is skipped rather than signalled.
func (h *Handler) reap(e *store.Entry, sp *spec.Spec, keepProcesses, dryRun bool) api.ReapReport {
	if keepProcesses {
		return api.ReapReport{
			Available:     false,
			Note:          "reaping opted out (--keep-processes): processes bound to this worktree's ports are left running",
			KeptProcesses: true,
		}
	}
	inContainer := platform.InContainer()
	if h.InContainer != nil {
		inContainer = h.InContainer()
	}
	if inContainer {
		return api.ReapReport{
			Available: false,
			Note: "reap unavailable: this coordinator runs inside a container, and discovery would see only the " +
				"container's pid and network namespaces — not the processes bound to the worktree's ports. " +
				"Run rm from the host, or kill any orphaned processes manually.",
		}
	}

	ports := entryPorts(e)
	if len(ports) == 0 {
		return api.ReapReport{Available: true}
	}

	// The exclusion list is the same one allocation uses: the spec's
	// reserved block plus the ledger's host-global reservations. A
	// reserved port's holder is never touched (B8.3) — it is probably the
	// co-resident production stack.
	excluded := map[int]bool{}
	for _, p := range sp.Reserved.Ports {
		excluded[p] = true
	}
	bands, err := h.st.ReadBands()
	if err != nil {
		return api.ReapReport{
			Available: false,
			Note: fmt.Sprintf("reap unavailable: the band ledger could not be read (%v), so the host-global "+
				"reservations — and with them the ports that must never be touched — are unknown; "+
				"complete the teardown and kill any orphaned processes manually", err),
		}
	}
	for _, r := range bands.Reservations {
		for _, p := range r.Ports {
			excluded[p] = true
		}
	}
	report := api.ReapReport{Available: true}
	var active []int
	for _, p := range ports {
		if excluded[p] {
			report.SkippedPorts = append(report.SkippedPorts, p)
			continue
		}
		active = append(active, p)
	}
	if len(active) == 0 {
		return report
	}

	holders, err := platform.Listeners(active)
	if err != nil {
		// Discovery failure is non-fatal: note it, complete the teardown,
		// tell the user to kill manually (B7.1).
		report.Available = false
		report.Note = fmt.Sprintf("process discovery failed (%v); the teardown completes, and any orphaned "+
			"processes bound to this worktree's ports must be killed manually", err)
		return report
	}
	report.Holders, report.Signalled = classifyAndSignal(holders, h.reapBinaries(sp), dryRun)
	return report
}

// classifyAndSignal splits the discovered holders into the reported-never-
// signalled and the signalled (or would-signal, under --dry-run) halves,
// applying the TERM-wait-KILL escalation to the spec's own binaries. The
// base-name comparison is the whole of "whose command names one of its own
// binaries" (B7.1): a holder whose command base is in the allowlist is
// signalled, everything else is reported and never signalled.
func classifyAndSignal(holders []platform.Holder, allowlist []string, dryRun bool) ([]api.ReapHolder, []api.ReapAction) {
	named := map[string]bool{}
	for _, b := range allowlist {
		named[filepath.Base(b)] = true
	}
	var reported []api.ReapHolder
	var actions []api.ReapAction
	var signal []platform.Holder
	for _, hld := range holders {
		base := filepath.Base(hld.Command)
		if !named[base] {
			reported = append(reported, api.ReapHolder{
				PID: hld.PID, Command: hld.Command, Port: hld.Port,
				Reason: "not one of the spec's own binaries — reported and never signalled, because it is probably the developer's own instance",
			})
			continue
		}
		if dryRun {
			actions = append(actions, api.ReapAction{
				PID: hld.PID, Command: hld.Command, Port: hld.Port,
				Signal: "would-signal",
			})
			continue
		}
		signal = append(signal, hld)
	}
	return reported, append(actions, signalEscalation(signal)...)
}

// signalEscalation runs B7.1's sequence against every named holder: the
// graceful signal to each, one shared wait of about three seconds, then the
// force kill for those still alive. The actions report which path it took and why: a TERM action
// naming the graceful step, then either a KILL action stating that no
// escalation was needed, or one stating that the escalation happened.
// The distinction is the point on Windows (08-platform.md §4.3): taskkill
// without /F posts a close message that console applications do not
// receive and GUI applications may ignore, so the escalation to taskkill
// /F must be reported, never silently equated with a graceful stop — that
// equivalence would hide a teardown that killed a desktop process without
// letting it release its lease. A failed graceful signal does not abort
// the sequence: on Windows the failure is the common case, not the
// process being gone, and the escalation still has to run.
func signalEscalation(holders []platform.Holder) []api.ReapAction {
	if len(holders) == 0 {
		return nil
	}
	terms := make([]api.ReapAction, len(holders))
	for i, hld := range holders {
		terms[i] = api.ReapAction{
			PID: hld.PID, Command: hld.Command, Port: hld.Port, Signal: "TERM",
		}
		if err := platform.SignalTerm(hld.PID); err != nil {
			terms[i].Err = err.Error()
		}
	}

	// One wait covers every holder: the grace period is wall-clock, so
	// three orphaned processes cost three seconds rather than nine.
	time.Sleep(reapGraceWait)

	actions := make([]api.ReapAction, 0, 2*len(holders))
	for i, hld := range holders {
		actions = append(actions, terms[i])
		if !platform.Alive(hld.PID) {
			actions = append(actions, api.ReapAction{
				PID: hld.PID, Command: hld.Command, Port: hld.Port,
				Signal: "KILL", Err: "no escalation needed: the process exited during the wait after the graceful signal",
			})
			continue
		}
		killAction := api.ReapAction{
			PID: hld.PID, Command: hld.Command, Port: hld.Port, Signal: "KILL",
		}
		if killErr := platform.SignalKill(hld.PID); killErr != nil {
			killAction.Err = killErr.Error()
		} else {
			// The escalation must be stated, never implied: on Windows this
			// action IS taskkill /F, and reporting it as a graceful stop
			// would hide the very situation the reaper exists to prevent
			// (08-platform.md §4.3).
			killAction.Err = "escalated to the force kill: the graceful signal (SIGTERM on unix, taskkill without /F on Windows) did not stop the process within the wait"
		}
		actions = append(actions, killAction)
	}
	return actions
}

// entryPorts collects the entry's port resource values, in a deterministic
// order.
func entryPorts(e *store.Entry) []int {
	var ports []int
	for _, name := range slices.Sorted(maps.Keys(e.Resources)) {
		r := e.Resources[name]
		if r.Type != "port" {
			continue
		}
		if p, ok := r.Value.(int); ok {
			ports = append(ports, p)
		}
	}
	return ports
}
