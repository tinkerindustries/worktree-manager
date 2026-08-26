package cli

// rm.go is `wt rm` (04-lifecycle.md §7, ARCHITECTURE.md §9.2): the safety
// checks in the client, then reap, teardown and deallocate in the
// coordinator, then `git worktree remove` — teardown first, git removal
// second, because teardown needs the registry entry the removal would
// orphan.
//
// The three safety checks read the working tree and run before anything is
// destroyed: uncommitted changes; unpushed commits via `git log @{u}..HEAD`
// — an absent upstream is itself a hit, not a pass; and an open PR, which
// needs gh.
//
// What a hit means is the repository's policy, not the tool's: the spec's
// `removal:` block sets each check to refuse (stop with exit 3, or exit 4
// when the check could not run) or warn (print it, record it as a note,
// continue). The defaults are refuse on uncommitted changes and warn on the
// other two, so a personal project with local-only branches and no gh
// installed can still remove a worktree, while the one hit nothing can
// recover still stops. `--strict` makes every check refuse for one run and
// `--force` makes every check warn for one run; they are the per-invocation
// override of a committed spec, and they cannot be given together.
//
// Neither flag reaches `git worktree remove`, which is never passed
// --force: the checks are advice about the tree, and git refusing is a fact
// about it.
//
// rm accepts a slug rather than only inferring its target from cwd, because
// the routine case is that the directory is already gone. It never picks a
// target on its own — "the most recent worktree" is not an inference this
// tool makes. The destruction rails: never --force a git worktree remove
// (git refusing is signal that a check missed something); never delete the
// worktree the caller is standing in; never delete the remote branch (the
// PR points at it).
//
// Partial states each get their own path (04-lifecycle.md §7.3): directory
// gone with an entry present is a registry-and-label-only teardown;
// directory present with no entry is a `git worktree remove` and nothing to
// deallocate; neither is a message and a stop.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/treecheck"
)

// rmResult is the one JSON object `wt rm --json` prints.
type rmResult struct {
	App  string `json:"app"`
	Slug string `json:"slug"`
	// Removed reports the teardown outcome: the entry dropped.
	Removed bool `json:"removed"`
	// WorktreeRemoved reports whether the git worktree was removed.
	WorktreeRemoved bool `json:"worktree_removed,omitempty"`
	// Reap is the reaper's bounded-coverage statement.
	Reap api.ReapReport `json:"reap"`
	// Notes are the bounded-coverage statements: skipped checks, skipped
	// git removal, and what survived.
	Notes []string `json:"notes,omitempty"`
	// Purged names the state-path resources whose stores this run
	// deleted. Empty means every state store survived.
	Purged []string `json:"purged,omitempty"`
	// Warnings are the safety checks that hit and were warned past
	// because the removal policy said warn. They are printed as they
	// happen, before anything is destroyed, and carried here so --json
	// sees them too.
	Warnings []string `json:"warnings,omitempty"`
}

// runRm implements `wt rm [--json] [--dry-run] [--cwd <dir>] [--slug <s>]
// [--keep-processes] [--strict|--force] [--purge <resource>]... [--keep-vm]
// [--purge-db]`.
//
// Two kinds of flag come from the spec rather than from rm: the machine
// resources' keep_flag names (B4.3) and the state-path resources'
// purge.flag names (03-drivers.md §4.4). Both are registered after the
// spec loads, so a spec that declares keep_flag: "--keep-vm" makes
// `wt rm --keep-vm` legal and one that declares purge.flag: "--purge-db"
// makes `wt rm --purge-db` legal; a spec without one refuses the flag as
// undefined. A declared flag whose name is already one of rm's own is
// refused, because registering it twice would panic the flag package.
//
// `--purge <resource>` is the other spelling of the same thing, naming the
// resource rather than its flag. Both resolve to the flag strings the
// state-path driver matches on, and a value matching no purgeable resource
// is a usage error: a --purge that selected nothing used to exit 0 having
// deleted nothing.
func runRm(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "preview what would be torn down and change nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	slug := fs.String("slug", "", "the worktree's slug (default: the cwd's basename when cwd is a worktree)")
	keepProcesses := fs.Bool("keep-processes", false, "do not signal processes bound to the worktree's ports")
	strict := fs.Bool("strict", false, "run every safety check as a refusal for this run, whatever the spec says")
	force := fs.Bool("force", false, "downgrade every safety check to a warning for this run; git worktree remove is still never forced")
	var purgeValues []string
	fs.Var(stringList(&purgeValues), "purge", "purge this state-path resource's store on teardown, by resource name (repeatable)")

	// The spec must load before the keep flags can be registered, and the
	// spec's location depends on --cwd, so the flag's value is pre-scanned
	// from the raw args in both spellings the flag package accepts.
	dir := *cwd
	if v := preScanFlag(args, "cwd"); v != "" {
		dir = v
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("resolving %s: %v", dir, err), ""))
		return ExitFailure
	}

	// The target: an explicit slug, or the cwd's worktree — never an
	// inference of "the most recent worktree" (04-lifecycle.md §7.1). The
	// slug's value is pre-scanned so the target check keeps its place
	// before the spec load, which the keep-flag registration needs.
	targetSlug := preScanFlag(args, "slug")
	if targetSlug == "" {
		cls, err := identity.Classify(absDir, identity.StandaloneDeclared())
		if err != nil || (cls.Outcome != identity.LinkedWorktree && cls.Outcome != identity.StandaloneClone) {
			e := New(ExitUsage,
				"no target: give --slug <slug>, or run rm from inside the repository",
				"run 'wt rm --slug <slug>' from the main checkout (or any other directory)")
			WriteError(stderr, e)
			return e.Code
		}
		targetSlug = identity.DefaultSlug(cls.WorktreeRoot)
	}

	sp, code := loadSpec(absDir, stderr)
	if code != ExitOK {
		return code
	}

	// The spec-declared flags: one boolean flag per machine keep_flag and
	// one per state-path purge.flag, deduplicated (two resources may
	// declare the same flag name).
	keepDeclared := declaredKeepFlags(sp)
	purgeDeclared := declaredPurgeFlags(sp)
	if e := registerSpecFlags(fs, keepDeclared, "keep_flag",
		"keep the VM up: tear down the containers and the entry but leave the instance running"); e != nil {
		WriteError(stderr, e)
		return e.Code
	}
	if e := registerSpecFlags(fs, purgeDeclared, "purge.flag",
		"purge this state-path resource's store on teardown"); e != nil {
		WriteError(stderr, e)
		return e.Code
	}

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt rm --slug <slug>' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}
	if *strict && *force {
		WriteError(stderr, UsageError(
			"give one of --strict or --force, not both",
			"--strict makes every safety check refuse and --force makes every one warn: the two ask for opposite things"))
		return ExitUsage
	}
	pol := removalPolicy{spec: &sp.Removal, strict: *strict, force: *force}

	keepPassed := passedSpecFlags(fs, keepDeclared)
	// The two spellings of a purge — the declared flag and --purge <name>
	// — resolve to the one list of flag strings the state-path driver
	// matches on.
	resolvedPurge, perr := resolvePurgeValues(purgeValues, purgeDeclared)
	if perr != nil {
		WriteError(stderr, perr)
		return perr.Code
	}
	purgeFlags := mergeFlags(passedSpecFlags(fs, purgeDeclared), resolvedPurge)
	purgeNames := purgeTargets(purgeDeclared, purgeFlags)
	// The parsed --slug is authoritative once Parse has run; the pre-scan
	// only existed to place the target check before the spec load.
	if *slug != "" {
		targetSlug = *slug
	}

	if reason := identity.ValidateSlug(targetSlug); reason != "" {
		e := New(ExitRefused,
			fmt.Sprintf("slug %q is not valid (%s)", targetSlug, reason),
			"give a slug matching ^[a-z0-9][a-z0-9-]*$ (at most 32 characters), then re-run")
		WriteError(stderr, e)
		return e.Code
	}

	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()

	// Phase one: prepare — ask the coordinator for the entry (its path and
	// its resources) and the reap preview, changing nothing (the reap runs
	// in dry-run, so nothing is signalled).
	prep, perr := rmRequest(sess, sp, targetSlug, *keepProcesses, true, purgeFlags, keepPassed)
	if perr != nil {
		WriteError(stderr, perr)
		return perr.Code
	}
	if !prep.EntryFound {
		return rmNoEntry(stdout, stderr, *jsonOut, sp, targetSlug, absDir, pol)
	}

	// Never delete the worktree the caller is standing in (B11.11): the
	// classification detects it directly.
	if prep.Path != "" {
		if identity.Contains(prep.Path, absDir) {
			e := New(ExitRefused,
				fmt.Sprintf("refusing to remove %s: the caller is standing inside it — remove a worktree from outside it", prep.Path),
				fmt.Sprintf("run 'wt rm --slug %s' from the main checkout or another directory", targetSlug))
			WriteError(stderr, e)
			return e.Code
		}
	}

	// The safety checks read the working tree; a tree whose directory is
	// already gone has nothing to protect, and the checks are skipped with
	// the bound stated. Otherwise any hit stops with exit 3.
	var notes []string
	var warnings []string
	targetRoot := prep.Path
	targetExists := false
	if targetRoot != "" {
		if fi, serr := os.Stat(targetRoot); serr == nil && fi.IsDir() {
			targetExists = true
		}
	}
	if !targetExists {
		notes = append(notes, fmt.Sprintf("the worktree directory %s is already gone; the tree-reading safety checks were skipped, and the teardown is registry-and-label-only", targetRoot))
	} else {
		code, warned := rmSafetyChecks(targetRoot, stderr, pol)
		if code != ExitOK {
			return code
		}
		warnings = warned
	}

	// Phase two: the real thing — reap and teardown in the coordinator.
	if *dryRun {
		// The prepare call was the preview; print it and stop.
		return rmPrintPreview(stdout, stderr, *jsonOut, sp, targetSlug, prep, targetExists, notes, purgeNames, purgeDeclared)
	}
	res, rerr := rmRequest(sess, sp, targetSlug, *keepProcesses, false, purgeFlags, keepPassed)
	if rerr != nil {
		WriteError(stderr, rerr)
		return rerr.Code
	}
	// Teardown done: the git half — `git worktree remove`, never --force.
	// A standalone clone is not a git worktree and has no registration to
	// remove; its directory is left in place and the note says so.
	worktreeRemoved := false
	if res.Removed && targetExists {
		if isStandalone(targetRoot) {
			notes = append(notes, fmt.Sprintf("%s is a standalone clone, not a git worktree: its directory is left in place (remove it yourself if it was disposable)", targetRoot))
		} else if code := gitWorktreeRemove(targetRoot, stderr); code == ExitOK {
			worktreeRemoved = true
		} else {
			// git refusing is signal that a check missed something
			// (B11.11): the error is reported as itself, never forced.
			// The notes still have to be said: this is the one exit
			// between the teardown and the report.
			for _, n := range notes {
				fmt.Fprintf(stderr, "note: %s\n", n)
			}
			return code
		}
	}

	// What a teardown deleted is not recoverable, so the report names it
	// rather than leaving "removed" to cover both outcomes.
	if res.Removed {
		notes = append(notes, "purged the state stores: "+purgeSummary(purgeNames, purgeDeclared))
	}
	writeRmReport(stdout, stderr, *jsonOut, rmResult{
		App: sp.App, Slug: targetSlug, Removed: res.Removed,
		WorktreeRemoved: worktreeRemoved,
		Reap:            res.Reap,
		Purged:          purgeNames,
		Notes:           notes,
		Warnings:        warnings,
	})
	return ExitOK
}

// rmRequest runs one rm call and decodes the result.
func rmRequest(sess *coordClient, sp *spec.Spec, slug string, keepProcesses, dryRun bool, purgeFlags, keepFlags []string) (*api.RmResult, *Error) {
	res, rerr := sess.client.Rm(&api.RmArgs{
		App: sp.App, Slug: slug, Spec: *sp,
		KeepProcesses: keepProcesses, DryRun: dryRun, PurgeFlags: purgeFlags, KeepFlags: keepFlags,
	})
	if rerr != nil {
		return nil, requestErr(sess.endpoint, api.VerbRm, rerr)
	}
	return res, nil
}

// rmNoEntry handles the two no-entry paths of 04-lifecycle.md §7.3:
// directory present (a `git worktree remove` and nothing to deallocate) or
// neither (a message and a stop).
func rmNoEntry(stdout, stderr io.Writer, jsonOut bool, sp *spec.Spec, slug, callerDir string, pol removalPolicy) int {
	// Locate the tree by slug from the caller's repository: the slug is
	// the directory basename, and `git worktree list` names every tree.
	root := ""
	if out, err := runGitOutput(callerDir, "worktree", "list", "--porcelain"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(line, "worktree "); ok && filepath.Base(rest) == slug {
				root = rest
				break
			}
		}
	}
	if root == "" {
		e := New(ExitFailure,
			fmt.Sprintf("no registry entry for %s/%s and no git worktree named %q in this repository: nothing to remove", sp.App, slug, slug),
			"check the slug with 'wt list' or 'git worktree list', then re-run")
		WriteError(stderr, e)
		return e.Code
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		e := New(ExitFailure,
			fmt.Sprintf("no registry entry for %s/%s and the worktree directory %s is already gone: nothing to remove", sp.App, slug, root),
			"check the slug with 'wt list' or 'git worktree list', then re-run")
		WriteError(stderr, e)
		return e.Code
	}
	// Directory present, no entry: the safety checks still run — a tree is
	// about to be removed — then `git worktree remove`, nothing to
	// deallocate.
	code, warned := rmSafetyChecks(root, stderr, pol)
	if code != ExitOK {
		return code
	}
	if code := gitWorktreeRemove(root, stderr); code != ExitOK {
		return code
	}
	writeRmReport(stdout, stderr, jsonOut, rmResult{
		App: sp.App, Slug: slug, Removed: false,
		WorktreeRemoved: true,
		Warnings:        warned,
		Notes: []string{
			fmt.Sprintf("no registry entry for %s/%s: the git worktree was removed and nothing was deallocated", sp.App, slug)},
	})
	return ExitOK
}

// specFlag is one CLI flag a spec resource declares — a machine's
// keep_flag or a state-path's purge.flag — paired with the resource that
// declared it, so a refusal can name the resource and not only the flag.
type specFlag struct {
	flag     string // as the spec spells it, e.g. "--purge-db"
	resource string // the resource's name, e.g. "db"
}

// declaredKeepFlags collects the machine resources' keep_flag names.
func declaredKeepFlags(sp *spec.Spec) []specFlag {
	return declaredFlags(sp, "machine", func(r *spec.Resource) string {
		if r.KeepFlag == nil {
			return ""
		}
		return *r.KeepFlag
	})
}

// declaredPurgeFlags collects the state-path resources' purge.flag names.
// A state-path resource without a purge block declares nothing, and is
// therefore not purgeable at all.
func declaredPurgeFlags(sp *spec.Spec) []specFlag {
	return declaredFlags(sp, "state-path", func(r *spec.Resource) string {
		if r.Purge == nil {
			return ""
		}
		return r.Purge.Flag
	})
}

// declaredFlags reads one flag field off every resource of the given type,
// in spec order and deduplicated by flag string: two resources may name
// the same flag, and one flag is one CLI flag.
func declaredFlags(sp *spec.Spec, typ string, pick func(*spec.Resource) string) []specFlag {
	var out []specFlag
	seen := map[string]bool{}
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Type != typ {
			continue
		}
		f := pick(r)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, specFlag{flag: f, resource: r.Name})
	}
	return out
}

// registerSpecFlags registers one boolean flag per spec-declared flag. A
// declared flag whose name is already one of rm's own is refused rather
// than registered: the flag package panics on a redefinition, and a
// committed spec must not be able to crash the binary. field names the
// spec field in the refusal so the fix lands in the right place.
func registerSpecFlags(fs *flag.FlagSet, declared []specFlag, field, usage string) *Error {
	for _, df := range declared {
		name := strings.TrimPrefix(df.flag, "--")
		if fs.Lookup(name) != nil {
			return UsageError(
				fmt.Sprintf("rename %s on resource %q in wt.yaml: it may not be one of rm's own flags", field, df.resource),
				"resource %q declares %s %q, which is already an rm flag", df.resource, field, df.flag)
		}
		fs.BoolVar(new(bool), name, false, usage)
	}
	return nil
}

// passedSpecFlags returns the declared flags the caller actually passed,
// in the spec's order.
func passedSpecFlags(fs *flag.FlagSet, declared []specFlag) []string {
	var out []string
	for _, df := range declared {
		if flagValue(fs, strings.TrimPrefix(df.flag, "--")) {
			out = append(out, df.flag)
		}
	}
	return out
}

// resolvePurgeValues turns the values of --purge into the flag strings the
// state-path driver matches on. A value names a resource — which is what
// the flag reads as — or spells the flag that resource declares, which is
// the only form the flag accepted before it validated anything. Any other
// value is a usage error naming what this repository can purge: a --purge
// matching no resource used to exit 0 having deleted nothing.
func resolvePurgeValues(values []string, declared []specFlag) ([]string, *Error) {
	var out []string
	for _, v := range values {
		matched := ""
		for _, df := range declared {
			if v == df.resource || v == df.flag || v == strings.TrimPrefix(df.flag, "--") {
				matched = df.flag
				break
			}
		}
		if matched == "" {
			return nil, UsageError(purgeRemedy(declared),
				"--purge %s names nothing this repository can purge", v)
		}
		out = append(out, matched)
	}
	return out, nil
}

// purgeRemedy names the resources that declare a purge flag.
func purgeRemedy(declared []specFlag) string {
	if len(declared) == 0 {
		return "no state-path resource in this repository's wt.yaml declares a purge: block, so rm has nothing to purge"
	}
	var names []string
	for _, df := range declared {
		names = append(names, fmt.Sprintf("%s (or %s)", df.resource, df.flag))
	}
	return "pass one of: " + strings.Join(names, ", ")
}

// purgeTargets names the state-path resources the given purge flags
// select, so rm can report what it deleted in the words the spec uses.
func purgeTargets(declared []specFlag, purgeFlags []string) []string {
	var out []string
	for _, df := range declared {
		for _, f := range purgeFlags {
			if f == df.flag {
				out = append(out, df.resource)
				break
			}
		}
	}
	return out
}

// purgeSummary renders the purge decision as one diagnostic phrase, saying
// plainly that nothing is deleted when no flag was passed.
func purgeSummary(names []string, declared []specFlag) string {
	if len(names) > 0 {
		return strings.Join(names, ", ")
	}
	if len(declared) == 0 {
		return "none — no state-path resource in this repository declares a purge flag"
	}
	return "none — every state store survives the teardown (" + purgeRemedy(declared) + ")"
}

// mergeFlags concatenates flag lists, dropping duplicates and keeping the
// order of first occurrence.
func mergeFlags(lists ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, f := range l {
			if seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// rmMaxLines caps how many changed paths or unpushed commits a refusal
// names.
const rmMaxLines = 5

// removalPolicy answers, for one check, whether a hit refuses or warns. The
// spec is the repository's committed policy; --strict and --force are the
// caller's per-invocation override of it, in the two directions.
type removalPolicy struct {
	spec   *spec.Removal
	strict bool
	force  bool
}

// refuses reports whether a hit on the named check stops rm.
func (p removalPolicy) refuses(check string) bool {
	switch {
	case p.strict:
		return true
	case p.force:
		return false
	}
	return spec.RemovalPolicy(p.spec, check) == spec.RemovalRefuse
}

// rmSafetyChecks runs the three tree-reading checks, in order, and returns
// the exit code and the warnings the warned-past checks left: 0 when
// nothing refused, 3 on a hit against a refusing check, 4 when a refusing
// check could not run (git broken, or gh missing or unauthenticated — a
// safety check that cannot run fails closed). The checks themselves live in
// internal/treecheck; what is here is rm's policy on each answer, and the
// policy is the repository's (see removalPolicy).
//
// A warned check prints "warning: ..." on stderr as it happens, so it is
// visible before anything is destroyed, and returns the same sentence so
// --json carries it too. Bounded coverage is stated either way: a check
// that warned is a check whose answer was ignored.
func rmSafetyChecks(root string, stderr io.Writer, pol removalPolicy) (int, []string) {
	var warnings []string
	// hit records one check's finding. fact is what the check found, in
	// neutral words, so the refusal and the warning can each frame it.
	// It returns false when rm must stop.
	hit := func(check string, code int, fact, remedy string) bool {
		if pol.refuses(check) {
			WriteError(stderr, New(code, fmt.Sprintf("refusing to remove %s: %s", root, fact), remedy))
			return false
		}
		w := fmt.Sprintf("%s: %s — removing it anyway, because removal.%s is %q", root, fact, check, spec.RemovalWarn)
		fmt.Fprintf(stderr, "warning: %s\n", w)
		warnings = append(warnings, w)
		return true
	}

	// 1. Uncommitted changes. Refusing is the default: uncommitted changes
	// are the one hit nothing else can recover, and `git worktree remove`
	// will refuse anyway, since it is never forced.
	res, err := treecheck.Uncommitted(root, treecheck.Git)
	switch {
	case err != nil:
		if !hit("uncommitted", ExitUnavailable,
			fmt.Sprintf("the uncommitted-changes check could not run (%v)", err),
			"fix git, then re-run rm") {
			return ExitUnavailable, nil
		}
	case !res.OK():
		if !hit("uncommitted", ExitRefused,
			fmt.Sprintf("it has uncommitted changes (%d changed path(s), first: %s)", len(res.Lines), res.First(rmMaxLines)),
			"commit or stash the changes, then re-run rm (or 'wt rm --force' to warn past every check for one run)") {
			return ExitRefused, nil
		}
	}

	// 2. Unpushed commits: `git log @{u}..HEAD`. An absent upstream is
	// itself a hit, not a pass (B11.10) — a local-only branch is the
	// ordinary shape of a personal project, which is why the default
	// policy warns rather than refuses.
	res, err = treecheck.Unpushed(root, treecheck.Git)
	switch {
	case errors.Is(err, treecheck.ErrNoUpstream):
		if !hit("unpushed", ExitRefused,
			"the branch has no upstream, and an absent upstream is itself a stop, not a pass",
			"push the branch (git push -u origin <branch>) or set an upstream, then re-run rm") {
			return ExitRefused, nil
		}
	case err != nil:
		if !hit("unpushed", ExitUnavailable,
			fmt.Sprintf("the unpushed-commits check could not run (%v)", err),
			"fix git, then re-run rm") {
			return ExitUnavailable, nil
		}
	case !res.OK():
		if !hit("unpushed", ExitRefused,
			fmt.Sprintf("it has %d unpushed commit(s), first: %s", len(res.Lines), res.First(rmMaxLines)),
			"push the commits, then re-run rm") {
			return ExitRefused, nil
		}
	}

	// 3. An open PR. gh missing or unauthenticated is a check that cannot
	// run, and under a refusing policy rm fails closed with exit 4.
	if code, fact := checkOpenPR(root); code != ExitOK {
		if !hit("open_pr", code, fact, ghRemedy(code, fact)) {
			return code, nil
		}
	}
	return ExitOK, warnings
}

// checkOpenPR asks gh whether the branch has an open PR. It returns
// (ExitOK, "") when there is none and when the PR has already merged or
// closed — that is the end of a branch's life, and rm is how the worktree
// goes with it. Otherwise the exit code and what it found, in neutral
// words the caller frames: 3 when a PR is open, 4 when the check could not
// run.
func checkOpenPR(root string) (int, string) {
	pr, err := treecheck.PRState(root, treecheck.Gh)
	if err != nil {
		var ue *treecheck.UnavailableError
		if errors.As(err, &ue) {
			switch {
			case ue.NotInstalled:
				return ExitUnavailable, "the open-PR check cannot run: gh is not installed or not on PATH"
			case ue.NotAuthenticated:
				return ExitUnavailable, fmt.Sprintf("the open-PR check could not run: gh is not authenticated (%s)", ue.Detail)
			}
			return ExitUnavailable, fmt.Sprintf("the open-PR check could not run: gh failed (%s)", ue.Detail)
		}
		return ExitUnavailable, fmt.Sprintf("the open-PR check could not run: %v", err)
	}
	if pr.Open() {
		return ExitRefused, fmt.Sprintf("the branch has an open PR #%d (%s); the PR points at the branch, and the remote branch is never deleted", pr.Number, pr.State)
	}
	return ExitOK, ""
}

// ghRemedy names the fix for each gh failure.
func ghRemedy(code int, detail string) string {
	switch code {
	case ExitRefused:
		return "close or merge the PR, then re-run rm"
	case ExitUnavailable:
		if strings.Contains(detail, "not installed") {
			return "install the GitHub CLI (brew install gh / apt install gh), then re-run rm — or set removal.open_pr: warn in wt.yaml if this repository has no pull requests"
		}
		if strings.Contains(detail, "not authenticated") {
			return "run 'gh auth login', then re-run rm — or set removal.open_pr: warn in wt.yaml"
		}
		return "fix gh (the error above names the problem), then re-run rm"
	}
	return ""
}

// gitWorktreeRemove runs `git worktree remove <root>`, never with --force:
// git refusing is signal that a check missed something (B11.11), and the
// refusal is reported as itself.
func gitWorktreeRemove(root string, stderr io.Writer) int {
	if err := treecheck.WorktreeRemove(root, treecheck.Git); err != nil {
		e := New(ExitFailure,
			fmt.Sprintf("git worktree remove refused to remove %s: %s — git refusing indicates a safety check missed something; nothing was forced",
				root, err),
			"resolve what git names, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}
	return ExitOK
}

// isStandalone reports whether the tree classifies as a standalone clone.
func isStandalone(root string) bool {
	cls, err := identity.Classify(root, identity.StandaloneDeclared())
	return err == nil && cls.Outcome == identity.StandaloneClone
}

// rmPrintPreview prints what the real rm would do, changing nothing.
func rmPrintPreview(stdout, stderr io.Writer, jsonOut bool, sp *spec.Spec, slug string, prep *api.RmResult, targetExists bool, notes []string, purgeNames []string, purgeDeclared []specFlag) int {
	note := "dry run: nothing was changed."
	for _, n := range notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	fmt.Fprintf(stderr, "would reap: %s\n", reapSummary(&prep.Reap))
	fmt.Fprintf(stderr, "would tear down the resources: %s\n", strings.Join(prep.Resources, ", "))
	// Tearing a state path down and deleting its store are different
	// things, and the preview says which one this run would do: without a
	// purge flag every state store survives, which is what the teardown
	// line alone used to read as.
	fmt.Fprintf(stderr, "would purge the state stores: %s\n", purgeSummary(purgeNames, purgeDeclared))
	if targetExists && !isStandalone(prep.Path) {
		fmt.Fprintf(stderr, "would run: git worktree remove %s\n", prep.Path)
	}
	if jsonOut {
		if err := WriteJSON(stdout, map[string]any{
			"dry_run": true, "app": sp.App, "slug": slug,
			"reap": prep.Reap, "resources": prep.Resources,
			"worktree": prep.Path, "purged": purgeNames,
		}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintln(stdout, note)
	return ExitOK
}

// reapSummary renders the reaper's report as one diagnostic line.
func reapSummary(r *api.ReapReport) string {
	switch {
	case r.KeptProcesses:
		return r.Note
	case !r.Available:
		return r.Note
	case len(r.Signalled) == 0 && len(r.Holders) == 0:
		return "no process bound to this worktree's ports"
	default:
		var parts []string
		for _, a := range r.Signalled {
			parts = append(parts, fmt.Sprintf("%s(%d, %s)", a.Signal, a.PID, a.Command))
		}
		for _, h := range r.Holders {
			parts = append(parts, fmt.Sprintf("reported %s(%d) — %s", h.Command, h.PID, h.Reason))
		}
		return strings.Join(parts, "; ")
	}
}

// writeRmReport prints the final rm result.
func writeRmReport(stdout, stderr io.Writer, jsonOut bool, r rmResult) int {
	for _, n := range r.Notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	fmt.Fprintf(stderr, "reap: %s\n", reapSummary(&r.Reap))
	if jsonOut {
		if err := WriteJSON(stdout, r); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	if r.WorktreeRemoved {
		fmt.Fprintf(stdout, "removed %s/%s: the git worktree is gone", r.App, r.Slug)
		if r.Removed {
			fmt.Fprintf(stdout, " and the registry entry was dropped")
		}
		fmt.Fprintln(stdout)
	} else if r.Removed {
		fmt.Fprintf(stdout, "removed %s/%s: the registry entry was dropped\n", r.App, r.Slug)
	} else {
		fmt.Fprintf(stdout, "rm of %s/%s: nothing was removed (see the notes above)\n", r.App, r.Slug)
	}
	return ExitOK
}

// runGitOutput runs one git command with cwd and returns its stdout.
func runGitOutput(cwd string, args ...string) (string, error) {
	out, err := treecheck.Git(cwd, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// preScanFlag finds a flag's value in the raw args before the flag package
// parses them, in both spellings the flag package accepts ("--cwd dir" and
// "--cwd=dir", with one or two dashes). It exists so rm can load the spec
// — and with it the machine keep flags the spec declares — before parsing.
// An absent flag returns "".
func preScanFlag(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, prefix := range []string{"--" + name + "=", "-" + name + "="} {
			if v, ok := strings.CutPrefix(a, prefix); ok {
				return v
			}
		}
		if a == "--"+name || a == "-"+name {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

// flagValue reads a boolean flag's value after Parse.
func flagValue(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	b, _ := f.Value.(flag.Getter).Get().(bool)
	return b
}
