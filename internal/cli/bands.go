package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	apiclient "github.com/mrgeoffrich/worktree-manager/internal/api/client"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// runBands implements the two bands verbs: `wt bands list` and
// `wt bands reserve` (with --host). Both reach the coordinator, so exit 5
// is wired through dialCoordinator like every other coordinator verb.
func runBands(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		WriteError(stderr, UsageError(
			"run 'wt bands list' or 'wt bands reserve'",
			"bands needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "list":
		return runBandsList(args[1:], stdout, stderr)
	case "suggest":
		return runBandsSuggest(args[1:], stdout, stderr)
	case "reserve":
		return runBandsReserve(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt bands list', 'wt bands suggest' or 'wt bands reserve'",
			"unknown bands verb %q", args[0]))
		return ExitUsage
	}
}

// runBandsSuggest implements `wt bands suggest [--spec <file>] [--json]`:
// propose where the spec's port bases could sit. The coordinator computes
// the required size from the spec (slot ceiling × ports per slot) and
// finds the lowest base per resource whose range fits — colliding with no
// existing band and no host-global reservation — so the skill chooses only
// where the bases go, not how large they are (02-coordination.md §6.2,
// 09-onboarding.md phase 3). The skill shows the suggestion, the developer
// confirms, and `wt bands reserve` claims it.
func runBandsSuggest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bands suggest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	specPath := fs.String("spec", "", "the spec file to suggest bases for (default: the walk-up wt.yaml)")
	cwd := fs.String("cwd", "", "find the spec from this directory (default: the process cwd)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt bands suggest' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	path := *specPath
	if path == "" {
		// The walk-up rule, exactly as spec explain and bands reserve find
		// the spec: the committed wt.yaml from cwd to the worktree root.
		found, err := spec.FindSpecPath(specStart(*cwd))
		if err != nil {
			var nae *spec.NotAdoptedError
			if errors.As(err, &nae) {
				WriteError(stderr, New(ExitUnavailable, nae.Error(),
					"commit wt.yaml at the repository root, or run from inside the repository, or pass --spec <file>"))
				return ExitUnavailable
			}
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		path = found
	}
	data, err := os.ReadFile(path)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("reading %s: %v", path, err), ""))
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
				fmt.Sprintf("edit %s, then re-run: wt bands suggest", path)))
			return ExitFailure
		}
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}

	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()
	res, rerr := sess.client.BandsSuggest(&api.SuggestBandArgs{Spec: *parsed})
	if rerr != nil {
		WriteError(stderr, requestErr(sess.endpoint, api.VerbBandsSuggest, rerr))
		return rerr.Code
	}

	if *jsonOut {
		if err := WriteJSON(stdout, res); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	for _, n := range res.Notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "app\t%s\n", res.App)
	fmt.Fprintln(w, "RESOURCE\tBASE\tSPAN\tRANGE")
	for _, s := range res.Suggestions {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d..%d\n", s.Resource, s.Base, s.Span, s.Low, s.High)
	}
	if len(res.Suggestions) == 0 {
		fmt.Fprintln(w, "(no port resources to suggest bases for)\t")
	}
	return finish(w)
}

// runBandsList implements `wt bands list [--json]`: the band ledger — the
// bases each app holds and the host-global reservations no app may allocate
// from. It is the onboarding skill's read of the machine's port facts.
func runBandsList(args []string, stdout, stderr io.Writer) int {
	return coordVerb("bands list", args, stdout, stderr, api.VerbBandsList,
		func(*flag.FlagSet) func(*coordClient) (*api.BandsListResult, *apiclient.Error) {
			return func(sess *coordClient) (*api.BandsListResult, *apiclient.Error) {
				return sess.client.BandsList()
			}
		}, writeBandsTable)
}

// writeBandsTable prints the ledger in text form: one line per app band,
// one per host reservation, both sorted.
func writeBandsTable(stdout, stderr io.Writer, res *api.BandsListResult) int {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "BANDS\t")
	fmt.Fprintln(w, "APP\tBASE")
	if len(res.Bands) == 0 {
		fmt.Fprintln(w, "(none)\t")
	}
	for _, b := range res.Bands {
		names := make([]string, 0, len(b.Bases))
		for name := range b.Bases {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s=%d", name, b.Bases[name]))
		}
		fmt.Fprintf(w, "%s\t%s\n", b.App, strings.Join(parts, " "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "RESERVATIONS\t")
	fmt.Fprintln(w, "PORTS\tNAMES\tNOTE")
	if len(res.Reservations) == 0 {
		fmt.Fprintln(w, "(none)\t")
	}
	for _, r := range res.Reservations {
		ports := joinPorts(r.Ports)
		if ports == "" {
			ports = "-"
		}
		names := strings.Join(r.Names, ", ")
		if names == "" {
			names = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", ports, names, r.Note)
	}
	return finish(w)
}

// joinPorts prints a sorted port list as "5319, 5320".
func joinPorts(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p))
	}
	return strings.Join(parts, ", ")
}

// runBandsReserve implements the two registration forms:
//
//	wt bands reserve --base <name>=<port>...            register this repo's
//	    [--json]                                        port band from its
//	                                                    committed spec
//	wt bands reserve --host --port <p>...               reserve host-global
//	    [--name <project>...]                           ports no app may
//	    --note <text>                                   allocate from and
//	    [--json]                                        compose project names
//	                                                    no teardown may reach
//
// Registration is explicit — nothing grabs a range on the fly — and a host
// reservation carries a required note naming what holds the range
// (02-coordination.md §6.2, plan.md §8 R6). The reserved names are the
// phase-4 rail behind the namespace driver's teardown refusal: a person
// declares the co-resident stack's compose project name once per machine.
func runBandsReserve(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bands reserve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	host := fs.Bool("host", false, "reserve host-global ports or project names instead of registering an app band")
	var bases []string
	fs.Var(stringList(&bases), "base", "band base for one port resource, <name>=<port>; repeatable")
	var ports []int
	fs.Var(portList(&ports), "port", "host-globally reserved port; repeatable")
	var names []string
	fs.Var(stringList(&names), "name", "host-globally reserved compose project name; repeatable")
	note := fs.String("note", "", "what holds the reserved range (required with --host)")
	cwd := fs.String("cwd", "", "find the spec from this directory (default: the process cwd)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt bands reserve' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	var reqArgs api.ReserveBandArgs
	if *host {
		if len(bases) > 0 {
			WriteError(stderr, UsageError(
				"run 'wt bands reserve --host --port <p>... [--name <project>...] --note <text>'",
				"--host takes --port and --name, not --base"))
			return ExitUsage
		}
		if len(ports) == 0 && len(names) == 0 {
			WriteError(stderr, UsageError(
				"run 'wt bands reserve --host --port 5319 --port 5320 --name compose-app-prod --note <text>'",
				"--host requires at least one --port or one --name"))
			return ExitUsage
		}
		if *note == "" {
			WriteError(stderr, UsageError(
				"re-run with --note naming what holds the range, e.g. --note \"compose-app production stack\"",
				"--host requires --note; an unlabelled reservation is one nobody can later judge"))
			return ExitUsage
		}
		reqArgs = api.ReserveBandArgs{Host: true, Ports: []int(ports), Names: []string(names), Note: *note}
	} else {
		if len(ports) > 0 || len(names) > 0 {
			WriteError(stderr, UsageError(
				"run 'wt bands reserve --host --port <p>... --name <project>...' for host reservations",
				"--port and --name are only valid with --host"))
			return ExitUsage
		}
		if *note != "" {
			WriteError(stderr, UsageError(
				"run 'wt bands reserve --host --port <p>... --note <text>' for host reservations",
				"--note is only valid with --host"))
			return ExitUsage
		}
		if len(bases) == 0 {
			WriteError(stderr, UsageError(
				"run 'wt bands reserve --base <name>=<port>...' with one base per port resource, e.g. --base api=4200",
				"a band registration needs at least one --base"))
			return ExitUsage
		}

		// The band's required size comes from the committed spec, so the
		// skill chooses only where the bases sit — the spec comes from the
		// walk-up rule, the same way spec explain finds it.
		specPath, err := spec.FindSpecPath(specStart(*cwd))
		if err != nil {
			var nae *spec.NotAdoptedError
			if errors.As(err, &nae) {
				WriteError(stderr, New(ExitUnavailable, nae.Error(),
					"commit wt.yaml at the repository root, or run from inside the repository"))
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
					fmt.Sprintf("edit %s, then re-run: wt bands reserve", specPath)))
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
		reqArgs = api.ReserveBandArgs{Spec: *parsed, Bases: baseMap}
	}

	sess, err := dialCoordinator()
	if err != nil {
		WriteError(stderr, err)
		return err.Code
	}
	defer sess.Close()
	res, rerr := sess.client.BandsReserve(&reqArgs)
	if rerr != nil {
		WriteError(stderr, requestErr(sess.endpoint, api.VerbBandsReserve, rerr))
		return rerr.Code
	}

	if *jsonOut {
		if err := WriteJSON(stdout, res); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	return writeReserveTable(stdout, res)
}

// writeReserveTable prints the registration result: the app's bases with
// the span each must cover, or the host reservation with its note.
func writeReserveTable(stdout io.Writer, res *api.ReserveBandResult) int {
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	if res.Host {
		fmt.Fprintf(w, "reserved:\t%s\n", joinPorts(res.Ports))
		if len(res.Names) > 0 {
			fmt.Fprintf(w, "names:\t%s\n", strings.Join(res.Names, ", "))
		}
		fmt.Fprintf(w, "note:\t%s\n", res.Note)
		return finish(w)
	}
	fmt.Fprintf(w, "app:\t%s\n", res.App)
	names := make([]string, 0, len(res.Bases))
	for name := range res.Bases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		base := res.Bases[name]
		span := res.Spans[name]
		fmt.Fprintf(w, "base:\t%s=%d (span %d ports: %d..%d)\n",
			name, base, span, base, base+span-1)
	}
	return finish(w)
}

// specStart is where the walk-up spec search begins: the --cwd directory,
// or the process cwd. Every verb that reads the committed spec takes the
// same flag.
func specStart(cwd string) string {
	if cwd == "" {
		return "."
	}
	return cwd
}
