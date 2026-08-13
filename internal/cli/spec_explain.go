package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// runSpecExplain implements `wt spec explain --slot N --slug S
// [--base <name>=<port>]... [--home <path>] [--worktree <path>] [--json]`:
// the resolved resource table for one slot, from a pure function of spec,
// slot, slug and bases — the derivation is the same pure function phase 3's
// coordinator will call with the ledger's bases (plan.md §9.1).
func runSpecExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spec explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	slot := fs.Int("slot", 0, "the slot to resolve (1..slots.max)")
	slug := fs.String("slug", "", "the worktree slug (^[a-z0-9][a-z0-9-]*$, at most 32)")
	var bases []string
	fs.Var(stringList(&bases), "base", "band base for one port resource, <name>=<port>; repeatable")
	home := fs.String("home", "", "override {home} (default: the user's home directory)")
	worktree := fs.String("worktree", "", "override {worktree} (default: cwd)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt spec explain --slot N --slug S' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	// The spec comes from the walk-up rule, so explain is testable from
	// inside a fixture tree and from its root.
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
				fmt.Sprintf("edit %s, then re-run: wt spec explain", specPath)))
			return ExitFailure
		}
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}

	baseMap, err := parseBases(parsed, bases)
	if err != nil {
		WriteError(stderr, err)
		return ExitUsage
	}

	// Usage pre-checks on the derivation inputs, so a caller mistake is a
	// usage error (exit 2) rather than a resolution failure.
	slotMax := spec.DefaultSlotMax
	if parsed.Slots.Max != nil {
		slotMax = *parsed.Slots.Max
	}
	if *slot < 1 || *slot > slotMax {
		WriteError(stderr, UsageError(
			fmt.Sprintf("give a slot in 1..%d, e.g. --slot 1", slotMax),
			"--slot %d is outside 1..%d (slot 0 is the primary checkout and is never allocated)", *slot, slotMax))
		return ExitUsage
	}
	if !spec.ValidSlug(*slug) {
		WriteError(stderr, UsageError(
			"give a kebab-case slug of at most 32 characters, e.g. --slug brisk-otter",
			"--slug %q is not a valid slug (must match ^[a-z0-9][a-z0-9-]*$, at most 32 characters)", *slug))
		return ExitUsage
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
	if *worktree == "" {
		wd, err := os.Getwd()
		if err != nil {
			WriteError(stderr, New(ExitUnavailable,
				fmt.Sprintf("cannot determine cwd: %v", err),
				"pass --worktree <path> explicitly"))
			return ExitUnavailable
		}
		*worktree = wd
	}

	table, err := spec.Resolve(parsed, spec.Context{
		Slug:     *slug,
		Slot:     *slot,
		Home:     *home,
		Worktree: *worktree,
		Bases:    baseMap,
	})
	if err != nil {
		var fe *spec.FieldError
		if errors.As(err, &fe) {
			WriteError(stderr, New(ExitFailure, fe.Error(),
				"check the spec and the --base flags, then re-run: wt spec explain"))
			return ExitFailure
		}
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}

	if *jsonOut {
		return writeExplainJSON(stdout, stderr, parsed, *slot, *slug, table)
	}
	return writeExplainTable(stdout, parsed, *slug, table)
}

// parseBases turns the --base flags into the band-base map, checking that
// every base names a port resource, every port resource has a base, and
// every port is a number.
func parseBases(s *spec.Spec, list []string) (map[string]int, error) {
	ports := map[string]bool{}
	for i := range s.Resources {
		if s.Resources[i].Type == "port" {
			ports[s.Resources[i].Name] = true
		}
	}
	bases := make(map[string]int, len(list))
	for _, entry := range list {
		name, portStr, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return nil, UsageError(
				"repeat --base once per port resource, e.g. --base api=4200",
				"invalid --base %q: want <name>=<port>", entry)
		}
		if !ports[name] {
			return nil, UsageError(
				"repeat --base once per port resource, e.g. --base api=4200",
				"base %q names no port resource", name)
		}
		if _, dup := bases[name]; dup {
			return nil, UsageError(
				"pass one --base per port resource",
				"duplicate base for port resource %q", name)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, UsageError(
				"give a port number 1..65535, e.g. --base api=4200",
				"invalid port %q for port resource %q", portStr, name)
		}
		bases[name] = port
	}
	for name := range ports {
		if _, ok := bases[name]; !ok {
			return nil, UsageError(
				fmt.Sprintf("pass a base for every port resource: --base %s=<port>", name),
				"no base supplied for port resource %q", name)
		}
	}
	return bases, nil
}

// explainJSON is the one JSON object `wt spec explain --json` prints.
type explainJSON struct {
	App       string                   `json:"app"`
	Slug      string                   `json:"slug"`
	Slot      int                      `json:"slot"`
	Resources map[string]spec.Resolved `json:"resources"`
}

func writeExplainJSON(stdout, stderr io.Writer, s *spec.Spec, slot int, slug string, table map[string]spec.Resolved) int {
	out := explainJSON{App: s.App, Slug: slug, Slot: slot, Resources: table}
	if err := WriteJSON(stdout, out); err != nil {
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}
	return ExitOK
}

// writeExplainTable prints the resource table in spec declaration order.
func writeExplainTable(stdout io.Writer, s *spec.Spec, slug string, table map[string]spec.Resolved) int {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "app\t%s\nslug\t%s\n\n", s.App, slug)
	fmt.Fprintln(w, "NAME\tTYPE\tVALUE")
	for i := range s.Resources {
		r := &s.Resources[i]
		res := table[r.Name]
		fmt.Fprintf(w, "%s\t%s\t%v\n", r.Name, res.Type, res.Value)
	}
	return finish(w)
}

// finish flushes the table writer, mapping a write error to an exit code.
func finish(w *tabwriter.Writer) int {
	if err := w.Flush(); err != nil {
		return ExitFailure
	}
	return ExitOK
}
