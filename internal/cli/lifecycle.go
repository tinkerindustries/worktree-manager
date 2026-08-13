package cli

// lifecycle.go is the client-side surface the lifecycle verbs share: the
// classification with its exit codes, the spec load, the descriptor read
// with its exit codes, the sticky-params persistence, and the resolved
// emit.env keys the hooks run against. The verbs themselves (init, start,
// rm) live beside it.

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// lifecycleClassify classifies cwd for the lifecycle verbs and maps the
// outcomes onto the fixed exit codes: a primary checkout is refused with
// exit 3 (slot 0 is never managed, and the standalone declaration is named
// as the alternative if that is what the user meant — A2, 04-lifecycle.md
// §9), and a non-repository is exit 4 (required context unavailable).
func lifecycleClassify(cwd string, stderr io.Writer) (*identity.Classification, int) {
	cls, err := identity.Classify(cwd, identity.StandaloneDeclared())
	if err != nil {
		e := identityError(err)
		WriteError(stderr, e)
		return nil, e.Code
	}
	switch cls.Outcome {
	case identity.LinkedWorktree, identity.StandaloneClone:
		return cls, ExitOK
	case identity.PrimaryCheckout:
		e := New(ExitRefused,
			"the primary checkout is slot 0: never allocated, never managed, and never initialised",
			"run wt init from inside a linked worktree of the repository; if you meant a standalone clone, set WT_STANDALONE=1 (or clone the repository) and re-run")
		WriteError(stderr, e)
		return nil, e.Code
	default:
		e := New(ExitUnavailable,
			fmt.Sprintf("%s is not inside a git work tree", cwd),
			"run wt from inside a linked worktree or standalone clone of an adopted repository, then re-run")
		WriteError(stderr, e)
		return nil, e.Code
	}
}

// loadSpec finds and parses the committed spec from startDir (walking up
// to the worktree root and stopping — never above it). A repository that
// has not adopted the tooling is exit 4 naming the file.
func loadSpec(startDir string, stderr io.Writer) (*spec.Spec, int) {
	specPath, err := spec.FindSpecPath(startDir)
	if err != nil {
		var nae *spec.NotAdoptedError
		if errors.As(err, &nae) {
			e := New(ExitUnavailable, nae.Error(),
				"commit wt.yaml at the repository root (docs/design/03-drivers.md §3), then re-run")
			WriteError(stderr, e)
			return nil, e.Code
		}
		e := New(ExitFailure, err.Error(), "")
		WriteError(stderr, e)
		return nil, e.Code
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		e := New(ExitFailure, fmt.Sprintf("reading %s: %v", specPath, err), "")
		WriteError(stderr, e)
		return nil, e.Code
	}
	sp, err := spec.Parse(data)
	if err != nil {
		var fe *spec.FieldError
		if errors.As(err, &fe) {
			e := New(ExitFailure, fe.Error(), fmt.Sprintf("fix %s, then re-run", specPath))
			WriteError(stderr, e)
			return nil, e.Code
		}
		e := New(ExitFailure, err.Error(), "")
		WriteError(stderr, e)
		return nil, e.Code
	}
	// Validation happens here rather than only server-side: `wt start`
	// contacts no coordinator, so without this it would run hooks off an
	// unvalidated spec.
	if err := spec.Validate(sp); err != nil {
		e := New(ExitFailure, err.Error(), fmt.Sprintf("fix %s, then re-run", specPath))
		WriteError(stderr, e)
		return nil, e.Code
	}
	return sp, ExitOK
}

// readDescriptorIfPresent loads the worktree's descriptor, returning nil
// when it does not exist.
func readDescriptorIfPresent(path, format string) (*descriptor.Descriptor, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("checking %s: %w", path, err)
	}
	d, err := descriptor.Read(path, format)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return d, nil
}

// persistSticky writes the sticky parameters a run chose back into the
// descriptor (04-lifecycle.md §5.2), merged with the persisted set so one
// run's choices never drop another run's. A write failure is a warning:
// the choice is not recorded and will be re-made, which is a worse run
// rather than a failed one.
func persistSticky(d *descriptor.Descriptor, dpath, format string, chosen map[string]string, stderr io.Writer) {
	if len(chosen) == 0 {
		return
	}
	merged := stickyParamsOf(d)
	for k, v := range chosen {
		merged[k] = v
	}
	d.Extras = withStickyParams(d.Extras, merged)
	if err := descriptor.Write(dpath, format, d); err != nil {
		fmt.Fprintf(stderr, "warning: persisting the chosen hook parameters into %s failed: %v; the choice is not recorded and will be re-made next run\n", dpath, err)
	}
}

// buildHooks and startHooks split the spec's one hook list into the two
// phases the init sequence runs: install, prepull and build bring the tree
// to buildable (step 5); start, seed and health bring the stack up (step
// 7). `wt start` runs the second phase alone. Both come from
// spec.HookNames, so a seventh hook is declared in one place.
const startPhaseFirst = 3

var (
	buildHooks = spec.HookNames[:startPhaseFirst]
	startHooks = spec.HookNames[startPhaseFirst:]
)

// clientHome is the client's home directory — the best available {home}
// for templates the client resolves (the .env keys and the hook
// environment). A host client's home is the coordinator's home; a
// container client's differs, which is the documented cost of the client
// emitting into its own tree (05-delivery.md §3).
func clientHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// resolvedEnvKeys computes the emit.env keys against one allocation: the
// environment the delivery half emitted (05-delivery.md §3), which the
// hook runner exports to every hook process.
func resolvedEnvKeys(sp *spec.Spec, ctx spec.Context, resolved map[string]spec.Resolved) (map[string]string, error) {
	if sp.Emit.Env == nil {
		return nil, nil
	}
	keys := make(map[string]string, len(sp.Emit.Env.Keys))
	for name, template := range sp.Emit.Env.Keys {
		value, err := spec.Substitute(template, ctx, resolved)
		if err != nil {
			return nil, fmt.Errorf("resolving the emit.env key %s: %v", name, err)
		}
		keys[name] = value
	}
	return keys, nil
}

// stickyParamKey is the descriptor extras key the sticky hook parameters
// are persisted under (04-lifecycle.md §5.2: chosen on first run, persisted
// in the descriptor, reused by later runs).
const stickyParamKey = "sticky_params"

// stickyParamsOf extracts the persisted sticky parameter values from a
// descriptor's extras.
func stickyParamsOf(d *descriptor.Descriptor) map[string]string {
	out := map[string]string{}
	if d == nil || d.Extras == nil {
		return out
	}
	raw, ok := d.Extras[stickyParamKey].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// withStickyParams returns the extras map with the sticky params submap
// replaced by values, preserving every other extra.
func withStickyParams(extras map[string]any, values map[string]string) map[string]any {
	out := make(map[string]any, len(extras)+1)
	for k, v := range extras {
		out[k] = v
	}
	sub := make(map[string]any, len(values))
	for k, v := range values {
		sub[k] = v
	}
	out[stickyParamKey] = sub
	return out
}
