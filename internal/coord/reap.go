package coord

// reap.go is the reaper (04-lifecycle.md §6, B7.1): the coordinator-side
// step of rm that stops orphaned processes bound to a worktree's ports
// before any filesystem mutation, because a survivor keeps heartbeating a
// shared lease and can starve the instance the user is actually looking at.
//
// The rails, all enforced here:
//
//   - Only binaries the spec names are signalled. The spec has no field
//     that names them in this phase — the plan's rule is to report a
//     missing spec field rather than add one, so this is reported in the
//     phase-5 pull request — and the seam ReapBinaries is where a named
//     list plugs in. With nothing named, every holder is reported and
//     nothing is signalled, which is the safe side of the rail: a process
//     that merely grabbed the port is probably the developer's own
//     instance (B1.6 applied to processes).
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
	"path/filepath"
	"sort"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
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
// §6). The spec schema has no field that names binaries in this phase —
// adding one is a schema change, and the plan's rule is to report a missing
// field rather than add it, which the phase-5 pull request does — so the
// default returns nothing and the reaper reports every holder without
// signalling any. A later phase that adds the field implements this seam.
// The returned names are compared against the base name of each holder's
// command; a test can install a fake to drive the signalling path.
func (h *Handler) reapBinaries(sp *spec.Spec) []string {
	if h.ReapBinaries != nil {
		return h.ReapBinaries(sp)
	}
	return nil
}

// reap runs the reaper over one entry: collect the entry's port values,
// subtract the exclusion list, discover the LISTEN holders, classify them
// against the spec's binaries, and signal the named ones with the
// TERM-wait-KILL escalation. It never fails the teardown: every limit is a
// note with the remedy, and teardown completes regardless (B7.1). The
// caller holds h.mu (the ledger read is store work).
func (h *Handler) reap(e *store.Entry, sp *spec.Spec, keepProcesses, dryRun bool) protocol.ReapReport {
	if keepProcesses {
		return protocol.ReapReport{
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
		return protocol.ReapReport{
			Available: false,
			Note: "reap unavailable: this coordinator runs inside a container, and discovery would see only the " +
				"container's pid and network namespaces — not the processes bound to the worktree's ports. " +
				"Run rm from the host, or kill any orphaned processes manually.",
		}
	}

	ports := entryPorts(e)
	if len(ports) == 0 {
		return protocol.ReapReport{Available: true}
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
		return protocol.ReapReport{
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
	report := protocol.ReapReport{Available: true}
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
func classifyAndSignal(holders []platform.Holder, allowlist []string, dryRun bool) ([]protocol.ReapHolder, []protocol.ReapAction) {
	named := map[string]bool{}
	for _, b := range allowlist {
		named[filepath.Base(b)] = true
	}
	var reported []protocol.ReapHolder
	var actions []protocol.ReapAction
	for _, hld := range holders {
		base := filepath.Base(hld.Command)
		if !named[base] {
			reported = append(reported, protocol.ReapHolder{
				PID: hld.PID, Command: hld.Command, Port: hld.Port,
				Reason: "not one of the spec's own binaries — reported and never signalled, because it is probably the developer's own instance",
			})
			continue
		}
		if dryRun {
			actions = append(actions, protocol.ReapAction{
				PID: hld.PID, Command: hld.Command, Port: hld.Port,
				Signal: "would-signal",
			})
			continue
		}
		actions = append(actions, signalEscalation(hld)...)
	}
	return reported, actions
}

// signalEscalation runs B7.1's sequence against one holder: SIGTERM, wait
// about three seconds, then SIGKILL if it is still alive. The actions
// report which step ran and whether it failed (a pid that exited during
// the wait is a success, reported as such).
func signalEscalation(hld platform.Holder) []protocol.ReapAction {
	var actions []protocol.ReapAction
	if err := platform.SignalTerm(hld.PID); err != nil {
		actions = append(actions, protocol.ReapAction{
			PID: hld.PID, Command: hld.Command, Port: hld.Port,
			Signal: "TERM", Err: err.Error(),
		})
		return actions
	}
	actions = append(actions, protocol.ReapAction{
		PID: hld.PID, Command: hld.Command, Port: hld.Port, Signal: "TERM",
	})
	time.Sleep(reapGraceWait)
	if !platform.Alive(hld.PID) {
		actions = append(actions, protocol.ReapAction{
			PID: hld.PID, Command: hld.Command, Port: hld.Port,
			Signal: "KILL", Err: "process exited during the three-second wait; no escalation needed",
		})
		return actions
	}
	if err := platform.SignalKill(hld.PID); err != nil {
		actions = append(actions, protocol.ReapAction{
			PID: hld.PID, Command: hld.Command, Port: hld.Port,
			Signal: "KILL", Err: err.Error(),
		})
		return actions
	}
	actions = append(actions, protocol.ReapAction{
		PID: hld.PID, Command: hld.Command, Port: hld.Port, Signal: "KILL",
	})
	return actions
}

// entryPorts collects the entry's port resource values, in a deterministic
// order.
func entryPorts(e *store.Entry) []int {
	var ports []int
	for _, name := range sortedResourceNames(e.Resources) {
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

// sortedResourceNames returns the entry's resource names sorted, so the
// reap's ports and the rm report's resource list are deterministic.
func sortedResourceNames(resources map[string]spec.Resolved) []string {
	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
