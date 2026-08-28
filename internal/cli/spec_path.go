package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// runSpecPath implements `wt spec path --slug S [--home <path>]
// [--root <path>]`: the one place the repository's `worktrees.path`
// template is turned into a path, printed bare on stdout so the caller
// that is about to create the tree can use it directly.
//
// It is a pure spec derivation like `spec explain`, and it contacts
// nothing: the location is the repository's convention, not an
// allocation. The verb exists so the generated worktree-create skill
// computes the path instead of reasoning about a template — the answer is
// the same one every time, whoever asks.
func runSpecPath(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spec path", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slug := fs.String("slug", "", "the worktree slug (^[a-z0-9][a-z0-9-]*$, at most 32)")
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
	if !spec.ValidSlug(*slug) {
		WriteError(stderr, UsageError(
			fmt.Sprintf("give a kebab-case slug of at most %d characters, e.g. --slug brisk-otter", spec.SlugMaxLen),
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
	fmt.Fprintln(stdout, path)
	return ExitOK
}
