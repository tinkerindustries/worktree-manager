package cli

// fleet_test.go exercises the client half of the phase-6 fleet surface:
// list, doctor, reconcile and clients rendered against the fake
// coordinator, plus the exit-criterion-3 end-to-end — a real coordinator
// in-process, a real git worktree deleted by hand, and `wt reconcile`
// tearing the entry down.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/coord"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// TestRunListJSONAndTable: `wt list --json` prints exactly the registry the
// coordinator reports (markers included), and the table renders it.
func TestRunListJSONAndTable(t *testing.T) {
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"list": cannedT(&api.ListResult{Entries: []api.ListEntry{
			{App: "compose-app", Slug: "wt-1", Slot: 1, State: "active", Path: "/gone", PathVisible: true,
				Owner: "4242", OwnerKind: "host", Flags: []string{"stale", "foreign"}},
			{App: "compose-app", Slug: "wt-2", Slot: 2, State: "active", Path: "/container/wt-2", PathVisible: false,
				Owner: "s123", OwnerKind: "ephemeral", Ephemeral: true, Flags: []string{"unverifiable", "reclaimable", "foreign"}},
		}}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list --json exit = %d; stderr: %s", code, stderr)
	}
	var res api.ListResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(res.Entries) != 2 || !hasFlagT(res.Entries[0].Flags, "stale") || !hasFlagT(res.Entries[1].Flags, "unverifiable") {
		t.Errorf("entries = %+v", res.Entries)
	}

	code, stdout, _ = runCLI(t, "list")
	if code != ExitOK {
		t.Fatalf("list table exit = %d", code)
	}
	for _, want := range []string{"compose-app", "wt-1", "stale", "wt-2", "unverifiable"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table lacks %q:\n%s", want, stdout)
		}
	}
}

// TestRunDoctorJSONAndClean: doctor renders the findings with their
// remedies; a clean report prints "no findings".
func TestRunDoctorJSONAndClean(t *testing.T) {
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"doctor": cannedT(&api.DoctorResult{Findings: []api.DoctorFinding{
			{App: "compose-app", Slug: "wt-1", Level: "error",
				Message: "the worktree directory /gone is gone",
				Remedy:  "run 'wt rm --slug wt-1' (or 'wt reconcile') to tear the resources down and drop the entry"},
		}, Notes: []string{"the docker daemon is unreachable; the compose-project-gone check was skipped"}}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor --json exit = %d; stderr: %s", code, stderr)
	}
	var res api.DoctorResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(res.Findings) != 1 || res.Findings[0].Remedy == "" {
		t.Errorf("findings = %+v", res.Findings)
	}

	code, stdout, stderr = runCLI(t, "doctor")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d", code)
	}
	if !strings.Contains(stdout, "error: the worktree directory") || !strings.Contains(stdout, "fix: run 'wt rm") {
		t.Errorf("table lacks the finding and its fix:\n%s", stdout)
	}
	if !strings.Contains(stderr, "note: the docker daemon is unreachable") {
		t.Errorf("stderr lacks the coverage note:\n%s", stderr)
	}

	ep2 := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"doctor": cannedT(&api.DoctorResult{}),
	})
	t.Setenv("WT_ENDPOINT", ep2)
	code, stdout, _ = runCLI(t, "doctor")
	if code != ExitOK || !strings.Contains(stdout, "no findings") {
		t.Errorf("clean doctor = %d %q", code, stdout)
	}
}

// TestRunClientsJSONAndTable: the client table renders kind, last seen,
// entries owned and the aged-out state.
func TestRunClientsJSONAndTable(t *testing.T) {
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"clients.list": cannedT(&api.ClientsListResult{Clients: []api.ClientInfo{
			{Identity: "4242", Kind: "host", LastSeen: "2026-08-12T10:00:00Z", Entries: 2},
			{Identity: "s123", Kind: "ephemeral", Ephemeral: true, LastSeen: "2026-08-10T10:00:00Z", Entries: 1, AgedOut: true},
		}}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "clients", "--json")
	if code != ExitOK {
		t.Fatalf("clients --json exit = %d; stderr: %s", code, stderr)
	}
	var res api.ClientsListResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(res.Clients) != 2 || !res.Clients[1].AgedOut {
		t.Errorf("clients = %+v", res.Clients)
	}

	code, stdout, _ = runCLI(t, "clients")
	if code != ExitOK {
		t.Fatalf("clients exit = %d", code)
	}
	if !strings.Contains(stdout, "aged out") || !strings.Contains(stdout, "s123") {
		t.Errorf("table lacks the aged-out client:\n%s", stdout)
	}
}

// TestRunReconcileDryRunAndReal: --dry-run previews every repair and
// changes nothing; the real run sends the coordinator the teardown refs and
// reports the outcomes.
func TestRunReconcileDryRunAndReal(t *testing.T) {
	sp := lifecycleSpec(t)
	main, _ := lifecycleFixture(t, sp)
	// The list the fake serves: one stale entry of this app (its directory
	// is gone, so the repair is the handle teardown) and one foreign entry
	// of another app.
	listResp := &api.ListResult{Entries: []api.ListEntry{
		{App: sp.App, Slug: "wt-1", Slot: 1, State: "active", Description: "the gone worktree",
			Path: filepath.Join(main, "gone"), PathVisible: true, Owner: "4242", OwnerKind: "host", Flags: []string{"stale"}},
		{App: "other-app", Slug: "wt-x", Slot: 1, State: "active", Description: "another app",
			Path: "/elsewhere", PathVisible: true, Owner: "4242", OwnerKind: "host", Flags: []string{"foreign"}},
	}}
	reconcileResp := &api.ReconcileResult{Outcomes: []api.ReconcileOutcome{
		{App: sp.App, Slug: "wt-1", Action: "torn-down"},
	}}
	rec, ep := newRecordingCoord(t, map[string]func(*api.Request) *api.Response{
		"list":      cannedT(listResp),
		"reconcile": cannedT(reconcileResp),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "reconcile", "--dry-run", "--cwd", main)
	if code != ExitOK {
		t.Fatalf("reconcile --dry-run exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "would-teardown") {
		t.Errorf("dry run lacks the would-teardown row:\n%s", stdout)
	}
	if len(rec.requests["reconcile"]) != 0 {
		t.Errorf("dry run sent %d reconcile requests; nothing may be sent", len(rec.requests["reconcile"]))
	}
	if !strings.Contains(stderr, "other-app/wt-x") {
		t.Errorf("stderr lacks the foreign-app skip:\n%s", stderr)
	}

	// The real run: the stale entry goes to the coordinator verb; the
	// foreign-app entry is skipped client-side.
	code, stdout, stderr = runCLI(t, "reconcile", "--cwd", main, "--json")
	if code != ExitOK {
		t.Fatalf("reconcile exit = %d; stderr: %s", code, stderr)
	}
	var out struct {
		App       string `json:"app"`
		DryRun    bool   `json:"dry_run"`
		Reconcile []struct {
			Slug   string `json:"slug"`
			Action string `json:"action"`
		} `json:"reconcile"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if out.DryRun || len(out.Reconcile) != 1 || out.Reconcile[0].Action != "torn-down" {
		t.Errorf("reconcile result = %+v", out)
	}
	if n := len(rec.requests["reconcile"]); n != 1 {
		t.Fatalf("reconcile requests = %d, want 1", n)
	}
	var args api.ReconcileArgs
	if err := json.Unmarshal(rec.requests["reconcile"][0], &args); err != nil {
		t.Fatalf("decoding the reconcile request: %v", err)
	}
	if len(args.Refs) != 1 || args.Refs[0].Slug != "wt-1" || args.App != sp.App {
		t.Errorf("reconcile request = %+v", args)
	}
}

// TestRunReconcileTearsDownDeletedWorktree is exit criterion 3 end to end
// without docker: a real coordinator in-process, a real git worktree
// initialised through the real client, the directory deleted by hand, and
// `wt reconcile` from the main checkout tearing the resources down and
// dropping the entry.
func TestRunReconcileTearsDownDeletedWorktree(t *testing.T) {
	// The spec: a port only — no hooks, no docker, and a port has no
	// teardown, so the reconcile teardown is clean with no daemon.
	max := 8
	sp := &spec.Spec{
		Version: 1, App: "rec-app",
		Slots:     spec.Slots{Max: &max},
		Resources: []spec.Resource{{Type: "port", Name: "api"}},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(sp); err != nil {
		t.Fatalf("spec: %v", err)
	}
	main, worktree := lifecycleFixture(t, sp)

	// The real coordinator on a loopback port, exactly the phase-6 CI
	// arrangement; the client resolves it through the endpoint.json the
	// server writes into the store root.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	storeRoot := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(storeRoot, 0o700); err != nil {
		t.Fatalf("store root: %v", err)
	}
	st, err := store.Open(storeRoot)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := coord.NewHandler(st, log)
	if err != nil {
		t.Fatalf("building the coordinator: %v", err)
	}
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	srv := coord.NewServer(h, log)
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, "127.0.0.1:0") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveDone:
		default:
		}
	})
	t.Setenv("WT_HOME", storeRoot)
	waitEndpoint(t, storeRoot)

	// Register the band through the handler, the way `wt bands reserve`
	// would.
	sess := &coord.Session{Version: api.VersionMax, Identity: coord.Identity{Kind: api.KindHost, Key: "4242"}}
	if resp := h.Handle(context.Background(), sess, &api.Request{
		Verb: "bands.reserve",
		Args: mustJSONT(&api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 7400}}),
	}); resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}

	// Init through the real client: the entry is allocated, materialised
	// (a port has no apply), emitted and activated.
	code, _, stderr := runCLI(t, "init", "--cwd", worktree, "--description", "the deleted worktree")
	if code != ExitOK {
		t.Fatalf("init exit = %d; stderr: %s", code, stderr)
	}

	// The tool that created the worktree removes it without calling wt rm.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("deleting %s by hand: %v", worktree, err)
	}

	// list shows the entry as stale — never as unverifiable.
	code, stdout, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("list exit = %d; stderr: %s", code, stderr)
	}
	var listed api.ListResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &listed); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Entries) != 1 || !hasFlagT(listed.Entries[0].Flags, "stale") || hasFlagT(listed.Entries[0].Flags, "unverifiable") {
		t.Fatalf("listed = %+v, want one stale entry", listed.Entries)
	}

	// Reconcile from the main checkout, --dry-run first.
	code, stdout, _ = runCLI(t, "reconcile", "--dry-run", "--cwd", main)
	if code != ExitOK || !strings.Contains(stdout, "would-teardown") {
		t.Fatalf("reconcile --dry-run = %d\n%s", code, stdout)
	}
	reg, err := st.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if len(reg.Entries) != 1 {
		t.Fatalf("dry run changed the registry: %d entries", len(reg.Entries))
	}

	// The real run tears the resources down and drops the entry.
	code, stdout, stderr = runCLI(t, "reconcile", "--cwd", main)
	if code != ExitOK {
		t.Fatalf("reconcile exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "torn-down") {
		t.Errorf("reconcile output lacks the torn-down outcome:\n%s", stdout)
	}
	reg, err = st.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("registry entries = %d after reconcile, want 0: %+v", len(reg.Entries), reg.Entries)
	}

	// Doctor is clean afterwards.
	code, stdout, stderr = runCLI(t, "doctor", "--json")
	if code != ExitOK {
		t.Fatalf("doctor exit = %d; stderr: %s", code, stderr)
	}
	var doc api.DoctorResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, f := range doc.Findings {
		if f.Level != "info" {
			t.Errorf("doctor after reconcile = %s", stdout)
		}
	}
}

// TestRunListUnreachableExitsFive: the fleet verbs reach the coordinator,
// so exit 5 is wired like every other coordinator verb.
func TestRunListUnreachableExitsFive(t *testing.T) {
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	for _, args := range [][]string{{"list"}, {"doctor"}, {"clients"}} {
		code, _, stderr := runCLI(t, args...)
		if code != ExitUnreachable {
			t.Errorf("%v exit = %d, want 5; stderr: %s", args, code, stderr)
		}
	}
	// Reconcile loads the repo's spec first, then dials.
	sp := lifecycleSpec(t)
	main, _ := lifecycleFixture(t, sp)
	code, _, stderr := runCLI(t, "reconcile", "--cwd", main)
	if code != ExitUnreachable {
		t.Errorf("reconcile exit = %d, want 5; stderr: %s", code, stderr)
	}
}

// TestRunReconcileNotAdopted: reconcile needs the repo's committed spec.
func TestRunReconcileNotAdopted(t *testing.T) {
	code, _, stderr := runCLI(t, "reconcile", "--cwd", t.TempDir())
	if code != ExitUnavailable {
		t.Errorf("reconcile in an unadopted repo exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "wt.yaml") {
		t.Errorf("stderr does not name the missing spec:\n%s", stderr)
	}
}

// hasFlagT is the marker helper for the cli tests.
func hasFlagT(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
