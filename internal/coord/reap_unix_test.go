//go:build darwin || linux

package coord

import (
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// reap_unix_test.go is the unix half of the reaper tests (moved here in
// phase 8b): they signal real processes with sh, python3 and kill, which
// do not exist on Windows. The Windows termination story — taskkill
// without /F posting a close message, the escalation to /F reported, a
// failed graceful step never aborting the escalation — is written in
// internal/platform/listeners_windows.go and coord/reap.go and needs an
// interactive Windows desktop to be verified; it is reported not_run.
//
// The escalation-reporting tests pin the phase-8b contract: the reaper
// states which of the two paths it took (08-platform.md §4.3).

// --- the reaper ----------------------------------------------------------

// reapPortsEntry writes a registry entry whose port resources are exactly
// the given values, and returns it.
func reapPortsEntry(t *testing.T, h *Harness, ports ...int) *store.Entry {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	resources := map[string]spec.Resolved{}
	for i, p := range ports {
		resources[fmt.Sprintf("api-%d", i)] = spec.Resolved{Type: "port", Value: p}
	}
	e := &store.Entry{
		App: "compose-app", Slug: "wt-1", Slot: 1,
		Owner: "4242", OwnerKind: protocol.KindHost,
		Path: "/tmp/wt/wt-1", State: store.StateActive,
		Resources: resources, CreatedAt: now, LastSeen: now,
	}
	if err := h.Store.UpsertEntry(*e); err != nil {
		t.Fatalf("writing the registry: %v", err)
	}
	return e
}

// listenProc starts a process that listens on 127.0.0.1:port and returns
// its pid and the command /proc reports for it.
func listenProc(t *testing.T, port int) (pid int, comm string) {
	t.Helper()
	return listenProcWith(t, port, "")
}

// listenProcWith starts a listener whose python preamble is setup —
// injected before the socket code, so a test can make the process ignore
// SIGTERM or exit on it, the two behaviours the escalation report
// distinguishes.
func listenProcWith(t *testing.T, port int, setup string) (pid int, comm string) {
	t.Helper()
	script := fmt.Sprintf(`python3 -c "import socket,time
%s
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', %d)); s.listen(1); time.sleep(300)"`, setup, port)
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the listener: %v", err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		exec.Command("kill", "-9", strconv.Itoa(cmd.Process.Pid)).Run()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	// Wait until the socket is listening.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The command name comes from the same discovery the reaper uses, rather
	// than from a second implementation. Reading /proc/<pid>/comm directly
	// works only on Linux, and the reaper's own allowlist is matched against
	// whatever platform.Listeners reports — so asking it is both portable and
	// the thing actually under test.
	holders, err := platform.Listeners([]int{port})
	if err != nil {
		t.Fatalf("discovering the listener on port %d: %v", port, err)
	}
	for _, h := range holders {
		if h.PID == cmd.Process.Pid {
			return h.PID, h.Command
		}
	}
	// The listener may be a child that replaced the shell, or the shell may
	// have forked; either way the discovery's pid is the one the reaper acts
	// on, so take it.
	if len(holders) == 1 {
		return holders[0].PID, holders[0].Command
	}
	t.Fatalf("discovery found %d holders of port %d, want the listener started here: %+v", len(holders), port, holders)
	return 0, ""
}

// reapHarness is the harness with the container-detection seam disabled,
// so the reap logic itself is exercised in whatever namespace the test
// runs in.
func reapHarness(t *testing.T) *Harness {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InContainer = func() bool { return false }
	return h
}

// TestCoordReapSignalsOnlyNamedBinaries: a holder whose command is one of
// the spec's binaries is signalled (TERM, then KILL after the wait); a
// holder that merely grabbed the port is reported and never signalled —
// it is probably the developer's own instance (B7.1).
func TestCoordReapSignalsOnlyNamedBinaries(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	old := reapGraceWait
	reapGraceWait = 100 * time.Millisecond
	defer func() { reapGraceWait = old }()

	report := h.H.reap(e, sp, false, false)
	if !report.Available {
		t.Fatalf("reap unavailable: %s", report.Note)
	}
	if len(report.Signalled) == 0 {
		t.Fatalf("no signal recorded for the named binary %q: %+v", comm, report)
	}
	if len(report.Holders) != 0 {
		t.Errorf("holders = %+v, want none (the only holder is a named binary)", report.Holders)
	}
	// The process must actually be gone.
	deadline := time.Now().Add(5 * time.Second)
	for alivePid(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alivePid(pid) {
		t.Errorf("the named binary (pid %d) survived the reap", pid)
	}
}

// alivePid is the test-side liveness check.
func alivePid(pid int) bool {
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

// TestCoordReapNeverSignalsANonSpecHolder: a process that merely grabbed
// the port is reported and never signalled, even with a reaper installed.
func TestCoordReapNeverSignalsANonSpecHolder(t *testing.T) {
	port := freeCoordPort(t)
	pid, _ := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{"some-other-binary"} }

	report := h.H.reap(e, sp, false, false)
	if !report.Available {
		t.Fatalf("reap unavailable: %s", report.Note)
	}
	if len(report.Signalled) != 0 {
		t.Fatalf("signalled %+v; a non-spec holder is never signalled", report.Signalled)
	}
	if len(report.Holders) != 1 {
		t.Fatalf("holders = %+v, want the one reported holder", report.Holders)
	}
	if !alivePid(pid) {
		t.Error("the reported holder was killed; only the spec's binaries may be signalled")
	}
}

// TestCoordReapNeverTouchesReservedPorts: a holder on a port in the spec's
// reserved block (or the ledger's host-global reservations) is never
// signalled, even when its command is a spec binary — the exclusion list
// is reused, never a second one (B8.3).
func TestCoordReapNeverTouchesReservedPorts(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	sp.Reserved.Ports = []int{port}
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	report := h.H.reap(e, sp, false, false)
	if len(report.SkippedPorts) != 1 || report.SkippedPorts[0] != port {
		t.Fatalf("skipped_ports = %v, want [%d]", report.SkippedPorts, port)
	}
	if len(report.Signalled) != 0 {
		t.Fatalf("signalled %+v; a reserved port's holder is never touched", report.Signalled)
	}
	if !alivePid(pid) {
		t.Error("the reserved port's holder was killed")
	}

	// The ledger's host-global reservations are the same rail.
	port2 := freeCoordPort(t)
	pid2, comm2 := listenProc(t, port2)
	e2 := reapPortsEntry(t, h, port2)
	if err := h.Store.AddReservation(store.Reservation{
		Ports: []int{port2}, Note: "the co-resident production stack",
	}); err != nil {
		t.Fatalf("writing the ledger: %v", err)
	}
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm2} }
	report2 := h.H.reap(e2, sp, false, false)
	if len(report2.Signalled) != 0 || !alivePid(pid2) {
		t.Errorf("host-reserved port holder signalled or killed: %+v", report2.Signalled)
	}
}

// TestCoordReapInContainerReportsUnavailable: a coordinator inside a
// container reports the reap as unavailable with the remedy named — never
// as a successful reap of zero processes (04-lifecycle.md §6).
func TestCoordReapInContainerReportsUnavailable(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InContainer = func() bool { return true }
	e := reapPortsEntry(t, h, freeCoordPort(t))
	sp := materialiseSpec(t)
	report := h.H.reap(e, sp, false, false)
	if report.Available {
		t.Error("Available = true inside a container, want false")
	}
	if !strings.Contains(report.Note, "container") || !strings.Contains(report.Note, "host") {
		t.Errorf("the note must name the cause and the remedy: %q", report.Note)
	}
}

// TestCoordReapDryRunListsWithoutSignalling: --dry-run lists what would be
// signalled and touches nothing (B7.1).
func TestCoordReapDryRunListsWithoutSignalling(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	report := h.H.reap(e, sp, false, true)
	if len(report.Signalled) != 1 || report.Signalled[0].Signal != "would-signal" {
		t.Fatalf("signalled = %+v, want one would-signal", report.Signalled)
	}
	if !alivePid(pid) {
		t.Error("the dry-run reap killed the process")
	}
}

// TestCoordReapKeepProcessesOptsOut: --keep-processes opts the reaper out
// entirely, stating the opt-out.
func TestCoordReapKeepProcessesOptsOut(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	report := h.H.reap(e, sp, true, false)
	if !report.KeptProcesses {
		t.Error("KeptProcesses = false, want true")
	}
	if len(report.Signalled) != 0 {
		t.Errorf("signalled %+v under --keep-processes", report.Signalled)
	}
	if !alivePid(pid) {
		t.Error("--keep-processes did not keep the process")
	}
}

// TestCoordReapEscalationIsReported: a holder that ignores the graceful
// signal survives the wait, the reaper escalates to the force kill, and
// the report says so — the escalation is stated, never implied
// (08-platform.md §4.3). This is the same report shape Windows produces
// when taskkill without /F is ignored, so the reporting contract is
// proved here on unix.
func TestCoordReapEscalationIsReported(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProcWith(t, port, "import signal\nsignal.signal(signal.SIGTERM, signal.SIG_IGN)")
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	old := reapGraceWait
	reapGraceWait = 100 * time.Millisecond
	defer func() { reapGraceWait = old }()

	report := h.H.reap(e, sp, false, false)
	if !report.Available {
		t.Fatalf("reap unavailable: %s", report.Note)
	}
	if len(report.Signalled) < 2 {
		t.Fatalf("signalled = %+v, want TERM then KILL", report.Signalled)
	}
	last := report.Signalled[len(report.Signalled)-1]
	if last.Signal != "KILL" {
		t.Errorf("last action = %+v, want the KILL escalation", last)
	}
	if !strings.Contains(last.Err, "escalated") {
		t.Errorf("the escalation is not stated: %q", last.Err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alivePid(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alivePid(pid) {
		t.Error("the TERM-ignoring holder survived the reap")
	}
}

// TestCoordReapGracefulExitNeedsNoEscalation: a holder that honours the
// graceful signal exits during the wait, and the report says no
// escalation was needed — the graceful path is reported as graceful.
func TestCoordReapGracefulExitNeedsNoEscalation(t *testing.T) {
	port := freeCoordPort(t)
	_, comm := listenProcWith(t, port, "import os, signal\nsignal.signal(signal.SIGTERM, lambda s, f: os._exit(0))")
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	old := reapGraceWait
	reapGraceWait = 100 * time.Millisecond
	defer func() { reapGraceWait = old }()

	report := h.H.reap(e, sp, false, false)
	if !report.Available {
		t.Fatalf("reap unavailable: %s", report.Note)
	}
	if len(report.Signalled) < 2 {
		t.Fatalf("signalled = %+v, want TERM then the no-escalation note", report.Signalled)
	}
	last := report.Signalled[len(report.Signalled)-1]
	if last.Signal != "KILL" {
		t.Errorf("last action = %+v, want the KILL note action", last)
	}
	if !strings.Contains(last.Err, "no escalation needed") {
		t.Errorf("the graceful outcome is not stated: %q", last.Err)
	}
}
