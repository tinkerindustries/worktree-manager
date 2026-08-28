package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// SpecPathResult is what `wt spec path --json` prints: everything the
// caller about to create a worktree needs, in one answer. The slug is here
// because --name may have changed it, and the caller has to use the same
// one for the branch, the directory and every later `wt rm`.
type SpecPathResult struct {
	Slug string `json:"slug"`
	Path string `json:"path"`
	Base string `json:"base"`
}

// runSpecPath implements `wt spec path (--slug S | --name N) [--json]
// [--home <path>] [--root <path>]`: the one place the repository's
// `worktrees:` block is turned into an answer about a specific worktree —
// where it goes, and what it branches from.
//
// It is a pure spec derivation like `spec explain`, and it contacts
// nothing: the location is the repository's convention, not an
// allocation. The verb exists so whatever creates the worktree — Claude
// Code's WorktreeCreate hook, or a person — computes the answer instead of
// reasoning about a template: the same one every time, whoever asks.
//
// The two input flags differ in who chose the name. --slug is for a caller
// that picked its own and wants anything else refused. --name is for a
// caller that was handed one: Claude Code names a worktree after the task
// that prompted it and appends a hash, and the WorktreeCreate hook is given
// that name rather than asked for one. Refusing there costs a person their
// worktree over a name nobody typed, and the remedy — pick a shorter
// name — is addressed to somebody who is not in the room. --name
// normalises deterministically (identity.NormaliseSlug) and says on stderr
// when it changed something.
//
// Bare stdout is the path, which is what a shell caller wants. --json adds
// the slug and the base, which is what the hook wants: one call, and the
// slug it must use downstream is in the answer rather than re-derived from
// the path's basename.
func runSpecPath(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spec path", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slug := fs.String("slug", "", fmt.Sprintf("the worktree slug (^[a-z0-9][a-z0-9-]*$, at most %d); refused if it is not already legal", spec.SlugMaxLen))
	name := fs.String("name", "", "a caller-supplied worktree name, normalised into a slug")
	jsonOut := fs.Bool("json", false, "print the slug, the path and the base revision as one JSON object")
	home := fs.String("home", "", "override {home} (default: the user's home directory)")
	root := fs.String("root", "", "the repository's main checkout, which a relative template resolves against (default: the spec's directory)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt spec path --slug S' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}
	if (*slug == "") == (*name == "") {
		WriteError(stderr, UsageError(
			"give --slug for a slug you chose, or --name for one you were handed",
			"give exactly one of --slug and --name"))
		return ExitUsage
	}
	if *name != "" {
		normalised, reason := identity.NormaliseSlug(*name)
		if reason != "" {
			WriteError(stderr, UsageError(
				"give a name with at least one letter or digit in it",
				"--name %q cannot be normalised: %s", *name, reason))
			return ExitUsage
		}
		// Said on stderr, never folded into stdout: a caller substituting a
		// name is the thing worth noticing, and stdout stays the answer.
		if normalised != *name {
			fmt.Fprintf(stderr, "note: normalised the name %q to the slug %q\n", *name, normalised)
		}
		*slug = normalised
	}
	if !spec.ValidSlug(*slug) {
		WriteError(stderr, UsageError(
			fmt.Sprintf("give a kebab-case slug of at most %d characters, e.g. --slug brisk-otter (or pass it as --name to have it normalised)", spec.SlugMaxLen),
			"--slug %q is not a valid slug (must match ^[a-z0-9][a-z0-9-]*$, at most %d characters)", *slug, spec.SlugMaxLen))
		return ExitUsage
	}

	specPath, err := spec.FindSpecPath(".")
	if err != nil {
		var nae *spec.NotAdoptedError
		if errors.As(err, &nae) {
			WriteError(stderr, New(ExitUnavailable, nae.Error(),
				"commit wt.yaml at the repository root (docs/design/03-drivers.md §3), or run from inside the repository"))
			return ExitUnavailable
		}
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("reading %s: %v", specPath, err), ""))
		return ExitFailure
	}
	parsed, err := spec.Parse(data)
	if err == nil {
		err = spec.Validate(parsed)
	}
	if err != nil {
		var fe *spec.FieldError
		if errors.As(err, &fe) {
			WriteError(stderr, New(ExitFailure, fe.Error(),
				fmt.Sprintf("edit %s, then re-run: wt spec path", specPath)))
			return ExitFailure
		}
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}

	// The spec sits at the repository root, so its directory is the main
	// checkout unless the caller names another one. Running this inside a
	// worktree would otherwise anchor the next tree under this one.
	if *root == "" {
		*root = filepath.Dir(specPath)
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("resolving %s: %v", *root, err), ""))
		return ExitFailure
	}
	if *home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			WriteError(stderr, New(ExitUnavailable,
				fmt.Sprintf("cannot determine the user home directory: %v", err),
				"pass --home <path> explicitly"))
			return ExitUnavailable
		}
		*home = h
	}

	path, err := spec.WorktreeLocation(parsed, abs, *slug, *home)
	if err != nil {
		WriteError(stderr, New(ExitFailure, err.Error(),
			fmt.Sprintf("edit %s, then re-run: wt spec path", specPath)))
		return ExitFailure
	}
	if *jsonOut {
		if err := WriteJSON(stdout, SpecPathResult{
			Slug: *slug, Path: path, Base: spec.WorktreeBase(parsed),
		}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintln(stdout, path)
	return ExitOK
}
