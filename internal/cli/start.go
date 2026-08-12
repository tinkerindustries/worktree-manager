package cli

// start.go is `wt start` (ARCHITECTURE.md §6.1): run the hooks that bring
// the stack up — start, seed, health — in a worktree that is already
// initialised. It does not allocate and does not emit: the resources come
// from the descriptor, the hooks are the repo's own commands, and the
// coordinator is never contacted (start is one of the two local verbs; the
// other is show).
//
// There is no `stop` verb and there will not be one: `rm` tears down, and
// there is no way to release a worktree's resources while keeping its
// entry (PLAN-SCOPE.md, "A stop verb").

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// startHooks are the hooks start runs, in order: the bring-up phase of
// init's step 7.
var startHooks = []string{"start", "seed", "health"}

// startResult is the one JSON object `wt start --json` prints.
type startResult struct {
	App      string   `json:"app"`
	Slug     string   `json:"slug"`
	Slot     int      `json:"slot"`
	HooksRun []string `json:"hooks_run"`
	Warnings []string `json:"warnings,omitempty"`
}

// runStart implements `wt start [--json] [--dry-run] [--cwd <dir>]
// [--param <name>=<value>]...`.
func runStart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "print the resolved commands and run nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	explicit := map[string]string{}
	fs.Var(stringMapFlag(explicit), "param", "hook parameter value, --param <name>=<value> (repeatable)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt start' with no positional arguments",
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
	sp, code := loadSpec(cls.WorktreeRoot, stderr)
	if code != ExitOK {
		return code
	}

	dpath := identity.DescriptorPath(cls.WorktreeRoot, sp.Emit.Descriptor)
	d, err := readDescriptorIfPresent(dpath, sp.Emit.Descriptor.Format)
	if err != nil {
		e := New(ExitFailure, err.Error(),
			"delete or fix the descriptor, then re-run wt init")
		WriteError(stderr, e)
		return e.Code
	}
	if d == nil {
		e := New(ExitUnavailable,
			fmt.Sprintf("no descriptor at %s: this linked worktree has not been initialised — run: wt init", dpath),
			"run: wt init")
		WriteError(stderr, e)
		return e.Code
	}

	ctx := spec.Context{
		App: sp.App, Slug: d.Slug, Slot: d.Slot,
		Home: clientHome(), Worktree: cls.WorktreeRoot,
	}
	runner := newHookRunner(sp, ctx, d.Resources, cls.WorktreeRoot, stderr, *dryRun)
	runner.setExplicit(explicit)
	runner.setPersisted(stickyParamsOf(d))
	if sp.Emit.Env != nil {
		keys, kerr := resolvedEnvKeys(sp, ctx, d.Resources)
		if kerr != nil {
			WriteError(stderr, New(ExitFailure, kerr.Error(), "fix the spec's emit.env keys, then re-run wt start"))
			return ExitFailure
		}
		runner.envKeys = keys
	}

	var hooksRun []string
	for _, name := range startHooks {
		hook := hookByName(&sp.Hooks, name)
		if hook == nil {
			continue
		}
		hooksRun = append(hooksRun, name)
		var herr error
		if name == "health" {
			herr = runner.runHealth(hook)
		} else {
			herr = runner.runHook(name, hook)
		}
		if herr != nil {
			// The worktree stays allocated and usable; the stack did not
			// come up, and the failure says what failed.
			e := New(ExitFailure, herr.Error(),
				"fix the cause named above, then re-run wt start")
			WriteError(stderr, e)
			return e.Code
		}
	}

	// Persist any sticky parameter chosen this run back into the
	// descriptor (04-lifecycle.md §5.2).
	if len(runner.chosenValues()) > 0 {
		d.Extras = withStickyParams(d.Extras, runner.chosenValues())
		if err := descriptor.Write(dpath, sp.Emit.Descriptor.Format, d); err != nil {
			fmt.Fprintf(stderr, "warning: persisting the chosen hook parameters into %s failed: %v; the choice is not recorded and will be re-made next run\n", dpath, err)
		}
	}

	result := startResult{
		App: sp.App, Slug: d.Slug, Slot: d.Slot,
		HooksRun: hooksRun,
	}
	if *jsonOut {
		if err := WriteJSON(stdout, result); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "started %s/%s (slot %d): hooks %s\n", sp.App, d.Slug, d.Slot, joinList(hooksRun))
	return ExitOK
}

// joinList renders a list for the text output.
func joinList(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
