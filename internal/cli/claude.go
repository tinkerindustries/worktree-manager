package cli

// claude.go is `wt claude install` and `wt claude uninstall`: registering
// worktree-manager as Claude Code's worktree creator, and taking the
// registration back out. Both are client-local — no coordinator call, no
// route — and both take --prefix exactly as the daemon verbs do, so a test
// never touches the machine's real ~/.claude.
//
// The mechanism is internal/claudehook's. What lives here is the flag
// parsing, the two output shapes, and the exit-code mapping: a refusal to
// overwrite something wt did not write is exit 3, naming --force, which is
// what every other refusal in this client does.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/mrgeoffrich/worktree-manager/internal/claudehook"
)

// claudeResult is the one JSON object `wt claude install --json` and
// `wt claude uninstall --json` print.
type claudeResult struct {
	Operation string              `json:"operation"` // install | uninstall
	DryRun    bool                `json:"dry_run"`
	Dir       string              `json:"dir"`
	Settings  string              `json:"settings"`
	LogPath   string              `json:"log_path"`
	SkillDir  string              `json:"skill_dir,omitempty"`
	Changes   []claudehook.Change `json:"changes"`
	// NothingInstalled marks a --refresh-only run that found no
	// installation to refresh, so the closing sentence does not claim an
	// integration that is not there.
	NothingInstalled bool `json:"nothing_installed,omitempty"`
}

func runClaude(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		WriteError(stderr, UsageError(
			"run 'wt claude install' or 'wt claude uninstall'",
			"claude needs a verb"))
		return ExitUsage
	}
	switch args[0] {
	case "install":
		return runClaudeInstall(args[1:], stdout, stderr)
	case "uninstall":
		return runClaudeUninstall(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return ExitOK
	default:
		WriteError(stderr, UsageError(
			"run 'wt claude install' or 'wt claude uninstall'",
			"unknown claude verb %q", args[0]))
		return ExitUsage
	}
}

// claudeFlags is the flag set both verbs share.
type claudeFlags struct {
	fs      *flag.FlagSet
	jsonOut *bool
	dryRun  *bool
	force   *bool
	prefix  *string
	skill   *bool
	// refreshOnly is install's alone; uninstall leaves it nil.
	refreshOnly *bool
}

func newClaudeFlags(name string, stderr io.Writer, skillUsage string) *claudeFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return &claudeFlags{
		fs:      fs,
		jsonOut: fs.Bool("json", false, "print exactly one JSON object on stdout"),
		dryRun:  fs.Bool("dry-run", false, "print what would happen and change nothing"),
		force:   fs.Bool("force", false, "overwrite hooks and files wt did not write"),
		prefix:  fs.String("prefix", "", "the Claude Code directory to act on (default: ~/.claude)"),
		skill:   fs.Bool("skill", true, skillUsage),
	}
}

// layout resolves --prefix, or the real ~/.claude when it is empty.
func (c *claudeFlags) layout() (claudehook.Layout, *Error) {
	if *c.prefix != "" {
		abs, err := filepath.Abs(*c.prefix)
		if err != nil {
			return claudehook.Layout{}, New(ExitFailure, fmt.Sprintf("resolving --prefix: %v", err), "")
		}
		return claudehook.Layout{Dir: abs}, nil
	}
	l, err := claudehook.DefaultLayout()
	if err != nil {
		return claudehook.Layout{}, New(ExitFailure, err.Error(), "")
	}
	return l, nil
}

// options builds the options both operations take. The wt binary recorded
// in the scripts is this process's own executable, so the hooks call the
// wt that installed them rather than whatever PATH resolves to inside a
// GUI app's environment — the case this whole verb exists for.
func (c *claudeFlags) options() claudehook.Options {
	self, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
			self = resolved
		}
	} else {
		self = ""
	}
	return claudehook.Options{
		WtBinary:    self,
		Force:       *c.force,
		DryRun:      *c.dryRun,
		Skill:       *c.skill,
		RefreshOnly: c.refreshOnly != nil && *c.refreshOnly,
	}
}

func runClaudeInstall(args []string, stdout, stderr io.Writer) int {
	c := newClaudeFlags("claude install", stderr,
		"install the worktree-onboarding skill into the user skills directory too")
	// An upgrade runs this: the scripts and the skill live in the binary,
	// so a new wt carries new copies, but registering the hooks at all is
	// the user's opt-in and an upgrade never makes it for them.
	c.refreshOnly = c.fs.Bool("refresh-only", false,
		"update an existing installation and create none; changes nothing when the hooks are not installed")
	if err := c.fs.Parse(args); err != nil {
		return ExitUsage
	}
	if c.fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt claude install' with no arguments",
			"unexpected arguments: %v", c.fs.Args()))
		return ExitUsage
	}
	return runClaudeOp("install", c, stdout, stderr, claudehook.Install)
}

func runClaudeUninstall(args []string, stdout, stderr io.Writer) int {
	c := newClaudeFlags("claude uninstall", stderr,
		"remove the worktree-onboarding skill from the user skills directory too")
	if err := c.fs.Parse(args); err != nil {
		return ExitUsage
	}
	if c.fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt claude uninstall' with no arguments",
			"unexpected arguments: %v", c.fs.Args()))
		return ExitUsage
	}
	return runClaudeOp("uninstall", c, stdout, stderr, claudehook.Uninstall)
}

// runClaudeOp is the half install and uninstall share: resolve the layout,
// run the operation, map a refusal to exit 3, and render.
func runClaudeOp(
	operation string,
	c *claudeFlags,
	stdout, stderr io.Writer,
	op func(claudehook.Layout, claudehook.Options) ([]claudehook.Change, error),
) int {
	layout, cerr := c.layout()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	// Asked before the operation, because the operation is what would
	// create the installation this reports the absence of.
	nothingInstalled := false
	if c.refreshOnly != nil && *c.refreshOnly {
		present, ierr := claudehook.Installed(layout)
		if ierr != nil {
			WriteError(stderr, New(ExitFailure, ierr.Error(), ""))
			return ExitFailure
		}
		nothingInstalled = !present
	}
	changes, err := op(layout, c.options())
	if err != nil {
		var conflict *claudehook.ConflictError
		if errors.As(err, &conflict) {
			WriteError(stderr, New(ExitRefused, conflict.Error(),
				fmt.Sprintf("run 'wt claude %s --force' to replace it, or move the file aside first", operation)))
			return ExitRefused
		}
		WriteError(stderr, New(ExitFailure, err.Error(), ""))
		return ExitFailure
	}

	result := claudeResult{
		Operation: operation,
		DryRun:    *c.dryRun,
		Dir:       layout.Dir,
		Settings:  layout.SettingsPath(),
		LogPath:   layout.LogPath(),
		Changes:   changes,

		NothingInstalled: nothingInstalled,
	}
	if *c.skill {
		result.SkillDir = layout.SkillDir()
	}
	if *c.jsonOut {
		if err := WriteJSON(stdout, result); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	return writeClaudeText(stdout, stderr, result)
}

// writeClaudeText renders the change list, then the one sentence that says
// what the machine will now do when a worktree is created.
func writeClaudeText(stdout, stderr io.Writer, r claudeResult) int {
	// A refresh with nothing to refresh has one thing to say and says it
	// on one line: an upgrade runs this on every machine, including the
	// ones that never registered the hooks, and a table plus a paragraph
	// there is noise about a non-event.
	if r.NothingInstalled {
		fmt.Fprintf(stdout, "nothing to refresh: no Claude Code hooks in %s — run 'wt claude install' to add them\n", r.Dir)
		return ExitOK
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ACTION\tPATH\tDETAIL")
	for _, c := range r.Changes {
		detail := c.Detail
		if detail == "" {
			detail = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.Action, c.Path, detail)
	}
	if code := finish(w); code != ExitOK {
		return code
	}

	if r.DryRun {
		fmt.Fprintln(stdout, "\ndry run: nothing was changed")
		return ExitOK
	}
	fmt.Fprintln(stdout)
	if r.Operation == "install" {
		fmt.Fprintf(stdout, "Claude Code now creates worktrees through wt. A repository with a\n")
		fmt.Fprintf(stdout, "wt.yaml gets an allocated environment; every other repository gets the\n")
		fmt.Fprintf(stdout, "plain worktree Claude Code would have made itself.\n")
		fmt.Fprintf(stdout, "hook log: %s\n", r.LogPath)
		return ExitOK
	}
	fmt.Fprintf(stdout, "Claude Code creates worktrees on its own again. The hook log at %s\n", r.LogPath)
	fmt.Fprintf(stdout, "was left in place, and so was every worktree already allocated —\n")
	fmt.Fprintf(stdout, "run 'wt list' to see them.\n")
	return ExitOK
}
