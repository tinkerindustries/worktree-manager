package cli

// coordverb.go is the shape every coordinator-backed read verb has: parse
// the flags, refuse positional arguments, dial, call the verb's one
// generated client method, then either print the JSON or render the
// table. Written out per verb it was eighteen copies of the same six
// steps, and the --json convention, the no-positional-args rule and the
// decode-error wording drifted between them.

import (
	"flag"
	"fmt"
	"io"

	apiclient "github.com/tinkerindustries/worktree-manager/internal/api/client"
)

// coordVerb runs one coordinator-backed verb whose result is T — the
// pointer type the generated client method returns.
//
// name is the verb as a user types it after `wt`; verb is the wire name.
// setup declares the verb's own flags and returns the function that runs
// the verb's one generated client call once parsing is done. render
// prints the text form; --json prints the decoded result instead and
// never reaches it.
func coordVerb[T any](name string, args []string, stdout, stderr io.Writer, verb string,
	setup func(*flag.FlagSet) func(*coordClient) (T, *apiclient.Error),
	render func(stdout, stderr io.Writer, res T) int) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	call := setup(fs)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			fmt.Sprintf("run 'wt %s' with no positional arguments", name),
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()
	res, rerr := call(sess)
	if rerr != nil {
		WriteError(stderr, requestErr(sess.endpoint, verb, rerr))
		return rerr.Code
	}

	if *jsonOut {
		if err := WriteJSON(stdout, res); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	return render(stdout, stderr, res)
}
