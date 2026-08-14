package cli

// fleet.go is the client half of phase 6's fleet surface: list, doctor,
// reconcile and clients (06-fleet.md, ARCHITECTURE.md §9.3). The
// coordinator is the only component that can see every repo, so every verb
// here is a thin render of a coordinator verb; reconcile additionally
// applies init's repair path — the four attach outcomes — to entries,
// which is what makes it thin rather than a second repair path.

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	apiclient "github.com/mrgeoffrich/worktree-manager/internal/api/client"
)

// runList implements `wt list [--json] [--wide]`: the whole registry
// across every repo, with the stale / unverifiable / reclaimable / foreign
// markers, secrets redacted unless --wide is given to the owning client.
func runList(args []string, stdout, stderr io.Writer) int {
	return coordVerb("list", args, stdout, stderr, api.VerbList,
		func(fs *flag.FlagSet) func(*coordClient) (*api.ListResult, *apiclient.Error) {
			wide := fs.Bool("wide", false, "show seed credentials (served to the owning client alone)")
			return func(sess *coordClient) (*api.ListResult, *apiclient.Error) {
				return sess.client.List(&api.ListArgs{Wide: *wide})
			}
		}, writeListTable)
}

// writeListTable prints the registry in text form: one line per entry,
// sorted by app then slot, with the markers in the flags column.
func writeListTable(stdout, stderr io.Writer, res *api.ListResult) int {
	entries := append([]api.ListEntry(nil), res.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].App != entries[j].App {
			return entries[i].App < entries[j].App
		}
		if entries[i].Slot != entries[j].Slot {
			return entries[i].Slot < entries[j].Slot
		}
		return entries[i].Slug < entries[j].Slug
	})
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tSLUG\tSLOT\tSTATE\tFLAGS\tDESCRIPTION")
	if len(entries) == 0 {
		fmt.Fprintln(w, "(no entries)\t")
	}
	for _, e := range entries {
		flags := strings.Join(e.Flags, ",")
		if flags == "" {
			flags = "-"
		}
		desc := e.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", e.App, e.Slug, e.Slot, e.State, flags, desc)
	}
	return finish(w)
}

// runDoctor implements `wt doctor [--json]`: read everything, write
// nothing, and report each finding with the exact command that fixes it.
// Findings are the result (stdout); the bounded-coverage notes are
// diagnostics (stderr). Exit 0 when doctor ran, whatever it found —
// findings are data, and a scheduled caller can branch on the JSON.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	return coordVerb("doctor", args, stdout, stderr, api.VerbDoctor,
		func(*flag.FlagSet) func(*coordClient) (*api.DoctorResult, *apiclient.Error) {
			return func(sess *coordClient) (*api.DoctorResult, *apiclient.Error) {
				return sess.client.Doctor()
			}
		}, writeDoctorReport)
}

// writeDoctorReport prints the findings on stdout, each with the command
// that fixes it, and the bounded-coverage notes on stderr.
func writeDoctorReport(stdout, stderr io.Writer, res *api.DoctorResult) int {
	for _, n := range res.Notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	if len(res.Findings) == 0 {
		fmt.Fprintln(stdout, "doctor: no findings; nothing was written.")
		return ExitOK
	}
	for _, f := range res.Findings {
		fmt.Fprintf(stdout, "%s: %s\n", f.Level, f.Message)
		if f.Remedy != "" {
			fmt.Fprintf(stdout, "  fix: %s\n", f.Remedy)
		}
	}
	return ExitOK
}

// runClients implements `wt clients [--json]`: the known clients, their
// kind, last seen, how many entries each owns, and which ephemeral clients
// have aged out.
func runClients(args []string, stdout, stderr io.Writer) int {
	return coordVerb("clients", args, stdout, stderr, api.VerbClientsList,
		func(*flag.FlagSet) func(*coordClient) (*api.ClientsListResult, *apiclient.Error) {
			return func(sess *coordClient) (*api.ClientsListResult, *apiclient.Error) {
				return sess.client.ClientsList()
			}
		}, writeClientsTable)
}

// writeClientsTable prints one line per known client.
func writeClientsTable(stdout, stderr io.Writer, res *api.ClientsListResult) int {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "IDENTITY\tKIND\tEPHEMERAL\tLAST SEEN\tENTRIES\tSTATE")
	if len(res.Clients) == 0 {
		fmt.Fprintln(w, "(no clients recorded)\t")
	}
	for _, c := range res.Clients {
		state := "live"
		if c.AgedOut {
			state = "aged out (entries reclaimable)"
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%d\t%s\n", c.Identity, c.Kind, c.Ephemeral, c.LastSeen, c.Entries, state)
	}
	return finish(w)
}

// reconcilePlan is one entry's repair, decided client-side from the list
// markers and the caller's own stat of the entry's path.
type reconcilePlan struct {
	slug   string
	desc   string
	path   string
	action string // "init" | "teardown"
	detail string
}

// runReconcile implements `wt reconcile [--json] [--dry-run] [--cwd <dir>]`:
// apply the repair paths to entries — the caller's own, plus ephemeral
// entries whose owner has aged out — reusing init's four attach outcomes
// for entries whose directory is present and the coordinator's
// reap-teardown-drop sequence for entries whose directory is gone. The
// verb's whole surface is destructive, so --dry-run is required rather than
// optional: it previews exactly what the real run would do and changes
// nothing.
func runReconcile(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "preview what would be repaired and change nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt reconcile' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	dir := *cwd
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("resolving %s: %v", dir, err), ""))
		return ExitFailure
	}
	// Reconcile needs the repo's spec, not a worktree classification — it
	// runs from the main checkout as naturally as from anywhere else in the
	// repo (rm is the same shape), and the entries it repairs may live in
	// worktrees whose directories are already gone.
	sp, code := loadSpec(absDir, stderr)
	if code != ExitOK {
		return code
	}

	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()

	list, lerr := sess.client.List(&api.ListArgs{})
	if lerr != nil {
		WriteError(stderr, requestErr(sess.endpoint, api.VerbList, lerr))
		return lerr.Code
	}

	// The plan: one repair per eligible entry of this app. Eligibility is
	// the caller's own entries plus aged-out ephemeral ones — the same rule
	// the coordinator re-checks on the refs it receives. Everything else is
	// reported as skipped, never silently passed over (bounded coverage,
	// plan.md §3).
	var plan []reconcilePlan
	var skipped []string
	for _, e := range list.Entries {
		if e.App != sp.App {
			skipped = append(skipped, fmt.Sprintf("%s/%s: belongs to another app; run 'wt reconcile' from that repo (or from the container that owns it)", e.App, e.Slug))
			continue
		}
		own := !containsFlag(e.Flags, "foreign")
		if !own && !containsFlag(e.Flags, "reclaimable") {
			skipped = append(skipped, fmt.Sprintf("%s/%s: owned by a live %s client; not this caller's to repair", e.App, e.Slug, e.OwnerKind))
			continue
		}
		if e.Description == "" {
			skipped = append(skipped, fmt.Sprintf("%s/%s: has no recorded description; run 'wt init --description <text>' in it by hand", e.App, e.Slug))
			continue
		}
		switch e.State {
		case "reserving", "tearing-down":
			// Roll back a reserving entry past its timeout, re-run the
			// teardown of a tearing-down one — both coordinator-side.
			plan = append(plan, reconcilePlan{slug: e.Slug, desc: e.Description,
				action: "teardown", detail: fmt.Sprintf("state %s: reap, tear down by handle and drop (or roll the allocation back)", e.State)})
		default:
			// The directory decides between init's repair path and the
			// handle teardown. For an entry whose path is visible only
			// inside a container, the caller's own stat is authoritative
			// when it is the owner; an aged-out ephemeral entry is
			// reclaimed by handle either way (ARCHITECTURE.md §10.2).
			if fi, serr := os.Stat(e.Path); serr == nil && fi.IsDir() {
				plan = append(plan, reconcilePlan{slug: e.Slug, desc: e.Description, path: e.Path,
					action: "init", detail: fmt.Sprintf("init's repair path in %s (idempotent; re-emits and rebuilds what drifted)", e.Path)})
			} else {
				plan = append(plan, reconcilePlan{slug: e.Slug, desc: e.Description, path: e.Path,
					action: "teardown", detail: fmt.Sprintf("the directory %s is gone (or not visible here): reap, tear down by handle and drop", e.Path)})
			}
		}
	}

	type reconcileRow struct {
		Slug   string `json:"slug"`
		Action string `json:"action"`
		Detail string `json:"detail,omitempty"`
	}
	rows := make([]reconcileRow, 0, len(plan))

	if *dryRun {
		// The preview: exactly what the real run would do, touching nothing
		// (the list read above changed nothing either).
		for _, p := range plan {
			rows = append(rows, reconcileRow{Slug: p.slug, Action: "would-" + p.action, Detail: p.detail})
		}
	} else {
		// The real run: init's repair path for the entries with a directory,
		// the coordinator's reap-teardown-drop for the rest.
		var coordinatorRefs []api.EntryRef
		for _, p := range plan {
			if p.action != "init" {
				coordinatorRefs = append(coordinatorRefs, api.EntryRef{App: sp.App, Slug: p.slug})
				continue
			}
			var buf bytes.Buffer
			code := runInit([]string{"--cwd", p.path, "--slug", p.slug, "--description", p.desc, "--json"}, &buf, stderr)
			detail := strings.TrimSpace(buf.String())
			if code != ExitOK {
				rows = append(rows, reconcileRow{Slug: p.slug, Action: "init-failed", Detail: detail})
				continue
			}
			rows = append(rows, reconcileRow{Slug: p.slug, Action: "repaired", Detail: detail})
		}
		if len(coordinatorRefs) > 0 {
			res, rerr := sess.client.Reconcile(&api.ReconcileArgs{App: sp.App, Spec: *sp, Refs: coordinatorRefs})
			if rerr != nil {
				WriteError(stderr, requestErr(sess.endpoint, api.VerbReconcile, rerr))
				return rerr.Code
			}
			for _, oc := range res.Outcomes {
				rows = append(rows, reconcileRow{Slug: oc.Slug, Action: oc.Action, Detail: oc.Note})
			}
		}
	}

	for _, s := range skipped {
		fmt.Fprintf(stderr, "note: skipped %s\n", s)
	}
	if *jsonOut {
		if err := WriteJSON(stdout, map[string]any{
			"app": sp.App, "dry_run": *dryRun,
			"reconcile": rows,
		}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	if len(rows) == 0 && len(skipped) == 0 {
		fmt.Fprintln(stdout, "reconcile: nothing to repair.")
		return ExitOK
	}
	for _, r := range rows {
		line := fmt.Sprintf("%s/%s: %s", sp.App, r.Slug, r.Action)
		if r.Detail != "" {
			line += " — " + r.Detail
		}
		fmt.Fprintln(stdout, line)
	}
	return ExitOK
}

// containsFlag reports whether the entry's flags carry the named marker.
func containsFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
