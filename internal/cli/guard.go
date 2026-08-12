package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/identity"
)

// guardStdin is where the PreToolUse payload is read from. It is a variable
// so the in-process tests can feed a payload without exec'ing the binary;
// production use is os.Stdin.
var guardStdin io.Reader = os.Stdin

// runGuard implements `wt guard [--json] [--cwd <dir>] [--tool <name>]
// [--input <json>] [--path <path>]`: the enforcement hook (ARCHITECTURE.md
// §9.6). It classifies cwd, resolves the path under test, applies the
// containment test and denies with a reason that names the correct root
// verbatim. It reads git and the descriptor and opens no socket.
//
// Input surface: a Claude Code PreToolUse hook receives a JSON payload on
// stdin carrying the tool name and its input, and that stdin payload is the
// primary form — the shape phase 7's generated hook will call against. The
// flags carry the same fields so the verb is testable and usable by hand:
// --cwd names the directory to classify, --tool the tool name (default
// Write), --input the tool_input object, --path shorthand for a single
// file_path.
//
// Exit codes: 0 allowed, 3 denied (refused by a safety check), 2 usage, 1
// or 4 for classification failures. `--json` prints exactly one JSON object
// on stdout; a denial's reason also goes to stderr, so a hook that surfaces
// only stderr still shows the model the reason to correct itself.
func runGuard(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	tool := fs.String("tool", "", "tool name (default: Write)")
	input := fs.String("input", "", "tool_input as a JSON object")
	path := fs.String("path", "", "shorthand for --input {\"file_path\": \"<path>\"}")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt guard --path <file>' or 'wt guard --tool <name> --input <json>', or pipe a PreToolUse payload on stdin",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	req, err := guardRequest(*tool, *input, *path)
	if err != nil {
		WriteError(stderr, err)
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

	verdict, err := identity.Guard(absDir, req)
	if err != nil {
		e := identityError(err)
		WriteError(stderr, e)
		return e.Code
	}

	if *jsonOut {
		if err := WriteJSON(stdout, verdict); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
	} else {
		word := "allowed"
		if !verdict.Allowed {
			word = "denied"
		}
		fmt.Fprintf(stdout, "%s: %s\n", word, verdict.Reason)
	}
	if !verdict.Allowed {
		// The human denial echoes on stderr in --json mode, where stdout
		// carries the machine-readable verdict alone.
		if *jsonOut {
			fmt.Fprintf(stderr, "denied: %s\n", verdict.Reason)
		}
		return ExitRefused
	}
	return ExitOK
}

// guardRequest resolves the tool call under test: a stdin payload when one
// is present (the hook's shape), the flags otherwise.
func guardRequest(tool, input, path string) (identity.GuardRequest, error) {
	data, err := io.ReadAll(guardStdin)
	if err != nil {
		return identity.GuardRequest{}, New(ExitFailure, fmt.Sprintf("reading stdin: %v", err), "")
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if input != "" || path != "" {
			return identity.GuardRequest{}, UsageError(
				"give the payload either on stdin or via --input/--path, not both",
				"stdin carries a payload and --input/--path is also set")
		}
		var req identity.GuardRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return identity.GuardRequest{}, UsageError(
				"the hook sends a JSON object with tool_name and tool_input on stdin",
				"stdin is not the PreToolUse payload: %v", err)
		}
		if req.ToolName == "" {
			return identity.GuardRequest{}, UsageError(
				"the hook sends a JSON object with tool_name and tool_input on stdin",
				"stdin payload has no tool_name")
		}
		return req, nil
	}
	if tool == "" {
		tool = "Write"
	}
	if input != "" && path != "" {
		return identity.GuardRequest{}, UsageError(
			"give the file under test either as --path or inside --input, not both",
			"--input and --path are both set")
	}
	var inputMap map[string]any
	switch {
	case path != "":
		inputMap = map[string]any{"file_path": path}
	case input != "":
		if err := json.Unmarshal([]byte(input), &inputMap); err != nil {
			return identity.GuardRequest{}, UsageError(
				"give the tool input as a JSON object, e.g. --input '{\"file_path\": \"/abs/path\"}'",
				"--input is not a JSON object: %v", err)
		}
	default:
		return identity.GuardRequest{}, UsageError(
			"pipe a PreToolUse payload on stdin, or give --path <file> (or --tool <name> --input <json>)",
			"no tool call to guard")
	}
	return identity.GuardRequest{ToolName: tool, ToolInput: inputMap}, nil
}

// identityError maps the identity package's named refusals onto exit codes
// and remedies. Every user-facing error carries a remedy.
func identityError(err error) *Error {
	var dw *identity.DeletedWorktreeError
	if errors.As(err, &dw) {
		return New(ExitFailure, dw.Error(),
			"prune the stale worktree registration: git worktree prune, then re-run")
	}
	var sw *identity.StandaloneInLinkedWorktreeError
	if errors.As(err, &sw) {
		return New(ExitFailure, sw.Error(),
			"unset WT_STANDALONE (or remove the descriptor's standalone flag), then re-run")
	}
	var gn *identity.GitNotFoundError
	if errors.As(err, &gn) {
		return New(ExitUnavailable, gn.Error(),
			"install git (brew install git / apt install git), then re-run")
	}
	return New(ExitFailure, err.Error(), "run wt from inside a git work tree, then re-run")
}
