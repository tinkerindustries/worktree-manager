package coord

// fleet.go is phase 6's coordinator surface: the fleet verbs list, doctor,
// reconcile and clients.list, plus reclamation — the machine-wide reads and
// the reclamation writes that only the coordinator can perform (06-fleet.md,
// ARCHITECTURE.md §9.3 and §10.2). The coordinator is the only component
// that can see every repo, which is what makes the cross-repo verbs
// coordinator verbs; the client renders.
//
// Orphaned entries are routine rather than exceptional — the tool that
// created a worktree removes it and will not call wt rm first — so this
// surface carries the phase's load: list keeps stale and unverifiable as
// distinct markers, doctor reports every finding with the exact command
// that fixes it, reconcile applies the existing repair paths (init's four
// attach outcomes, and rm's reap-teardown-drop sequence) to entries, and
// reclamation tears a dead ephemeral client's entries down by handle on a
// measured interval.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/descriptor"
	"github.com/tinkerindustries/worktree-manager/internal/driver"
	"github.com/tinkerindustries/worktree-manager/internal/envfile"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
	"github.com/tinkerindustries/worktree-manager/internal/store"
)

// Verb names, shared between the dispatch and the tests.
const (
	verbList      = "list"
	verbDoctor    = "doctor"
	verbReconcile = "reconcile"
	verbClients   = "clients.list"
)

// ReclaimIntervalDefault is how long an ephemeral client may go unseen
// before its entries become reclaimable — the R3 answer chosen in phase 6
// (plan.md §9.2: the interval's default was left open, and the coordinator
// now has a real signal to set it from: measured last-seen times). Chosen:
// 24 hours.
//
// The two failure modes bound the choice. Too short and a paused container
// loses its entries: a disposable-clone agent session is normally minutes
// to a few hours, but a session interrupted overnight (a laptop closed, a
// machine suspended) must still find its entries when it resumes, and a
// day is the longest plausible interruption. Too long and slot exhaustion
// arrives: the default ceiling is 32 slots per app, and one slot per dead
// container per day is nowhere near exhausting it. A live container that
// runs longer than a day without one wt call is the deliberate exception:
// reclamation is safe even if the client returns, because teardown works
// from a handle held in the registry and the client can re-init.
const ReclaimIntervalDefault = 24 * time.Hour

// list implements the list verb: the whole registry across every repo, with
// the markers of 06-fleet.md §3 computed here — stale (the coordinator can
// stat the path and the directory is gone), unverifiable (the path exists
// only inside a container the coordinator cannot stat, and is never called
// stale), reclaimable (the ephemeral owner has aged out), and foreign (the
// entry belongs to another client). Secrets are served to the owning client
// alone and only under --wide; every other combination is redacted
// structurally (ARCHITECTURE.md §12.2).
func (h *Handler) list(s *Session, req *api.Request) *api.Response {
	var args api.ListArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed list request: %v", err), "upgrade wt: this coordinator expects a list argument object")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// The lenient read: a registry written by a newer schema is listed, never
	// written back (the phase-3 rail), so list works against a newer
	// coordinator's registry.
	reg, err := h.st.ReadRegistryList()
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	clients, err := h.st.ReadClients()
	if err != nil {
		return h.storeErr("reading the client table", err)
	}
	now := time.Now()
	interval := h.reclaimInterval()
	out := make([]api.ListEntry, 0, len(reg.Entries))
	for _, e := range reg.Entries {
		le := api.ListEntry{
			App: e.App, Slug: e.Slug, Slot: e.Slot, Description: e.Description,
			State: e.State, Path: e.Path, PathVisible: e.PathVisible,
			Owner: e.Owner, OwnerKind: e.OwnerKind, Ephemeral: e.Ephemeral,
			CreatedAt: e.CreatedAt, LastSeen: e.LastSeen, Resources: e.Resources,
		}
		owned := e.Owner == s.Identity.Key && e.OwnerKind == s.Identity.Kind
		if !owned {
			le.Flags = append(le.Flags, "foreign")
			// The owner's key is redacted for every other client: a named
			// client's key is its token, and showing it would hand the
			// identity over — the reader could present the token and read
			// the owner's secrets with --wide (the security pass, phase 9;
			// the same redaction covers the owner field of the entry).
			le.Owner = redactKey(e.OwnerKind, e.Owner)
		}
		if e.PathVisible {
			if _, serr := os.Stat(e.Path); serr != nil {
				// stale: the directory is gone and this side could have seen
				// it — actionable from here (06-fleet.md §3).
				le.Flags = append(le.Flags, "stale")
			}
		} else {
			// unverifiable: the path exists only inside a container the
			// coordinator cannot stat, so staleness is unknowable — calling
			// it stale would invite destroying live work (ARCHITECTURE.md
			// §9.3, §10.3).
			le.Flags = append(le.Flags, "unverifiable")
		}
		if e.Ephemeral && clientAgedOut(clients, e.Owner, e.OwnerKind, now, interval) {
			le.Flags = append(le.Flags, "reclaimable")
		}
		if args.Wide && owned {
			le.Secrets = e.Secrets
		}
		out = append(out, le)
	}
	return &api.Response{Result: mustJSON(api.ListResult{Entries: out})}
}

// doctor implements the doctor verb: read everything, write nothing, and
// report each finding with the exact command that fixes it (06-fleet.md §4,
// ARCHITECTURE.md §9.3 — a hard rule: a report listing problems without
// remedies gets read once). Info-level rows are observations and carry no
// remedy because there is nothing to fix.
func (h *Handler) doctor(s *Session, req *api.Request) *api.Response {
	h.mu.Lock()
	defer h.mu.Unlock()

	findings := []api.DoctorFinding{}
	notes := []string{}

	reg, err := h.st.ReadRegistryList()
	if err != nil {
		// M6 §9: an unreadable registry still lets doctor run — the
		// unreadability is its first finding. (A database that cannot be
		// opened at all is refused at wtd startup, not here.)
		findings = append(findings, api.DoctorFinding{
			Kind:    "registry-unreadable",
			Level:   "error",
			Summary: "the registry database cannot be read",
			Message: fmt.Sprintf("the registry is not readable: %v", err),
			Remedy:  "restore the store database (wt.db) from a backup; an unreadable registry is never truncated and recreated — rebuilding it from descriptors is not possible, because the registry is the only source of repository locations and a rebuild from one repo's worktrees would silently drop every other repo's (and every other client's) entries",
		})
		return &api.Response{Result: mustJSON(api.DoctorResult{Findings: findings})}
	}
	if reg.SchemaVersion > store.SchemaVersion {
		findings = append(findings, api.DoctorFinding{
			Kind:    "registry-schema-newer",
			Level:   "warning",
			Summary: "the registry is newer than this coordinator understands; entries are not checked",
			Message: fmt.Sprintf("the registry carries schema version %d but this coordinator understands %d; entries are listed, not checked", reg.SchemaVersion, store.SchemaVersion),
			Remedy:  "upgrade wtd, then re-run 'wt doctor'",
		})
	}
	clients, err := h.st.ReadClients()
	if err != nil {
		return h.storeErr("reading the client table", err)
	}
	bands, err := h.st.ReadBands()
	if err != nil {
		return h.storeErr("reading the band ledger", err)
	}
	specs, err := h.st.ReadSpecs()
	if err != nil {
		return h.storeErr("reading the spec cache", err)
	}

	now := time.Now()
	interval := h.reclaimInterval()

	// The per-app specs this pass found, for the cross-entry checks (the
	// band and reaper checks) that need them.
	appSpecs := map[string]*spec.Spec{}
	// The repos doctor scans: git common dirs reachable from visible entry
	// paths. A repo with no entries is not scanned — the registry is the
	// only source of repository locations — and that bound is stated.
	repos := map[string]bool{}

	// Occupied slots per app, for the ceiling check.
	occupiedByApp := map[string]int{}

	for i := range reg.Entries {
		e := &reg.Entries[i]
		occupiedByApp[e.App]++
		h.doctorEntry(e, clients, specs, bands, now, interval, &findings, &notes, repos, appSpecs)
	}

	h.doctorRepos(reg, repos, &findings, &notes)
	h.doctorBands(bands, &findings, &notes)
	h.doctorCeilings(appSpecs, occupiedByApp, &findings)
	h.doctorReapers(appSpecs, &findings)
	h.doctorMachines(appSpecs, &findings, &notes)
	h.doctorHelpers(appSpecs, &findings, &notes)

	if len(notes) > 0 {
		sort.Strings(notes)
	}
	return &api.Response{Result: mustJSON(api.DoctorResult{Findings: findings, Notes: notes})}
}

// doctorEntry runs every check one entry can be checked for. An
// unverifiable entry is reported as unverifiable — never as stale — and its
// path-dependent checks are skipped; the reclamation finding still applies,
// because a dead ephemeral owner's entry is reclaimed by handle whether or
// not its path is visible (ARCHITECTURE.md §10.2).
func (h *Handler) doctorEntry(e *store.Entry, clients store.ClientsFile, specs store.SpecsFile, bands store.BandsFile, now time.Time, interval time.Duration, findings *[]api.DoctorFinding, notes *[]string, repos map[string]bool, appSpecs map[string]*spec.Spec) {
	app, slug := e.App, e.Slug
	if e.Ephemeral && clientAgedOut(clients, e.Owner, e.OwnerKind, now, interval) {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "entry-owner-aged-out", Level: "warning", Path: e.Path,
			Summary: fmt.Sprintf("its ephemeral %s owner has aged out; the entry can be reclaimed", e.OwnerKind),
			Message: fmt.Sprintf("the ephemeral owner %s client %s, last seen %s, has aged out past the reclamation interval; the entry will be reclaimed by handle",
				e.OwnerKind, redactKey(e.OwnerKind, e.Owner), e.LastSeen),
			Remedy: "run 'wt reconcile' to reclaim it now (or rescue anything it holds first)",
		})
	}

	if !e.PathVisible {
		// The path exists only inside a container the coordinator cannot
		// stat: unverifiable, never stale, and nothing path-dependent can be
		// checked (ARCHITECTURE.md §10.3). An observation, so no remedy.
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "entry-path-unverifiable", Level: "info", Path: e.Path,
			Summary: "its path is visible only inside a container, so its checks are skipped",
			Message: fmt.Sprintf("the entry's path %s is not visible to the coordinator (it exists only inside a container); unverifiable — the entry is never called stale, and its checks are skipped", e.Path),
		})
		return
	}
	if _, serr := os.Stat(e.Path); serr != nil {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "worktree-missing", Level: "error", Path: e.Path,
			Summary: "its worktree directory is gone, but its resources are still allocated",
			Message: fmt.Sprintf("the worktree directory %s is gone", e.Path),
			Remedy:  fmt.Sprintf("run 'wt rm --slug %s' (or 'wt reconcile') to tear the resources down and drop the entry", slug),
		})
		return
	}

	// State checks that do not depend on the spec.
	if e.State == store.StateReserving {
		if created, perr := time.Parse(time.RFC3339Nano, e.CreatedAt); perr == nil && now.Sub(created) >= ReservingTimeout {
			*findings = append(*findings, api.DoctorFinding{
				App: app, Slug: slug, Kind: "entry-reserving-stale", Level: "warning", Path: e.Path,
				Summary: fmt.Sprintf("it has been reserving since %s, past its %s timeout", e.CreatedAt, ReservingTimeout),
				Message: fmt.Sprintf("the entry has been reserving since %s, past its %s timeout — a client died mid-sequence, or materialisation is stuck", e.CreatedAt, ReservingTimeout),
				Remedy:  "run 'wt reconcile' to roll the allocation back",
			})
		}
	}
	if e.State == store.StateTearingDown {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "entry-tearing-down", Level: "warning", Path: e.Path,
			Summary: "it is tearing down with resources still outstanding",
			Message: fmt.Sprintf("the entry is tearing-down with resources outstanding: %s", teardownNoteText(e)),
			Remedy:  fmt.Sprintf("fix the cause named above, then re-run 'wt rm --slug %s'", slug),
		})
	}

	// The repository context: the committed spec, found by walking up from
	// the entry's visible path, with the coordinator's per-app cache as the
	// fallback. Without it, the descriptor and drift checks cannot run —
	// stated, never silent.
	sp := h.specForEntry(e, specs)
	if sp == nil {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "spec-missing", Level: "warning", Path: e.Path,
			Summary: "no wt.yaml can be found for it, so its descriptor, .env and drift checks are skipped",
			Message: fmt.Sprintf("no spec can be found for the entry (walking up from %s found nothing, and no spec is cached for app %q); the descriptor, .env and drift checks are skipped", e.Path, app),
			Remedy:  "commit wt.yaml at the repository root (or run 'wt init' in the worktree), then re-run 'wt doctor'",
		})
		return
	}
	if sp.App != app {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "spec-app-mismatch", Level: "error", Path: e.Path,
			Summary: fmt.Sprintf("its wt.yaml declares app %q, but the entry belongs to app %q", sp.App, app),
			Message: fmt.Sprintf("the spec found from %s declares app %q but the entry belongs to app %q", e.Path, sp.App, app),
			Remedy:  "fix the spec's app field, then re-run 'wt init' in the worktree",
		})
		return
	}
	if _, ok := appSpecs[app]; !ok {
		appSpecs[app] = sp
	}

	// The descriptor: missing or unreadable both fix with init, which
	// re-emits from the entry (attach outcome re-emitted, ARCHITECTURE.md
	// §9.1).
	dpath := e.DescriptorPath
	if dpath == "" {
		dpath = filepath.Join(e.Path, sp.Emit.Descriptor.Filename)
	}
	if _, derr := os.Stat(dpath); derr != nil {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "descriptor-missing", Level: "error", Path: dpath,
			Summary: fmt.Sprintf("its descriptor %s is missing, so nothing in the worktree can read its ports", filepath.Base(dpath)),
			Message: fmt.Sprintf("the descriptor at %s is missing", dpath),
			Remedy:  fmt.Sprintf("run 'wt init' in %s (it re-emits the descriptor from the entry)", e.Path),
		})
	} else if _, derr := descriptor.Read(dpath, sp.Emit.Descriptor.Format); derr != nil {
		*findings = append(*findings, api.DoctorFinding{
			App: app, Slug: slug, Kind: "descriptor-unreadable", Level: "error", Path: dpath,
			Summary: fmt.Sprintf("its descriptor %s cannot be read", filepath.Base(dpath)),
			Message: fmt.Sprintf("the descriptor at %s is unreadable: %v", dpath, derr),
			Remedy:  fmt.Sprintf("run 'wt init' in %s (it re-emits the descriptor from the entry)", e.Path),
		})
	}

	// The .env managed block: a managed key defined outside the block is
	// stripped by the next init (D6, 05-delivery.md §3.2); doctor reports
	// it first so the drift is visible rather than silent.
	if sp.Emit.Env != nil {
		envPath := spec.WorktreePath(e.Path, sp.Emit.Env.Path)
		if outside, oerr := envfile.OutsideBlockKeys(envPath, sp.Emit.Env.Keys); oerr == nil && len(outside) > 0 {
			*findings = append(*findings, api.DoctorFinding{
				App: app, Slug: slug, Kind: "env-keys-outside-block", Level: "warning", Path: envPath,
				Summary: fmt.Sprintf("%d managed key(s) in its .env sit outside the managed block and will be stripped", len(outside)),
				Message: fmt.Sprintf("managed key(s) defined outside the .env block at %s: %s", envPath, strings.Join(outside, ", ")),
				Details: outside,
				Remedy:  fmt.Sprintf("run 'wt init' in %s (it re-emits the block and strips the duplicates)", e.Path),
			})
		}
	}

	// Resource drift: each driver's verify, plus the compose-project-gone
	// check. The union of the spec's rows and the recorded values is
	// covered, exactly like teardown.
	if h.Drivers == nil {
		*notes = append(*notes, fmt.Sprintf("%s/%s: the coordinator has no driver registry; the drift checks were skipped", app, slug))
		return
	}
	env, perr := h.entryEnv(e, sp)
	if perr != nil {
		*notes = append(*notes, fmt.Sprintf("%s/%s: the drift checks could not run: %s", app, slug, perr.Msg))
		return
	}
	covered := map[string]bool{}
	for i := range sp.Resources {
		res := &sp.Resources[i]
		covered[res.Name] = true
		value, ok := e.Resources[res.Name]
		if !ok {
			continue
		}
		d := h.Drivers.Driver(res.Type)
		if d == nil {
			// A declared type with no driver in this phase (machine, cidr):
			// the check is skipped and the bound is stated.
			*notes = append(*notes, fmt.Sprintf("%s/%s: no driver for resource type %q in this phase; the drift check was skipped", app, slug, res.Type))
			continue
		}
		vf, verr := d.Verify(res, value.Value, env)
		if verr != nil {
			*notes = append(*notes, fmt.Sprintf("%s/%s: verifying resource %s failed: %v", app, slug, res.Name, verr))
			continue
		}
		for _, f := range vf {
			if f.Level == driver.LevelInfo {
				// An observation — the machine driver's "cannot be
				// checked from here" bound — is a note, never a finding
				// and never a silent drop (bounded coverage, plan.md §3).
				*notes = append(*notes, fmt.Sprintf("%s/%s: %s", app, slug, f.Message))
				continue
			}
			// A port allocated to a running worktree is bound by that
			// worktree's own service, which is the state init worked to
			// produce. The driver cannot know that, because it is handed a
			// port and not an entry; reported as drift, it makes a warning
			// out of every healthy worktree and hands the reader a remedy
			// ("run wt init to rebuild") for nothing that is wrong. The
			// observation is kept as a note.
			if f.Kind == "port-bound" && e.State == store.StateActive {
				*notes = append(*notes, fmt.Sprintf("%s/%s: %s (expected while the worktree is running)", app, slug, f.Message))
				continue
			}
			*findings = append(*findings, api.DoctorFinding{
				App: app, Slug: slug, Kind: "resource-drift", Level: f.Level, Path: e.Path,
				Summary: fmt.Sprintf("resource %s: %s", res.Name, f.Message),
				Message: f.Message,
				Remedy:  driftRemedy(f, res, e.Path),
			})
		}
		// The compose-project-gone check: an active entry whose compose
		// project has no objects at all has lost its stack (drift row:
		// "compose project gone").
		if e.State == store.StateActive && res.Type == "namespace" && spec.NamespaceKind(res) == "compose" {
			if project, ok := value.Value.(string); ok && project != "" && env.Docker != nil {
				if verr := env.Docker.Version(); verr == nil {
					if objs, oerr := projectObjectsCount(env.Docker, project); oerr == nil && objs == 0 {
						*findings = append(*findings, api.DoctorFinding{
							App: app, Slug: slug, Kind: "compose-project-gone", Level: "error", Path: e.Path,
							Summary: fmt.Sprintf("its compose stack %s is gone while the entry is active", project),
							Message: fmt.Sprintf("the compose project %s has no objects — the stack is gone while the entry is active", project),
							Remedy:  fmt.Sprintf("run 'wt init' in %s to rebuild", e.Path),
						})
					}
				} else {
					*notes = append(*notes, fmt.Sprintf("%s/%s: the docker daemon is unreachable; the compose-project-gone check was skipped", app, slug))
				}
			}
		}
	}
	// A recorded value whose spec row is gone: the spec changed since
	// allocation, and the drift check cannot interpret the value without
	// its row — stated.
	for name := range e.Resources {
		if !covered[name] {
			*notes = append(*notes, fmt.Sprintf("%s/%s: resource %q has a recorded value but the spec no longer declares it; its drift check was skipped", app, slug, name))
		}
	}

	// The repository scan input: this entry's git common dir, so the
	// uninitialised-worktree check can run.
	if common, cerr := gitCommonDir(e.Path); cerr == nil {
		repos[common] = true
	} else {
		*notes = append(*notes, fmt.Sprintf("%s/%s: the uninitialised-worktree scan could not run for %s: %v", app, slug, e.Path, cerr))
	}
}

// teardownNoteText renders the entry's teardown note, or a stand-in when
// the entry carries none.
func teardownNoteText(e *store.Entry) string {
	if e.TeardownNote != "" {
		return e.TeardownNote
	}
	return "teardown left resources behind (the note is empty: an older entry)"
}

// driftRemedy names the command that fixes one driver verify finding.
// Most drift fixes with init's repair path; the findings that do not are
// named by kind, because init rebuilds what the tool allocates and a port
// held by a stranger or a name pinned in a compose file is not that.
func driftRemedy(f driver.Finding, res *spec.Resource, worktree string) string {
	switch f.Kind {
	case "compose-name-pinned":
		return fmt.Sprintf("remove the pinned name: from the compose file, then run 'wt init' in %s", worktree)
	case "port-bound":
		// Init would re-emit the same port and change nothing: the port is
		// held by a process this entry does not own.
		return fmt.Sprintf("run 'wt rm --dry-run' in %s to see what the reaper finds holding it", worktree)
	case "port-unprobeable":
		return "nothing to fix yet: re-run 'wt doctor' when the port can be probed"
	}
	return fmt.Sprintf("run 'wt init' in %s to rebuild", worktree)
}

// doctorRepos scans each repository reachable through the registry for
// worktrees whose directory is present but that have no registry entry —
// the "directory present, no entry" row, fixed by init in that directory
// — and runs the phase-7 generated-artefact drift check against the
// repo's main checkout (drift.go).
func (h *Handler) doctorRepos(reg store.RegistryFile, repos map[string]bool, findings *[]api.DoctorFinding, notes *[]string) {
	for common := range repos {
		out, err := gitOut("-C", common, "worktree", "list", "--porcelain")
		if err != nil {
			*notes = append(*notes, fmt.Sprintf("the worktree list of %s could not be read: %v", common, err))
			continue
		}
		// The first entry of `git worktree list` is the main checkout —
		// slot 0, never managed, never a finding (the classification rule,
		// 01-identity.md §4.1). It is also where the repo's generated
		// artefacts live, committed with the spec.
		main := ""
		first := true
		for _, line := range strings.Split(string(out), "\n") {
			path, ok := strings.CutPrefix(line, "worktree ")
			if !ok {
				continue
			}
			path = strings.TrimSpace(path)
			if first {
				first = false
				main = path
				continue
			}
			if fi, serr := os.Stat(path); serr != nil || !fi.IsDir() {
				continue // the directory is gone: not this finding
			}
			if registryHasPath(reg, path) {
				continue
			}
			*findings = append(*findings, api.DoctorFinding{
				Kind:    "worktree-unadopted",
				Level:   "warning",
				Summary: fmt.Sprintf("the worktree %s has no registry entry", filepath.Base(path)),
				Message: fmt.Sprintf("the worktree %s is present but has no registry entry — it was never initialised, or its entry was dropped", path),
				Remedy:  fmt.Sprintf("run 'wt init' in %s", path),
				Repo:    main,
				Path:    path,
			})
		}
		if main != "" {
			h.doctorDrift(main, h.bandsForDrift(), findings, notes)
		}
	}
}

// bandsForDrift loads the band ledger for the drift check, or an empty
// ledger when it cannot be read (the caller's notes carry the skip).
func (h *Handler) bandsForDrift() store.BandsFile {
	bands, err := h.st.ReadBands()
	if err != nil {
		return store.BandsFile{}
	}
	return bands
}

// registryHasPath reports whether any registry entry's recorded path names
// the same directory as path. Both sides are symlink-resolved before the
// comparison: on macOS /var is a symlink to /private/var, so the same
// directory compares unequal as a string (the hazard that cost init its
// collision check). A path that cannot be resolved — the recorded one may
// name a directory inside a container — compares unequal rather than being
// called a match.
func registryHasPath(reg store.RegistryFile, path string) bool {
	for _, e := range reg.Entries {
		if same, _ := platform.SamePath(e.Path, path); same {
			return true
		}
	}
	return false
}

// doctorBands reports two apps whose registered port bands overlap — the
// machine-wide collision only one ledger can see (06-fleet.md §8).
func (h *Handler) doctorBands(bands store.BandsFile, findings *[]api.DoctorFinding, notes *[]string) {
	type span struct {
		app  string
		name string
		lo   int
		hi   int
	}
	var spans []span
	for _, b := range bands.Bands {
		for name, base := range b.Bases {
			size := b.Spans[name]
			if size < 1 {
				*notes = append(*notes, fmt.Sprintf("app %q's band carries no span for base %q (an older ledger); the overlap check skips it", b.App, name))
				continue
			}
			spans = append(spans, span{app: b.App, name: name, lo: base, hi: base + size - 1})
		}
	}
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			a, b := spans[i], spans[j]
			if a.hi < b.lo || b.hi < a.lo {
				continue
			}
			if a.app == b.app {
				// Two port resources of one app share the band by design —
				// the group form derives every resource of the group from
				// the same base. The finding is the cross-app collision.
				continue
			}
			*findings = append(*findings, api.DoctorFinding{
				Kind:    "band-overlap",
				Level:   "error",
				Summary: fmt.Sprintf("the port bands of apps %q and %q overlap", a.app, b.app),
				Message: fmt.Sprintf("the port bands of app %q (%s: %d..%d) and app %q (%s: %d..%d) overlap",
					a.app, a.name, a.lo, a.hi, b.app, b.name, b.lo, b.hi),
				Remedy: fmt.Sprintf("move one app's band: 'wt bands reserve --base <name>=<port>...' for %q (or %q), then re-run 'wt doctor'", a.app, b.app),
			})
		}
	}
}

// doctorCeilings reports an app approaching its slot ceiling — the
// capacity finding whose remedy every allocation-exhaustion message points
// at (B16.6).
func (h *Handler) doctorCeilings(appSpecs map[string]*spec.Spec, occupiedByApp map[string]int, findings *[]api.DoctorFinding) {
	for app, occupied := range occupiedByApp {
		max := spec.DefaultSlotMax
		if sp, ok := appSpecs[app]; ok && sp.Slots.Max != nil && *sp.Slots.Max >= 1 {
			max = *sp.Slots.Max
		}
		free := max - occupied
		if free*4 < max {
			*findings = append(*findings, api.DoctorFinding{
				App: app, Kind: "slot-ceiling", Level: "warning",
				Summary: fmt.Sprintf("only %d of its %d worktree slots are free", free, max),
				Message: fmt.Sprintf("app %q is approaching its slot ceiling: %d of %d slots are occupied (%d free)", app, occupied, max, free),
				Remedy:  "run 'wt cleanup' to reclaim your slots, or 'wt rm --slug <slug>' to free one entry's slot",
			})
		}
	}
}

// doctorReapers reports a spec whose reaper can never signal: an app with
// port resources and an empty reaper.binaries allowlist. The rail stays
// closed for such a spec — naming nothing signals nothing, which is the
// safe default — and doctor makes the gap visible rather than silent.
func (h *Handler) doctorReapers(appSpecs map[string]*spec.Spec, findings *[]api.DoctorFinding) {
	for app, sp := range appSpecs {
		if !hasPortResources(sp) {
			continue
		}
		if len(sp.Reaper.Binaries) == 0 {
			*findings = append(*findings, api.DoctorFinding{
				App: app, Kind: "reaper-no-binaries", Level: "warning",
				Summary: "its wt.yaml names no reaper binaries, so the reaper can signal nothing",
				Message: fmt.Sprintf("the spec of app %q names no reaper binaries, so the reaper can signal nothing: every process bound to this app's ports would be reported and never signalled", app),
				Remedy:  "set reaper.binaries in the app's wt.yaml (the binaries the reaper may signal), then re-run 'wt doctor'",
			})
		}
	}
}

// reconcile implements the reconcile verb: the caller's own entries plus
// ephemeral entries whose owner has aged out, repaired by the existing
// paths — the reap-teardown-drop sequence rm runs for a gone directory, and
// the rollback for a reserving entry past its timeout. The eligibility is
// re-checked here; the caller's claim is never trusted.
func (h *Handler) reconcile(s *Session, req *api.Request) *api.Response {
	var args api.ReconcileArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed reconcile request: %v", err), "upgrade wt: this coordinator expects an app, spec and entry refs")
	}
	if err := spec.Validate(&args.Spec); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the reconcile is refused whole: %v", err),
			"fix the spec, then re-run")
	}
	if h.Drivers == nil {
		return respErr(1, "the coordinator has no driver registry installed",
			"restart wtd, then re-run")
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, err := h.st.ReadClients()
	if err != nil {
		return h.storeErr("reading the client table", err)
	}
	now := time.Now()
	interval := h.reclaimInterval()

	out := make([]api.ReconcileOutcome, 0, len(args.Refs))
	for _, ref := range args.Refs {
		if ref.App == "" {
			ref.App = args.App
		}
		e, ok, err := h.st.GetEntry(ref.App, ref.Slug)
		if err != nil {
			return h.storeErr("reading the registry", err)
		}
		if !ok {
			out = append(out, api.ReconcileOutcome{App: ref.App, Slug: ref.Slug, Action: "not-found"})
			continue
		}
		own := e.Owner == s.Identity.Key && e.OwnerKind == s.Identity.Kind
		reclaimable := e.Ephemeral && clientAgedOut(clients, e.Owner, e.OwnerKind, now, interval)
		if !own && !reclaimable {
			out = append(out, api.ReconcileOutcome{
				App: ref.App, Slug: ref.Slug, Action: "skipped",
				Note: fmt.Sprintf("owned by %s client %s, last seen %s; only the owning client may repair it, or an ephemeral owner that has aged out",
					e.OwnerKind, redactKey(e.OwnerKind, e.Owner), lastSeenOf(clients, e.Owner, e.OwnerKind)),
			})
			continue
		}
		if e.State == store.StateReserving {
			created, perr := time.Parse(time.RFC3339Nano, e.CreatedAt)
			if perr == nil && now.Sub(created) >= ReservingTimeout {
				// Roll the allocation back by handle, the same outcome the
				// coordinator's own timer produces: a client that died after
				// materialise left real objects whose only handle is this
				// entry, so the teardown runs before the entry goes.
				tresp := h.teardownEntry(s, e, &args.Spec, nil, nil)
				if tresp.Error != nil {
					out = append(out, api.ReconcileOutcome{
						App: ref.App, Slug: ref.Slug, Action: "rolled-back",
						Note: fmt.Sprintf("teardown did not free the slot: %s", tresp.Error.Msg),
					})
					continue
				}
				out = append(out, api.ReconcileOutcome{App: ref.App, Slug: ref.Slug, Action: "rolled-back"})
				continue
			}
			out = append(out, api.ReconcileOutcome{
				App: ref.App, Slug: ref.Slug, Action: "skipped",
				Note: "the entry is reserving and within its timeout: it is either still being set up, or the coordinator's timer will age it out",
			})
			continue
		}
		// The rm sequence: reap, then teardown by handle, then the entry
		// drop (or tearing-down with the note, when something survived).
		// Allowed for an aged-out ephemeral owner by the reclamation rule:
		// teardown works from a handle held in the registry
		// (ARCHITECTURE.md §10.2).
		var reap api.ReapReport
		if cerr := h.runUnlocked(ref.App, ref.Slug, func() {
			reap = h.reap(e, &args.Spec, false, false)
		}); cerr != nil {
			out = append(out, api.ReconcileOutcome{
				App: ref.App, Slug: ref.Slug, Action: "skipped", Note: cerr.Msg,
			})
			continue
		}
		tresp := h.teardownEntry(s, e, &args.Spec, nil, nil)
		if tresp.Error != nil {
			out = append(out, api.ReconcileOutcome{
				App: ref.App, Slug: ref.Slug, Action: "torn-down",
				Note: fmt.Sprintf("teardown did not free the slot: %s", tresp.Error.Msg),
			})
			continue
		}
		note := ""
		if reap.Note != "" {
			note = "reap: " + reap.Note
		}
		out = append(out, api.ReconcileOutcome{App: ref.App, Slug: ref.Slug, Action: "torn-down", Note: note})
	}
	return &api.Response{Result: mustJSON(api.ReconcileResult{Outcomes: out})}
}

// clientsList implements the clients.list verb: the known clients, their
// kind, the coordinator's measured last-seen time, how many entries each
// owns, and whether an ephemeral client has aged out past the reclamation
// interval.
func (h *Handler) clientsList(s *Session, req *api.Request) *api.Response {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients, err := h.st.ReadClients()
	if err != nil {
		return h.storeErr("reading the client table", err)
	}
	reg, err := h.st.ReadRegistryList()
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	now := time.Now()
	interval := h.reclaimInterval()
	out := make([]api.ClientInfo, 0, len(clients.Clients))
	for _, c := range clients.Clients {
		// A client's own row shows its full identity (the caller needs to
		// recognise itself); every other row is redacted — a named row's
		// identity is its token, and an ephemeral row's is a session id,
		// neither of which another client may read (the security pass,
		// phase 9).
		mine := c.Kind == s.Identity.Kind && c.Identity == s.Identity.Key
		identity := c.Identity
		if !mine {
			identity = redactKey(c.Kind, c.Identity)
		}
		info := api.ClientInfo{
			Identity: identity, Kind: c.Kind, Ephemeral: c.Ephemeral, LastSeen: c.LastSeen,
		}
		for _, e := range reg.Entries {
			if e.Owner == c.Identity && e.OwnerKind == c.Kind {
				info.Entries++
			}
		}
		if c.Ephemeral {
			info.AgedOut = clientAgedOut(clients, c.Identity, c.Kind, now, interval)
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Identity < out[j].Identity
	})
	return &api.Response{Result: mustJSON(api.ClientsListResult{Clients: out})}
}

// ReclaimEphemeral reclaims the entries of ephemeral clients whose last
// seen is older than the interval: teardown by handle, then the entry drop.
// It is the coordinator's timer's work — reclamation runs on the configured
// interval measured against real last-seen times (ARCHITECTURE.md §10.2) —
// and it is safe without the client ever returning, because teardown works
// from a handle held in the registry and the coordinator holds host
// privilege. Returns how many entries were dropped; survivors stay in
// tearing-down with the slot held, exactly as a client-driven teardown
// would leave them.
func (h *Handler) ReclaimEphemeral(now time.Time) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Drivers == nil {
		return 0, errors.New("coord: reclamation needs the driver registry")
	}
	clients, err := h.st.ReadClients()
	if err != nil {
		return 0, err
	}
	specs, err := h.st.ReadSpecs()
	if err != nil {
		return 0, err
	}
	reg, err := h.st.ReadRegistry()
	if err != nil {
		return 0, err
	}
	interval := h.reclaimInterval()
	reclaimed := 0
	// Snapshotted by identity and looked up fresh each time: the teardown
	// rewrites the registry, so an index into the slice read here would not
	// survive the first reclamation.
	type candidate struct{ app, slug string }
	var candidates []candidate
	for i := range reg.Entries {
		candidates = append(candidates, candidate{reg.Entries[i].App, reg.Entries[i].Slug})
	}
	for _, c := range candidates {
		e, ok, err := h.st.GetEntry(c.app, c.slug)
		if err != nil {
			return reclaimed, err
		}
		if !ok {
			continue // gone since the snapshot
		}
		if !e.Ephemeral || !clientAgedOut(clients, e.Owner, e.OwnerKind, now, interval) {
			continue
		}
		// The spec: the committed one when the path is visible to the
		// coordinator, else the per-app cache — without it the teardown
		// cannot compute dependent projects or purge refusals, and the
		// entry stays with the skip stated (a silent degrade reads as
		// success).
		sp := h.specForEntry(e, specs)
		if sp == nil {
			h.log.Warn("reclamation skipped an entry: no spec is available",
				"app", e.App, "slug", e.Slug, "owner", e.Owner)
			continue
		}
		tresp := h.teardownEntry(nil, e, sp, nil, nil)
		if tresp.Error != nil {
			h.log.Warn("reclamation teardown left resources behind",
				"app", e.App, "slug", e.Slug, "err", tresp.Error.Msg)
			continue
		}
		reclaimed++
		h.log.Info("reclaimed an aged-out ephemeral client's entry",
			"app", e.App, "slug", e.Slug, "owner", e.Owner)
	}
	// The registry is read fresh for the prune: the teardowns above wrote
	// it, and the owners the prune checks must be current.
	reg, err = h.st.ReadRegistry()
	if err != nil {
		return reclaimed, err
	}
	if err := h.pruneClients(clients, reg, now, interval); err != nil {
		return reclaimed, err
	}
	return reclaimed, nil
}

// pruneClients drops the client rows that can no longer be reached: an
// ephemeral client that has aged out and owns no entry. An ephemeral
// client's identity is a fresh session id per connection and `wt` runs once
// per operation, so without this the table grows by a row per invocation
// and every per-entry scan over it grows with it.
//
// A host or named client's row is kept however old it is: its identity is
// stable, so the row is the record of a client that will be back.
func (h *Handler) pruneClients(clients store.ClientsFile, reg store.RegistryFile, now time.Time, interval time.Duration) error {
	owners := make(map[[2]string]bool, len(reg.Entries))
	for _, e := range reg.Entries {
		owners[[2]string{e.OwnerKind, e.Owner}] = true
	}
	kept := clients.Clients[:0]
	var dropped []store.ClientEntry
	for _, c := range clients.Clients {
		if c.Ephemeral && !owners[[2]string{c.Kind, c.Identity}] &&
			clientAgedOut(clients, c.Identity, c.Kind, now, interval) {
			dropped = append(dropped, c)
			continue
		}
		kept = append(kept, c)
	}
	if len(dropped) == 0 {
		return nil
	}
	// One transaction: the prune is a sequence of row deletions that must
	// land together, exactly like the reclamation it follows.
	if err := h.st.WithTx(func(tx *store.Tx) error {
		for _, c := range dropped {
			if err := tx.DeleteClient(c.Identity, c.Kind); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	h.log.Info("pruned aged-out ephemeral client rows that own no entries", "dropped", len(dropped))
	return nil
}

// specForEntry finds the spec a teardown or a check can use for one entry:
// the committed spec by walking up from the entry's visible path, else the
// per-app cached spec the coordinator records on every allocate. Nil when
// neither is available; the caller states the skip.
func (h *Handler) specForEntry(e *store.Entry, cache store.SpecsFile) *spec.Spec {
	if e.PathVisible {
		if p, err := spec.FindSpecPath(e.Path); err == nil {
			if data, err := os.ReadFile(p); err == nil {
				if sp, err := spec.Parse(data); err == nil && spec.Validate(sp) == nil {
					return sp
				}
			}
		}
	}
	if sp, ok := cache.Specs[e.App]; ok {
		return &sp
	}
	return nil
}

// cacheSpec records the latest valid spec the coordinator saw for an app —
// the reclamation fallback when the entry's path is not visible from the
// host (ARCHITECTURE.md §10.2: reclamation runs with no client around to
// send the spec). Best-effort: a cache write failure is logged, never a
// refusal.
func (h *Handler) cacheSpec(app string, sp *spec.Spec) {
	if err := h.st.UpsertSpec(app, *sp); err != nil {
		h.log.Warn("writing the spec cache", "err", err)
	}
}

// reclaimInterval returns the handler's reclamation interval, defaulting to
// the phase-6 choice.
func (h *Handler) reclaimInterval() time.Duration {
	if h.ReclaimInterval > 0 {
		return h.ReclaimInterval
	}
	return ReclaimIntervalDefault
}

// clientAgedOut reports whether the client's last-seen time — the
// coordinator's own measurement, never a timestamp a client wrote
// (ARCHITECTURE.md §10.2) — is older than the interval. A client row
// missing from clients.json is not aged out: the table is the authority for
// reclamation, and reclaiming on missing data would be destruction without
// a measurement.
func clientAgedOut(clients store.ClientsFile, identity, kind string, now time.Time, interval time.Duration) bool {
	c := findClient(clients, kind, identity)
	if c == nil {
		return false
	}
	ts, err := time.Parse(time.RFC3339Nano, c.LastSeen)
	if err != nil {
		return false
	}
	return now.Sub(ts) >= interval
}

// findClient looks one client row up by kind and identity, nil when the
// table has no row for it.
func findClient(clients store.ClientsFile, kind, identity string) *store.ClientEntry {
	for i := range clients.Clients {
		if clients.Clients[i].Kind == kind && clients.Clients[i].Identity == identity {
			return &clients.Clients[i]
		}
	}
	return nil
}

// lastSeenOf renders a client's last-seen time from the table, or a
// stand-in when the row is missing.
func lastSeenOf(clients store.ClientsFile, identity, kind string) string {
	if c := findClient(clients, kind, identity); c != nil {
		return c.LastSeen
	}
	return "never recorded"
}

// gitCommonDir returns the repository's git common directory for a visible
// worktree path — the input to `git worktree list`, which names every
// worktree of the repo.
func gitCommonDir(path string) (string, error) {
	out, err := gitOut("-C", path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir in %s: %w", path, err)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	return common, nil
}

// projectObjectsCount asks the docker seam how many objects carry the
// project label.
func projectObjectsCount(d driver.Docker, project string) (int, error) {
	containers, err := d.ListContainers(project)
	if err != nil {
		return 0, err
	}
	networks, err := d.ListNetworks(project)
	if err != nil {
		return 0, err
	}
	volumes, err := d.ListVolumes(project)
	if err != nil {
		return 0, err
	}
	return len(containers) + len(networks) + len(volumes), nil
}

// doctorMachines reports an app approaching its machine capacity — the
// "max_concurrent machines approaching" finding of 06-fleet.md §4, whose
// remedy is B4.2's message naming what is running. The count is the
// daemon's own instances, one machine-wide count per run, because the
// constraint is on the machine (03-drivers.md §4.5, §8): the capacity
// guard refuses a new instance past max_concurrent, and doctor says so
// before the refusal is the first anyone hears of it. Doctor reads
// everything and writes nothing, so the runner is only asked, never
// started or stopped.
func (h *Handler) doctorMachines(appSpecs map[string]*spec.Spec, findings *[]api.DoctorFinding, notes *[]string) {
	hasMachine := false
	for _, sp := range appSpecs {
		for i := range sp.Resources {
			if sp.Resources[i].Type == "machine" {
				hasMachine = true
				break
			}
		}
		if hasMachine {
			break
		}
	}
	if !hasMachine {
		return
	}
	runner := h.machine()
	instances, err := runner.List()
	if err != nil {
		// No runner on this platform, or the helper cannot answer: the
		// bound is stated, never a silent pass.
		*notes = append(*notes, fmt.Sprintf("the machine capacity check was skipped: %v", err))
		return
	}
	var running []string
	for _, in := range instances {
		if in.Running {
			running = append(running, in.Name)
		}
	}
	sort.Strings(running)
	for app, sp := range appSpecs {
		for i := range sp.Resources {
			r := &sp.Resources[i]
			if r.Type != "machine" {
				continue
			}
			max := spec.DefaultMachineMaxConcurrent
			if r.MaxConcurrent != nil && *r.MaxConcurrent >= 1 {
				max = *r.MaxConcurrent
			}
			// "Approaching": three quarters of the limit or more, the same
			// ratio the slot-ceiling finding uses.
			if len(running)*4 < max*3 {
				continue
			}
			var parts []string
			for _, n := range running {
				parts = append(parts, fmt.Sprintf("%s (tear down with %q)", n, runner.DeleteCommand(n)))
			}
			*findings = append(*findings, api.DoctorFinding{
				App: app, Kind: "machine-capacity", Level: "warning",
				Summary: fmt.Sprintf("%d of its %d VM instances are running; the next new one is refused", len(running), max),
				Details: parts,
				Message: fmt.Sprintf("app %q is approaching the machine capacity: %d of %d instances are running (%s); the next new instance is refused by the capacity guard",
					app, len(running), max, strings.Join(parts, ", ")),
				Remedy: "tear one instance down with the command named above, then re-run 'wt doctor'",
			})
		}
	}
}

// coordinatorHelper is one binary the coordinator shells out to, with the
// sentence naming what stops working when it cannot be reached.
type coordinatorHelper struct {
	binary string
	needed string
}

// gitOut runs one git command for the coordinator's own repo reads and
// returns its stdout. git is resolved through platform.HelperCommand like
// every other helper, so what doctor reports reachable and what these
// scans can actually run are the same binary; the error carries git's
// stderr rather than a bare exit status.
func gitOut(args ...string) ([]byte, error) {
	cmd, err := platform.HelperCommand("git", args...)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, platform.HelperError("git "+strings.Join(args, " "), err)
	}
	return out, nil
}

// lookHelper resolves a helper the way every driver does.
func (h *Handler) lookHelper(bin string) (string, error) {
	if h.LookHelper != nil {
		return h.LookHelper(bin)
	}
	return platform.LookHelper(bin)
}

// doctorHelpers reports the helper binaries the coordinator cannot reach.
//
// platform.LookHelper resolves a helper at call time — this process's
// PATH, then the known install directories — which is what stops a
// supervised coordinator reporting docker missing on a machine where
// docker runs. It cannot know about a genuinely custom install location,
// and that residue is what this finding is for: the sweep's gh failure
// logged its skip and reported nothing, and a driver's refusal only
// arrives when somebody runs the verb that needs the helper. Doctor asks
// before either.
//
// The question is asked in the coordinator's own process, because the
// coordinator's environment is the one that decides. It cannot tell an
// uninstalled helper from an unreachable one, and the remedy says both.
//
// Which helpers are checked follows from what the adopted specs declare.
// git and gh are always checked — the repo scans and the scheduled
// cleanup sweep need them whatever a spec says. The listener-discovery
// helpers come from platform.ListenerHelpers, because which tool a port
// scan reaches for is the platform's answer and not this package's to
// assume: lsof on macOS, netstat and tasklist on Windows, and nothing at
// all on a Linux with /proc, where the scan reads files. Naming lsof here
// reported a Windows machine missing a binary Windows does not have and
// a scan there never runs. docker is checked when some spec declares a
// namespace or cidr resource, and the machine runner when some spec
// declares a machine resource.
func (h *Handler) doctorHelpers(appSpecs map[string]*spec.Spec, findings *[]api.DoctorFinding, notes *[]string) {
	helpers := []coordinatorHelper{
		{"git", "reading repositories: the doctor repo scan, the generated-artefact drift check, and every worktree safety check rm and cleanup make"},
		{"gh", "the scheduled cleanup sweep, which needs a merged pull request before it removes anything"},
	}
	for _, bin := range platform.ListenerHelpers() {
		helpers = append(helpers, coordinatorHelper{bin,
			"'wt ports scan', which reports the machine's listening sockets"})
	}
	var wantsDocker, wantsMachine bool
	for _, sp := range appSpecs {
		for i := range sp.Resources {
			switch sp.Resources[i].Type {
			case "namespace", "cidr":
				wantsDocker = true
			case "machine":
				wantsMachine = true
			}
		}
	}
	if wantsDocker {
		helpers = append(helpers, coordinatorHelper{"docker",
			"the namespace and cidr drivers, which allocate and tear down this repository's compose projects and networks"})
	}
	if wantsMachine {
		if bin := h.machine().Binary(); bin != "" {
			helpers = append(helpers, coordinatorHelper{bin,
				"the machine driver, which starts and destroys this repository's per-worktree VMs"})
		} else {
			*notes = append(*notes, "the machine helper check was skipped: this platform has no VM runner")
		}
	}

	var missing []string
	for _, hp := range helpers {
		if _, err := h.lookHelper(hp.binary); err != nil {
			missing = append(missing, fmt.Sprintf("%s (needed for %s)", hp.binary, hp.needed))
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	where := "nothing beyond it"
	if dirs := platform.HelperDirs(); len(dirs) > 0 {
		where = strings.Join(dirs, ", ")
	}
	*findings = append(*findings, api.DoctorFinding{
		Kind:    "helpers-unreachable",
		Level:   "warning",
		Summary: fmt.Sprintf("the coordinator cannot reach %d helper binaries: %s", len(missing), strings.Join(missing, ", ")),
		Details: missing,
		Message: fmt.Sprintf("the coordinator cannot reach %d of the helper binaries it shells out to: %s. It searched its own PATH and then %s",
			len(missing), strings.Join(missing, ", "), where),
		Remedy: fmt.Sprintf("if you can run the helper in your own shell, it is installed somewhere the coordinator does not search: set %s to the directory holding it and restart the coordinator. If you cannot run it either, install it first",
			platform.HelperDirsEnv),
	})
}
