package cli

// hooks.go is the hook sequencer (04-lifecycle.md §5): the six repo-declared
// commands the client runs without understanding. The contract, enforced
// here and nowhere else:
//
//   - cwd is the worktree root;
//   - the environment is what the delivery half emitted — the resolved
//     emit.env keys (the same values the .env managed block carries) are
//     exported to the hook process;
//   - hook output streams to stderr (stdout and stderr both);
//   - a non-zero exit stops the sequence;
//   - --dry-run prints the resolved command without running it.
//
// Two behaviours from §5.2 and §5.3 live here too: the sticky parameters
// (chosen on first run, persisted in the descriptor, reused by later runs,
// overridden by an explicit flag with a warning), and health's poll-with-
// progress that on timeout dumps the last lines of the hook's output.
// B10.4's missing seed credentials is the sticky-parameter case with no
// value anywhere: the seed hook is skipped with a warning naming the
// remedy, and the run continues — the worktree is still useful without a
// seeded instance.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// healthPollInterval is how often the health hook is retried, and how
// often progress is logged (B7.4: poll, log every 10s, dump the last lines
// on timeout). A variable so the polling behaviour is testable without
// waiting ten seconds between attempts.
var healthPollInterval = 10 * time.Second

// healthTailLines is how many of the last lines of the health check's
// output are dumped on timeout.
const healthTailLines = 40

// hookRunner runs one repo's hooks. One instance per verb invocation; the
// chosen sticky values accumulate on it and the caller persists them into
// the descriptor when the run is done.
type hookRunner struct {
	sp       *spec.Spec
	ctx      spec.Context
	resolved map[string]spec.Resolved
	worktree string
	stderr   io.Writer
	dryRun   bool

	// envKeys are the resolved emit.env keys, exported to every hook
	// process: the environment the delivery half emitted.
	envKeys map[string]string

	// persisted are the sticky values read from the descriptor; chosen are
	// the sticky values decided this run (to be persisted); explicit are
	// the --param flag values. A parameter resolves explicit → persisted →
	// default.
	persisted map[string]string
	explicit  map[string]string
	chosen    map[string]string
}

// newHookRunner builds the runner for one worktree.
func newHookRunner(sp *spec.Spec, ctx spec.Context, resolved map[string]spec.Resolved, worktree string, stderr io.Writer, dryRun bool) *hookRunner {
	return &hookRunner{
		sp: sp, ctx: ctx, resolved: resolved, worktree: worktree,
		stderr: stderr, dryRun: dryRun,
		persisted: map[string]string{},
		explicit:  map[string]string{},
		chosen:    map[string]string{},
	}
}

// setPersisted installs the sticky values read from the descriptor.
func (r *hookRunner) setPersisted(values map[string]string) {
	r.persisted = values
}

// setExplicit installs the --param flag values.
func (r *hookRunner) setExplicit(values map[string]string) {
	r.explicit = values
}

// chosenValues returns the sticky values decided this run, for the caller
// to persist into the descriptor.
func (r *hookRunner) chosenValues() map[string]string {
	return r.chosen
}

// runAll runs the named hooks in order, stopping at the first failure.
// It returns the name of the hook that failed, or "".
func (r *hookRunner) runAll(hooks []string, h *spec.Hooks) (string, error) {
	for _, name := range hooks {
		hook := hookByName(h, name)
		if hook == nil {
			continue
		}
		if name == "health" {
			if err := r.runHealth(hook); err != nil {
				return name, err
			}
			continue
		}
		if err := r.runHook(name, hook); err != nil {
			return name, err
		}
	}
	return "", nil
}

// runHook executes one hook: resolve the command, print it under --dry-run,
// or run it with cwd at the worktree root, the delivery environment, output
// streamed to stderr, and the declared timeout killing the command's whole
// process group.
func (r *hookRunner) runHook(name string, hook *spec.Hook) error {
	cmd, err := r.resolveCommand(name, hook)
	if err != nil {
		return err
	}
	if r.dryRun {
		fmt.Fprintf(r.stderr, "hook %s: would run: %s\n", name, cmd)
		return nil
	}
	fmt.Fprintf(r.stderr, "hook %s: %s\n", name, cmd)

	var c *exec.Cmd
	var ctx context.Context
	timeout := r.hookTimeout(name, hook)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
		// CommandContext owns the kill-on-timeout plumbing; the Cancel
		// override widens it from the shell to the shell's whole process
		// group, so a timed-out hook cannot orphan its children.
		c = exec.CommandContext(ctx, "sh", "-c", cmd)
		c.Cancel = func() error { return platform.KillGroup(c) }
		c.WaitDelay = 5 * time.Second
	} else {
		c = exec.Command("sh", "-c", cmd)
	}
	c.Dir = r.worktree
	c.Env = r.hookEnv()
	c.Stdout = r.stderr
	c.Stderr = r.stderr
	platform.StartInOwnGroup(c)

	err = c.Run()
	if timeout > 0 && ctx.Err() != nil {
		// The context expired and the process group was killed: the
		// timeout is the failure, named as such.
		return fmt.Errorf("hook %s timed out after %s; its process group was terminated", name, timeout)
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("hook %s failed with exit code %d; its output is above", name, ee.ExitCode())
		}
		return fmt.Errorf("hook %s failed: %v", name, err)
	}
	return nil
}

// runHealth polls the health hook: run it, and on a failure wait
// healthPollInterval and retry, logging progress, until it succeeds or the
// declared timeout is exhausted — then dump the last lines of the hook's
// output and fail. Returning before the stack is usable pushes the failure
// into whatever the user does next, where it looks unrelated (B7.4).
//
// A health hook with no declared timeout polls without a bound, stated in
// the first progress line — bounded coverage is stated, never silent
// (plan.md §3).
func (r *hookRunner) runHealth(hook *spec.Hook) error {
	cmd, err := r.resolveCommand("health", hook)
	if err != nil {
		return err
	}
	if r.dryRun {
		fmt.Fprintf(r.stderr, "hook health: would run: %s\n", cmd)
		return nil
	}
	timeout := r.hookTimeout("health", hook)
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
		fmt.Fprintf(r.stderr, "hook health: %s (polling every %s, budget %s)\n", cmd, healthPollInterval, timeout)
	} else {
		fmt.Fprintf(r.stderr, "hook health: %s (polling every %s; no timeout declared, so no bound — bounded coverage is stated, never silent)\n",
			cmd, healthPollInterval)
	}

	tail := newTailBuffer(healthTailLines)
	attempt := 0
	for {
		attempt++
		err := r.runCommandCapture(cmd, tail)
		if err == nil {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			fmt.Fprintf(r.stderr, "hook health: timed out after %s (last attempt: %v)\n", timeout, err)
			fmt.Fprintf(r.stderr, "hook health: last lines of the health check output:\n%s", tail.String())
			return fmt.Errorf("health check timed out after %s: %v; the stack did not become usable", timeout, err)
		}
		remaining := ""
		if !deadline.IsZero() {
			remaining = fmt.Sprintf(" (%s left)", time.Until(deadline).Round(time.Second))
		}
		fmt.Fprintf(r.stderr, "hook health: attempt %d failed: %v; retrying in %s%s\n",
			attempt, err, healthPollInterval, remaining)
		time.Sleep(healthPollInterval)
	}
}

// runCommandCapture runs one command with the hook environment, streaming
// the output to stderr while also writing it into tail (the ring buffer
// the timeout dump reads).
func (r *hookRunner) runCommandCapture(cmd string, tail *tailBuffer) error {
	c := exec.Command("sh", "-c", cmd)
	c.Dir = r.worktree
	c.Env = r.hookEnv()
	c.Stdout = io.MultiWriter(r.stderr, tail)
	c.Stderr = io.MultiWriter(r.stderr, tail)
	platform.StartInOwnGroup(c)
	err := c.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("exit code %d", ee.ExitCode())
		}
		return err
	}
	return nil
}

// hookTimeout parses the hook's declared timeout; 0 means none.
func (r *hookRunner) hookTimeout(name string, hook *spec.Hook) time.Duration {
	if hook.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(hook.Timeout)
	if err != nil {
		// Validation refuses a malformed duration at spec-validate time;
		// reaching here is a spec edited after validation.
		fmt.Fprintf(r.stderr, "hook %s: timeout %q is not a duration; running without a timeout\n", name, hook.Timeout)
		return 0
	}
	return d
}

// hookEnv is the hook process environment: the caller's environment plus
// the resolved emit.env keys — the environment the delivery half emitted
// (05-delivery.md §3). The keys resolve through the same templates as the
// .env managed block, so the hook process and the .env file agree.
func (r *hookRunner) hookEnv() []string {
	env := os.Environ()
	names := make([]string, 0, len(r.envKeys))
	for name := range r.envKeys {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+r.envKeys[name])
	}
	return env
}

// resolveCommand substitutes a hook's run template: its declared params
// first (explicit flag → persisted value → default, with the sticky
// semantics of §5.2), then the standard template variables (resources and
// builtins) through spec.Substitute.
func (r *hookRunner) resolveCommand(name string, hook *spec.Hook) (string, error) {
	values, missing, err := r.paramValues(name, hook)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		if name == "seed" {
			// B10.4: missing seed credentials is a warning naming the
			// remedy, not a hard failure — the worktree is still useful
			// without a seeded instance, and the descriptor is already
			// written. The seed hook is skipped and the run continues.
			for _, p := range missing {
				fmt.Fprintf(r.stderr,
					"warning: seed parameter %q has no value — no flag, no persisted choice, no default; "+
						"the seed hook is skipped and the worktree starts unseeded. "+
						"fix: pass --param %s=<value>, then re-run (the choice persists once made).\n",
					p, p)
			}
			return "", nil
		}
		return "", fmt.Errorf("hook %s: parameter %q has no value — no flag, no persisted choice, no default; "+
			"pass --param %s=<value>, then re-run", name, missing[0], missing[0])
	}

	// Substitute the params first (spec.Substitute does not know them),
	// then the resources and builtins.
	out := hook.Run
	for _, p := range sortedKeys(values) {
		out = strings.ReplaceAll(out, "{"+p+"}", values[p])
	}
	cmd, err := spec.Substitute(out, r.ctx, r.resolved)
	if err != nil {
		return "", fmt.Errorf("hook %s: resolving the command: %v", name, err)
	}
	return cmd, nil
}

// paramValues resolves every declared parameter of a hook. The returned
// map holds the values used; missing lists the parameters with no value
// anywhere. A sticky value decided this run is recorded in chosen, for the
// caller to persist; an explicit value that contradicts a persisted sticky
// choice is used with a warning — a silent override of a persisted choice
// is a surprise waiting several runs to be discovered (§5.2).
func (r *hookRunner) paramValues(name string, hook *spec.Hook) (map[string]string, []string, error) {
	values := map[string]string{}
	var missing []string
	for _, p := range sortedKeys(hook.Params) {
		param := hook.Params[p]
		if v, ok := r.explicit[p]; ok {
			values[p] = v
			if param.Sticky {
				if persisted, ok := r.persisted[p]; ok && persisted != v {
					fmt.Fprintf(r.stderr,
						"warning: hook %s: explicit %s=%s overrides the persisted choice %s=%s\n",
						name, p, v, p, persisted)
				}
				r.chosen[p] = v
			}
			continue
		}
		if v, ok := r.persisted[p]; ok {
			values[p] = v
			continue
		}
		if param.Default != "" {
			values[p] = param.Default
			if param.Sticky {
				r.chosen[p] = param.Default
			}
			continue
		}
		missing = append(missing, p)
	}
	return values, missing, nil
}

// hookByName mirrors the spec package's lookup so the runner can address
// hooks by name without exporting the mapping.
func hookByName(h *spec.Hooks, name string) *spec.Hook {
	switch name {
	case "install":
		return h.Install
	case "prepull":
		return h.Prepull
	case "build":
		return h.Build
	case "start":
		return h.Start
	case "seed":
		return h.Seed
	case "health":
		return h.Health
	}
	return nil
}

// sortedKeys is shared with show.go (the client's table output sorts the
// same way); it is declared there as a generic over map[string]V.

// tailBuffer is the ring buffer the health timeout dump reads: the last
// capacity lines of whatever was written through it.
type tailBuffer struct {
	capacity int
	lines    []string
}

func newTailBuffer(capacity int) *tailBuffer {
	return &tailBuffer{capacity: capacity}
}

// Write implements io.Writer, keeping only the last capacity lines.
func (t *tailBuffer) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line == "" {
			continue
		}
		t.lines = append(t.lines, line)
		if len(t.lines) > t.capacity {
			t.lines = t.lines[len(t.lines)-t.capacity:]
		}
	}
	return len(p), nil
}

// String renders the buffered lines.
func (t *tailBuffer) String() string {
	var b bytes.Buffer
	for _, line := range t.lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
