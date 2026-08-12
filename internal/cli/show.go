package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// runShow implements `wt show [--json] [--cwd <dir>]`: it reads the
// descriptor back from the working tree and prints it. Table by default,
// `--json` for consumers — every generated doc, skill and briefing points at
// this verb, so every value an agent might otherwise hardcode is in its
// output (05-delivery.md §7). It works with no coordinator installed and
// opens no socket.
//
// It says something useful in each of the cases it can distinguish: not a
// repository (exit 4); a repository with no spec, which means not adopted
// (exit 4); a primary checkout, which is slot 0 and unmanaged (exit 0, no
// descriptor by design); a linked worktree with no descriptor, which names
// `wt init` (exit 4).
func runShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt show' with no arguments",
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

	cls, err := identity.Classify(absDir, identity.StandaloneDeclared())
	if err != nil {
		e := identityError(err)
		WriteError(stderr, e)
		return e.Code
	}
	switch cls.Outcome {
	case identity.NotARepository:
		return showVerdict(stdout, stderr, *jsonOut, showResult{
			Found:   false,
			Reason:  fmt.Sprintf("%s is not inside a git work tree", absDir),
			Outcome: cls.Outcome.String(),
		}, New(ExitUnavailable, fmt.Sprintf("%s is not inside a git work tree", absDir),
			"run wt from inside a git repository, then re-run"))
	case identity.PrimaryCheckout, identity.StandaloneClone:
		// Slot 0 carries no descriptor by design. Which of the two
		// remaining cases this is depends on the spec: absent means the
		// repository has not adopted the tooling; present means a primary
		// checkout of an adopted repository, unmanaged.
		if _, err := spec.FindSpecPath(cls.WorktreeRoot); err != nil {
			var nae *spec.NotAdoptedError
			if errors.As(err, &nae) {
				e := New(ExitUnavailable, nae.Error(),
					"commit wt.yaml at the repository root (docs/design/03-drivers.md §3), then re-run: wt show")
				return showVerdict(stdout, stderr, *jsonOut, showResult{
					Found:        false,
					Reason:       nae.Error(),
					Outcome:      cls.Outcome.String(),
					WorktreeRoot: cls.WorktreeRoot,
				}, e)
			}
			e := New(ExitFailure, err.Error(), "")
			WriteError(stderr, e)
			return e.Code
		}
		return showVerdict(stdout, stderr, *jsonOut, showResult{
			Found:        false,
			Reason:       "the primary checkout is slot 0: never allocated, never managed, and it carries no descriptor",
			Outcome:      cls.Outcome.String(),
			WorktreeRoot: cls.WorktreeRoot,
		}, nil)
	}

	// Linked worktree: the spec names the descriptor, the descriptor is
	// the answer.
	specPath, err := spec.FindSpecPath(cls.WorktreeRoot)
	if err != nil {
		var nae *spec.NotAdoptedError
		if errors.As(err, &nae) {
			e := New(ExitUnavailable, nae.Error(),
				"commit wt.yaml at the repository root (docs/design/03-drivers.md §3), then re-run: wt show")
			return showVerdict(stdout, stderr, *jsonOut, showResult{
				Found:        false,
				Reason:       nae.Error(),
				Outcome:      cls.Outcome.String(),
				WorktreeRoot: cls.WorktreeRoot,
			}, e)
		}
		e := New(ExitFailure, err.Error(), "")
		WriteError(stderr, e)
		return e.Code
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		e := New(ExitFailure, fmt.Sprintf("reading %s: %v", specPath, err), "")
		WriteError(stderr, e)
		return e.Code
	}
	parsed, err := spec.Parse(data)
	if err != nil {
		var fe *spec.FieldError
		if errors.As(err, &fe) {
			e := New(ExitFailure, fe.Error(),
				fmt.Sprintf("fix %s, then re-run: wt show", specPath))
			WriteError(stderr, e)
			return e.Code
		}
		e := New(ExitFailure, err.Error(), "")
		WriteError(stderr, e)
		return e.Code
	}

	dpath := identity.DescriptorPath(cls.WorktreeRoot, parsed.Emit.Descriptor)
	if _, err := os.Stat(dpath); err != nil {
		e := New(ExitUnavailable,
			fmt.Sprintf("no descriptor at %s: this linked worktree has not been initialised — run: wt init", dpath),
			"run: wt init")
		return showVerdict(stdout, stderr, *jsonOut, showResult{
			Found:        false,
			Reason:       e.Msg,
			Outcome:      cls.Outcome.String(),
			WorktreeRoot: cls.WorktreeRoot,
		}, e)
	}

	d, err := descriptor.Read(dpath, parsed.Emit.Descriptor.Format)
	if err != nil {
		var ve *descriptor.VersionError
		if errors.As(err, &ve) {
			e := New(ExitFailure, err.Error(),
				"upgrade wt, then re-run: wt show")
			return showVerdict(stdout, stderr, *jsonOut, showResult{
				Found:        false,
				Reason:       err.Error(),
				Outcome:      cls.Outcome.String(),
				WorktreeRoot: cls.WorktreeRoot,
			}, e)
		}
		// 05-delivery.md §8: a descriptor that will not parse is reported
		// with wt init named as the rebuild, and is never overwritten.
		e := New(ExitFailure,
			fmt.Sprintf("%s is not a readable %s descriptor: %v", dpath, parsed.Emit.Descriptor.Format, err),
			"rebuild the descriptor: wt init")
		return showVerdict(stdout, stderr, *jsonOut, showResult{
			Found:        false,
			Reason:       e.Msg,
			Outcome:      cls.Outcome.String(),
			WorktreeRoot: cls.WorktreeRoot,
		}, e)
	}

	if *jsonOut {
		if err := WriteJSON(stdout, showResult{Found: true, Descriptor: d}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	writeShowTable(stdout, d)
	return ExitOK
}

// showResult is the one JSON object `wt show --json` prints: a found/not-
// found verdict, with the descriptor itself on success. Every case show can
// distinguish carries the same shape, so a consumer branches on `found`.
type showResult struct {
	Found        bool                   `json:"found"`
	Reason       string                 `json:"reason,omitempty"`
	Outcome      string                 `json:"outcome,omitempty"`
	WorktreeRoot string                 `json:"worktree_root,omitempty"`
	Descriptor   *descriptor.Descriptor `json:"descriptor,omitempty"`
}

// showVerdict prints a not-found result: the JSON verdict on stdout under
// --json, the human verdict otherwise, and the error (with its remedy) on
// stderr. A nil err means the case is not an error — the primary checkout —
// and exits 0.
func showVerdict(stdout, stderr io.Writer, jsonOut bool, res showResult, err *Error) int {
	if jsonOut {
		if werr := WriteJSON(stdout, res); werr != nil {
			WriteError(stderr, New(ExitFailure, werr.Error(), ""))
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "%s\n", res.Reason)
	}
	if err == nil {
		return ExitOK
	}
	WriteError(stderr, err)
	return err.Code
}

// writeShowTable prints the descriptor as a table, in a fixed order, so a
// human reading it finds every value the generated docs point at.
func writeShowTable(stdout io.Writer, d *descriptor.Descriptor) {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "app:\t%s\n", d.App)
	fmt.Fprintf(w, "slug:\t%s\n", d.Slug)
	fmt.Fprintf(w, "slot:\t%d\n", d.Slot)
	fmt.Fprintf(w, "path:\t%s\n", d.Path)
	fmt.Fprintf(w, "standalone:\t%v\n", d.Standalone)
	fmt.Fprintf(w, "description:\t%s\n", d.Description)

	fmt.Fprintln(w, "\nRESOURCE\tTYPE\tVALUE")
	names := sortedKeys(d.Resources)
	for _, name := range names {
		r := d.Resources[name]
		fmt.Fprintf(w, "%s\t%s\t%v\n", name, r.Type, r.Value)
	}

	fmt.Fprintln(w, "\nSTATE")
	stateNames := sortedKeys(d.State)
	for _, name := range stateNames {
		s := d.State[name]
		if s == nil || s.Isolated == nil {
			fmt.Fprintf(w, "%s:\t(null)\n", name)
			continue
		}
		fmt.Fprintf(w, "%s:\tisolated=%v\n", name, *s.Isolated)
	}

	fmt.Fprintln(w, "\nSHARED")
	for _, sh := range d.Shared {
		fmt.Fprintf(w, "%s\t%s\n", sh.Name, sh.Impact)
	}

	fmt.Fprintln(w, "\nEXTRAS")
	for _, k := range sortedKeys(d.Extras) {
		fmt.Fprintf(w, "%s:\t%v\n", k, d.Extras[k])
	}
	finish(w)
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
