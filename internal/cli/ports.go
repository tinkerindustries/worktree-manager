package cli

// ports.go is the client half of `wt ports scan` (phase 7): what is
// listening on this machine right now, as the onboarding skill's phase-1
// audit reads it (09-onboarding.md §3). The scan runs in the coordinator —
// listener discovery needs the host network namespace — so exit 5 is wired
// through dialCoordinator like every other coordinator verb. It reports
// facts and never reserves anything: whether a listener belongs to a
// production stack is a person's judgement, shown by this verb and
// declared with `wt bands reserve --host` (plan.md §8, R6).

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	apiclient "github.com/mrgeoffrich/worktree-manager/internal/api/client"
)

// runPorts implements `wt ports scan [--json]`.
func runPorts(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		WriteError(stderr, UsageError(
			"run 'wt ports scan'",
			"ports needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "scan":
		return runPortsScan(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt ports scan'",
			"unknown ports verb %q", args[0]))
		return ExitUsage
	}
}

// runPortsScan implements `wt ports scan [--json]`: every LISTEN TCP
// socket in the coordinator's network namespace, sorted by port — the
// listeners the developer has not declared, so they can be reserved with
// `wt bands reserve --host`. The scan's bounded-coverage notes (what it
// could not see and why) go to stderr; the listeners are the result.
func runPortsScan(args []string, stdout, stderr io.Writer) int {
	return coordVerb("ports scan", args, stdout, stderr, api.VerbPortsScan,
		func(*flag.FlagSet) func(*coordClient) (*api.PortsScanResult, *apiclient.Error) {
			return func(sess *coordClient) (*api.PortsScanResult, *apiclient.Error) {
				return sess.client.PortsScan()
			}
		}, writePortsTable)
}

// writePortsTable prints one line per LISTEN socket, with the scan's
// bounded-coverage notes on stderr.
func writePortsTable(stdout, stderr io.Writer, res *api.PortsScanResult) int {
	for _, n := range res.Notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PORT\tPID\tCOMMAND")
	if len(res.Listeners) == 0 {
		fmt.Fprintln(w, "(nothing listening)\t")
	}
	for _, l := range res.Listeners {
		port := fmt.Sprintf("%d", l.Port)
		if l.Port == 0 {
			port = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", port, l.PID, l.Command)
	}
	return finish(w)
}
