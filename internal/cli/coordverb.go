package cli

// coordverb.go is the shape every coordinator-backed read verb has: parse
// the flags, refuse positional arguments, dial, request, decode, then
// either print the JSON or render the table. Written out per verb it was
// eighteen copies of the same six steps, and the --json convention, the
// no-positional-args rule and the decode-error wording drifted between
// them.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
)

// coordVerb runs one coordinator-backed verb whose result decodes into T.
//
// name is the verb as a user types it after `wt`. flags declares the verb's
// own flags and returns the function that builds the request arguments once
// parsing is done (nil for a verb that sends none). render prints the text
// form; --json prints the decoded result instead and never reaches it.
func coordVerb[T any](name string, args []string, stdout, stderr io.Writer, verb string,
	flags func(*flag.FlagSet) func() any, render func(stdout, stderr io.Writer, res *T) int) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	build := func() any { return nil }
	if flags != nil {
		build = flags(fs)
	}
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
	raw, rerr := sess.request(verb, build())
	if rerr != nil {
		WriteError(stderr, rerr)
		return rerr.Code
	}
	var res T
	if err := json.Unmarshal(raw, &res); err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("decoding the %s response: %v", name, err), ""))
		return ExitFailure
	}

	if *jsonOut {
		if err := WriteJSON(stdout, res); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	return render(stdout, stderr, &res)
}
