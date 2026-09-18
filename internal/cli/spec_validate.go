package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// runSpecValidate implements `wt spec validate [path]`: parse and validate,
// exit 0 when clean, non-zero naming the field and the reason when not. The
// path defaults to walking up from cwd to the worktree root and stopping
// there — never above it, the rule every later phase depends on.
func runSpecValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spec validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 1 {
		WriteError(stderr, UsageError(
			"run 'wt spec validate [path]' with at most one path",
			"too many arguments: %v", fs.Args()))
		return ExitUsage
	}

	path := "."
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	specPath, err := resolveSpecPath(path)
	if err != nil {
		return failValidate(stdout, stderr, *jsonOut, "", err)
	}

	data, err := os.ReadFile(specPath)
	if err != nil {
		return failValidate(stdout, stderr, *jsonOut, "",
			fmt.Errorf("reading %s: %w", specPath, err))
	}
	parsed, err := spec.Parse(data)
	if err == nil {
		err = spec.Validate(parsed)
	}
	if err != nil {
		return failValidate(stdout, stderr, *jsonOut, specPath, err)
	}

	if *jsonOut {
		if err := WriteJSON(stdout, validateResult{Valid: true, Path: specPath}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "valid: %s\n", specPath)
	return ExitOK
}

// validateResult is the one JSON object `wt spec validate --json` prints.
type validateResult struct {
	Valid bool   `json:"valid"`
	Path  string `json:"path,omitempty"`
	Field string `json:"field,omitempty"`
	// Reason is omitted on success; on failure it carries the refusal.
	Reason string `json:"reason,omitempty"`
}

// resolveSpecPath turns the verb's argument into the spec file to read: a
// file is used as given, a directory is the start of the walk-up.
func resolveSpecPath(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", UsageError(
			"give a path that exists: 'wt spec validate <path>'",
			"%s: %v", path, err)
	}
	if fi.IsDir() {
		return spec.FindSpecPath(path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", New(ExitFailure, fmt.Sprintf("resolving %s: %v", path, err), "")
	}
	return abs, nil
}

// failValidate reports a refused or unreadable spec. The JSON result goes to
// stdout under --json — it is the verb's answer — and the human error is a
// diagnostic on stderr. The exit code is non-zero either way.
func failValidate(stdout, stderr io.Writer, jsonOut bool, specPath string, err error) int {
	var fe *spec.FieldError
	var nae *spec.NotAdoptedError
	var ce *Error
	switch {
	case errors.As(err, &fe):
		remedy := fmt.Sprintf("edit %s, then re-run: wt spec validate", specPath)
		if fe.Field != "" {
			remedy = fmt.Sprintf("edit %s to resolve %s, then re-run: wt spec validate", specPath, fe.Field)
		}
		WriteError(stderr, New(ExitFailure, fe.Error(), remedy))
		if jsonOut {
			WriteJSON(stdout, validateResult{Valid: false, Path: specPath, Field: fe.Field, Reason: fe.Reason})
		}
		return ExitFailure
	case errors.As(err, &nae):
		WriteError(stderr, New(ExitUnavailable, nae.Error(),
			"commit wt.yaml at the repository root (docs/design/03-drivers.md §3), or point at one: wt spec validate <path>"))
		if jsonOut {
			WriteJSON(stdout, validateResult{Valid: false, Reason: nae.Error()})
		}
		return ExitUnavailable
	case errors.As(err, &ce):
		WriteError(stderr, ce)
		if jsonOut {
			WriteJSON(stdout, validateResult{Valid: false, Reason: ce.Msg})
		}
		return ce.Code
	default:
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		if jsonOut {
			WriteJSON(stdout, validateResult{Valid: false, Reason: err.Error()})
		}
		return ExitFailure
	}
}
