package cli

// init.go is `wt init` (04-lifecycle.md §4, ARCHITECTURE.md §9.1): attach
// to the current tree, allocate, materialise, emit, run the hooks and
// activate. The seven steps, and what each undoes on failure:
//
//	1 client      classify, resolve the slug from the directory basename,
//	              parse the spec                         — nothing written yet
//	2 coordinator allocate the lowest free slot, commit reserving
//	                                                   — drop the entry
//	3 coordinator driver apply in dependency order      — driver teardown in
//	                                                    reverse, drop the entry
//	4 client      emit the descriptor and the .env block — remove written files,
//	                                                    drop the entry
//	5 client      hooks install, prepull, build          — none: the worktree
//	                                                    and allocation survive
//	                                                    for inspection
//	6 coordinator flip the entry to active              —
//	7 client      hooks start, seed, health              — leave running; the
//	                                                    worktree is usable
//
// Rollback covers the steps up to activation and stops there (ARCHITECTURE.md
// §9.1). The git worktree itself is never removed — another tool created it
// and may still be using it. A failed health check has not left the worktree
// broken, it has left it not yet running: the allocation stays, with the
// diagnostic evidence. The client drives the rollback, calling release for
// what it allocated; a client that dies mid-sequence leaves a reserving
// entry, which the coordinator ages out on its own timer.
//
// Four attach outcomes make init the repair path as well as the setup path:
// no entry and no descriptor (allocate and set up), entry and descriptor
// agree (reconcile, re-derive, rebuild what is missing), entry present and
// descriptor missing (re-emit the descriptor from the entry and say so),
// descriptor present and no entry (the client sends the descriptor and the
// coordinator rebuilds the entry at its recorded slot — the descriptor beats
// the registry, ARCHITECTURE.md §8.6 rule 1).
//
// init never refuses over working-tree state — a worktree an agent has been
// editing for an hour is dirty by design — and never reallocates: an
// existing entry's slot is authoritative (rule 5).

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/envfile"
	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// initHookPhases are the two hook phases of the sequence: the build phase
// (step 5) and the bring-up phase (step 7), in run order.
var initHookPhases = [][]string{buildHooks, startHooks}

// initResult is the one JSON object `wt init --json` prints.
type initResult struct {
	App        string                 `json:"app"`
	Slug       string                 `json:"slug"`
	Slot       int                    `json:"slot"`
	State      string                 `json:"state"`
	Attach     string                 `json:"attach"` // which of the four outcomes applied
	Notes      []string               `json:"notes,omitempty"`
	ProbeNote  string                 `json:"probe_note,omitempty"`
	Skipped    []string               `json:"skipped,omitempty"`
	Descriptor *descriptor.Descriptor `json:"descriptor,omitempty"`
}

// runInit implements `wt init [--json] [--dry-run] [--cwd <dir>]
// [--slug <s>] --description <text> [--param <name>=<value>]...`.
func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "print what would happen and change nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	slug := fs.String("slug", "", "explicit slug (default: the worktree directory's basename)")
	description := fs.String("description", "", "what this worktree is for, ten words or fewer (required)")
	explicit := map[string]string{}
	fs.Var(stringMapFlag(explicit), "param", "hook parameter value, --param <name>=<value> (repeatable)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt init --description <text>' with no positional arguments",
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

	cls, code := lifecycleClassify(absDir, stderr)
	if code != ExitOK {
		return code
	}

	// A worktree nested inside another worktree is refused: the two trees
	// are indistinguishable in every listing the user reads afterwards
	// (04-lifecycle.md §7, deferred from phase 1 for want of a real-repo
	// fixture).
	if enclosing, err := identity.NestedInside(cls.WorktreeRoot); err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("checking whether %s is nested inside another worktree: %v", cls.WorktreeRoot, err), ""))
		return ExitFailure
	} else if enclosing != "" {
		e := New(ExitRefused,
			fmt.Sprintf("refusing to initialise %s: it is nested inside another git working tree at %s, and the two trees would be indistinguishable in every listing",
				cls.WorktreeRoot, enclosing),
			"move the worktree out of the enclosing tree (or delete it and create it elsewhere), then re-run wt init")
		WriteError(stderr, e)
		return e.Code
	}

	// The slug comes from the directory basename, never the branch — the
	// branch is not usable as identity because branches get renamed and
	// deleted (01-identity.md §4.1). An explicit --slug overrides it.
	name := *slug
	if name == "" {
		name = identity.DefaultSlug(cls.WorktreeRoot)
	}
	if reason := identity.ValidateSlug(name); reason != "" {
		e := New(ExitRefused,
			fmt.Sprintf("slug %q is not valid (%s)", name, reason),
			"give an explicit --slug matching ^[a-z0-9][a-z0-9-]*$ (at most 32 characters), then re-run")
		WriteError(stderr, e)
		return e.Code
	}

	sp, code := loadSpec(cls.WorktreeRoot, stderr)
	if code != ExitOK {
		return code
	}

	// The description is required and non-interactive: a missing one fails
	// asking for the flag (04-lifecycle.md §3.2, B11.16 — ten words or
	// fewer so list can say what each worktree is for).
	if *description == "" {
		e := UsageError(
			"run 'wt init --description \"what this worktree is for\"' (ten words or fewer)",
			"init needs a description: what is this worktree for?")
		WriteError(stderr, e)
		return e.Code
	}
	if n := len(strings.Fields(*description)); n > 10 {
		e := New(ExitRefused,
			fmt.Sprintf("the description is %d words; the field is for ten words or fewer", n),
			"give a description of ten words or fewer, then re-run")
		WriteError(stderr, e)
		return e.Code
	}

	dpath := identity.DescriptorPath(cls.WorktreeRoot, sp.Emit.Descriptor)
	existing, err := readDescriptorIfPresent(dpath, sp.Emit.Descriptor.Format)
	if err != nil {
		WriteError(stderr, New(ExitFailure, err.Error(),
			"delete or fix the descriptor, then re-run wt init"))
		return ExitFailure
	}

	if *dryRun {
		return initDryRun(stdout, stderr, *jsonOut, cls, sp, name, *description, dpath, existing)
	}

	// Step 2: allocate. The client drives the rollback of everything up to
	// activation, so from here on every failure path either releases the
	// entry or says why it cannot.
	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()

	slotHint := 0
	if existing != nil {
		slotHint = existing.Slot
	}
	raw, rerr := sess.request("allocate", &api.AllocateArgs{
		Spec: *sp, Slug: name, Path: cls.WorktreeRoot,
		DescriptorPath: dpath, Description: *description,
		SlotHint: slotHint,
	})
	if rerr != nil {
		WriteError(stderr, rerr)
		return rerr.Code
	}
	var res api.AllocateResult
	if err := json.Unmarshal(raw, &res); err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("decoding the allocation: %v", err), ""))
		return ExitFailure
	}

	// A slug that collides with a different path under the same app stops
	// with exit 3 and asks for an explicit slug — two worktree directories
	// with the same basename are otherwise indistinguishable in every
	// listing the user reads afterwards (04-lifecycle.md §2.3).
	samePath, _ := platform.SamePath(res.Path, cls.WorktreeRoot)
	if res.Path != "" && !samePath {
		e := New(ExitRefused,
			fmt.Sprintf("slug %q already names entry %q at %s, which is a different path from %s",
				name, res.Slug, res.Path, cls.WorktreeRoot),
			"give an explicit --slug for this worktree, then re-run")
		WriteError(stderr, e)
		return e.Code
	}

	attach := attachOutcome(res.Existed, existing != nil)
	notes := []string{}
	switch attach {
	case "rebuilt":
		fmt.Fprintf(stderr, "no registry entry found, but a descriptor exists at %s: the registry was deleted, corrupted, or is another machine's — the entry is rebuilt from the descriptor at its recorded slot %d\n",
			dpath, res.Slot)
	case "re-emitted":
		fmt.Fprintf(stderr, "an entry exists for %q, but the descriptor at %s is missing: the descriptor is re-emitted from the entry\n", name, dpath)
	case "reconciled":
		fmt.Fprintf(stderr, "an entry and the descriptor at %s agree: reconciling — resources are re-derived, re-applied and re-emitted\n", dpath)
	}
	if res.ProbeNote != "" {
		notes = append(notes, res.ProbeNote)
	}
	notes = append(notes, res.Skipped...)
	notes = append(notes, res.Notes...)

	// Step 3: materialise — driver apply in dependency order. On a failure
	// whose rollback was clean the client drops the entry (release); on a
	// failure whose rollback left resources behind the entry moves to
	// tearing-down and the client must not release what survived (B2.3).
	// A refusal (exit 3 — the machine capacity guard) or an unavailability
	// (exit 4 — no VM runner on this platform) comes back as a protocol
	// error; the entry is the caller's own and reserving, so releasing it
	// is the same rollback the in-band failure path drives.
	mraw, merr := sess.request("materialise", &api.MaterialiseArgs{
		App: sp.App, Slug: name, Spec: *sp,
	})
	if merr != nil {
		if _, rerr := sess.request("release", &api.EntryRef{App: sp.App, Slug: name}); rerr != nil {
			fmt.Fprintf(stderr, "warning: releasing the entry after the failed init failed: %v; a reserving entry ages out on the coordinator's timer, and a tearing-down entry is repaired with 'wt rm --slug %s'\n", rerr, name)
		}
		WriteError(stderr, merr)
		return merr.Code
	}
	var mres api.MaterialiseResult
	if err := json.Unmarshal(mraw, &mres); err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("decoding the materialisation: %v", err), ""))
		return ExitFailure
	}
	if mres.Failed != "" {
		for _, oc := range mres.Outcomes {
			notes = append(notes, fmt.Sprintf("%s: applied", oc.Resource))
		}
		msg := fmt.Sprintf("materialising %s failed: %s", mres.Failed, mres.Err)
		if mres.RollbackErr != "" {
			// The rollback itself failed: resources are out there and the
			// entry stays tearing-down with the slot held.
			e := New(ExitFailure,
				msg+fmt.Sprintf("; the rollback also failed: %s — the entry stays tearing-down and the slot stays held", mres.RollbackErr),
				"fix the cause named above, then re-run wt init (or wt rm to tear down)")
			WriteError(stderr, e)
			return e.Code
		}
		// Clean rollback: drop the entry — the rollback that covers init's
		// steps up to activation.
		drop := func() {
			if _, rerr := sess.request("release", &api.EntryRef{App: sp.App, Slug: name}); rerr != nil {
				fmt.Fprintf(stderr, "warning: releasing the entry after the failed init failed: %v; a reserving entry ages out on the coordinator's timer, and a tearing-down entry is repaired with 'wt rm --slug %s'\n", rerr, name)
			}
		}
		drop()
		e := New(ExitFailure,
			msg+"; the applied resources were torn down in reverse and the entry was released — the tree is exactly as it was found",
			"fix the cause named above, then re-run wt init")
		WriteError(stderr, e)
		return e.Code
	}
	for _, oc := range mres.Outcomes {
		for _, n := range oc.Notes {
			notes = append(notes, fmt.Sprintf("%s: %s", oc.Resource, n))
		}
	}

	// Step 4: emit the descriptor and the .env managed block, and make git
	// ignore the descriptor. The undo for a failure here is removing the
	// written files — the .env is only removed if init created it, so hand
	// content that predates init is never destroyed — then releasing the
	// entry.
	ctx := spec.Context{
		App: sp.App, Slug: name, Slot: res.Slot,
		Home: clientHome(), Worktree: cls.WorktreeRoot,
	}
	d := &descriptor.Descriptor{
		Version:     descriptor.Version,
		App:         sp.App,
		Slug:        name,
		Slot:        res.Slot,
		Path:        cls.WorktreeRoot,
		Standalone:  cls.Standalone,
		Description: *description,
		Resources:   res.Resources,
		State:       descriptor.BuildState(sp),
		Extras:      map[string]any{},
	}
	for _, row := range res.Shared {
		d.Shared = append(d.Shared, descriptor.Shared{Name: row.Name, Impact: row.Impact})
	}
	if existing != nil {
		d.Extras = existing.Extras
	}
	d.Extras = withStickyParams(d.Extras, stickyParamsOf(existing))

	// The files this step wrote, for the rollback: the descriptor is always
	// init's own file; the .env only if it did not exist before.
	envPath := ""
	envExisted := false
	if sp.Emit.Env != nil {
		envPath = spec.WorktreePath(cls.WorktreeRoot, sp.Emit.Env.Path)
		if _, serr := os.Stat(envPath); serr == nil {
			envExisted = true
		}
	}
	written := []string{dpath}

	writeErr := descriptor.Write(dpath, sp.Emit.Descriptor.Format, d)
	if writeErr == nil {
		fmt.Fprintf(stderr, "descriptor written to %s (slot %d)\n", dpath, res.Slot)
		if sp.Emit.Env != nil {
			seedFrom := filepath.Join(cls.MainCheckoutPath, sp.Emit.Env.Path)
			rep, uerr := envfile.Update(envPath, sp.Emit.Env, ctx, res.Resources, seedFrom)
			if uerr != nil {
				writeErr = fmt.Errorf("writing the .env managed block: %w", uerr)
			} else {
				if !envExisted {
					written = append(written, envPath)
				}
				fmt.Fprintf(stderr, ".env managed block written to %s\n", envPath)
				if rep.SeededFrom != "" {
					fmt.Fprintf(stderr, ".env seeded from the main checkout's %s (unmanaged content only)\n", rep.SeededFrom)
				}
				if rep.SeedSkipped != "" {
					fmt.Fprintf(stderr, "note: %s\n", rep.SeedSkipped)
				}
				if len(rep.Stripped) > 0 {
					fmt.Fprintf(stderr, "note: managed key(s) defined outside the block were stripped: %s\n", strings.Join(rep.Stripped, ", "))
				}
			}
		}
		if writeErr == nil {
			if _, ierr := descriptor.EnsureIgnored(cls.WorktreeRoot, cls.GitCommonDir, sp.Emit.Descriptor.Filename); ierr != nil {
				fmt.Fprintf(stderr, "warning: making git ignore the descriptor failed: %v; the descriptor may show as untracked\n", ierr)
			}
		}
	}
	if writeErr != nil {
		// Undo step 4: remove the written files, then drop the entry.
		for _, f := range written {
			if os.Remove(f) == nil {
				fmt.Fprintf(stderr, "removed %s\n", f)
			}
		}
		if _, rerr := sess.request("release", &api.EntryRef{App: sp.App, Slug: name}); rerr != nil {
			fmt.Fprintf(stderr, "warning: releasing the entry after the failed init failed: %v; a reserving entry ages out on the coordinator's timer, and a tearing-down entry is repaired with 'wt rm --slug %s'\n", rerr, name)
		}
		e := New(ExitFailure,
			fmt.Sprintf("emitting the allocation failed: %v; the written files were removed and the entry was released — the tree is as it was found", writeErr),
			"fix the cause named above, then re-run wt init")
		WriteError(stderr, e)
		return e.Code
	}

	// Steps 5 and 7: the hooks, via the sequencer. Step 5's undo is none —
	// an install failure stops, surfaces the output verbatim, and the
	// worktree and allocation survive for inspection (04-lifecycle.md §9);
	// step 7's failure leaves the worktree allocated and usable, and says
	// what failed.
	runner := newHookRunner(sp, ctx, res.Resources, cls.WorktreeRoot, stderr, false)
	runner.setExplicit(explicit)
	runner.setPersisted(stickyParamsOf(existing))
	if sp.Emit.Env != nil {
		keys, kerr := resolvedEnvKeys(sp, ctx, res.Resources)
		if kerr != nil {
			WriteError(stderr, New(ExitFailure, kerr.Error(), "fix the spec's emit.env keys, then re-run wt init"))
			return ExitFailure
		}
		runner.envKeys = keys
	}

	persist := func() {
		persistSticky(d, dpath, sp.Emit.Descriptor.Format, runner.chosenValues(), stderr)
	}

	for phase, hooks := range initHookPhases {
		failed, herr := runner.runAll(hooks, &sp.Hooks)
		persist()
		if herr != nil {
			// The entry is already active for the second phase — for the
			// first phase it stays reserving until activation. Either way
			// the allocation and the tree survive for inspection.
			e := New(ExitFailure,
				fmt.Sprintf("hook %s failed: %v", failed, herr),
				"fix the cause named above, then re-run wt init (it reconciles and re-runs the failed hooks)")
			WriteError(stderr, e)
			return e.Code
		}
		if phase == 0 {
			// Step 6: flip the entry to active — the point past which
			// rollback stops (ARCHITECTURE.md §9.1).
			if _, aerr := sess.request("activate", &api.EntryRef{App: sp.App, Slug: name}); aerr != nil {
				WriteError(stderr, aerr)
				return aerr.Code
			}
			fmt.Fprintf(stderr, "entry %s/%s activated (slot %d)\n", sp.App, name, res.Slot)
		}
	}

	// Success. What stdout carries is the descriptor — the form everything
	// downstream reads (04-lifecycle.md §4.1); wt show --json is the same
	// record through its own verb.
	result := initResult{
		App: sp.App, Slug: name, Slot: res.Slot, State: "active",
		Attach: attach, Notes: notes,
		ProbeNote: res.ProbeNote, Skipped: res.Skipped,
		Descriptor: d,
	}
	if *jsonOut {
		if err := WriteJSON(stdout, result); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	writeInitTable(stdout, &result)
	return ExitOK
}

// attachOutcome names the four attach outcomes of ARCHITECTURE.md §9.1.
func attachOutcome(entryExisted, descriptorExisted bool) string {
	switch {
	case entryExisted && descriptorExisted:
		return "reconciled"
	case entryExisted:
		return "re-emitted"
	case descriptorExisted:
		return "rebuilt"
	default:
		return "allocated"
	}
}

// stringMapFlag is the flag.Value for the repeatable --param name=value
// flag: the flag package calls Set once per occurrence.
// initDryRun prints what init would do and changes nothing.
func initDryRun(stdout, stderr io.Writer, jsonOut bool, cls *identity.Classification, sp *spec.Spec, slug, description, dpath string, existing *descriptor.Descriptor) int {
	var hooks []string
	for _, phase := range initHookPhases {
		for _, name := range phase {
			if spec.HookByName(&sp.Hooks, name) != nil {
				hooks = append(hooks, name)
			}
		}
	}
	attach := "depends on the registry"
	if existing != nil {
		attach = "rebuilt (a descriptor exists, no entry: the registry is rebuilt from it)"
	}
	lines := []string{
		fmt.Sprintf("would classify %s as %s", cls.WorktreeRoot, cls.Outcome),
		fmt.Sprintf("would resolve the slug %q from the directory basename", slug),
		fmt.Sprintf("would allocate the entry %s/%s (lowest free slot; an existing entry's slot is authoritative)", sp.App, slug),
		fmt.Sprintf("would materialise the resources: %s", strings.Join(resourceNames(sp), ", ")),
		fmt.Sprintf("would emit the descriptor at %s", dpath),
		fmt.Sprintf("would run the hooks: %s", strings.Join(hooks, ", ")),
		fmt.Sprintf("would activate the entry (attach outcome: %s)", attach),
	}
	if description != "" {
		lines = append([]string{fmt.Sprintf("would record the description %q", description)}, lines...)
	}
	if jsonOut {
		if err := WriteJSON(stdout, map[string]any{
			"dry_run": true, "app": sp.App, "slug": slug,
			"worktree": cls.WorktreeRoot, "attach": attach,
			"resources": resourceNames(sp), "hooks": hooks,
			"descriptor": dpath,
		}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	for _, l := range lines {
		fmt.Fprintln(stderr, l)
	}
	fmt.Fprintln(stdout, "dry run: nothing was changed.")
	return ExitOK
}

// resourceNames lists the spec's resource names in declaration order.
func resourceNames(sp *spec.Spec) []string {
	names := make([]string, 0, len(sp.Resources))
	for i := range sp.Resources {
		names = append(names, sp.Resources[i].Name)
	}
	return names
}

// writeInitTable prints the init result in text form: the descriptor the
// worktree now carries.
func writeInitTable(stdout io.Writer, r *initResult) {
	fmt.Fprintf(stdout, "app: %s  slug: %s  slot: %d  state: %s  attach: %s\n", r.App, r.Slug, r.Slot, r.State, r.Attach)
	if r.Descriptor != nil {
		fmt.Fprintln(stdout)
		writeShowTable(stdout, r.Descriptor)
	}
}
