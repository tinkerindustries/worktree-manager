---
name: worktree-remove
description: Remove a worktree of this repository with wt rm. Use when the user asks to tear a worktree down or is done with it. Do not use when a run merely failed — a failure is not a reason to remove the worktree.
---

# worktree-remove

Remove one worktree of this repository with `wt rm`. The safety checks
live in the binary; this skill's job is to not fight them.

## The rule that matters

`wt rm` exits 3 when a safety check refuses — uncommitted changes,
unpushed commits, an open pull request. On exit 3, stop and ask the user.
Never reach for `--force`: an agent that responds to a refusal by adding
`--force` has defeated the check rather than satisfied it.

`wt rm` exits 4 when a check could not run at all (for example `gh` is
missing). Report what could not be checked; do not proceed on a missing
safety check.

## Rails

- Never `--force` a `git worktree remove`. Git refusing is signal that a
  check missed something.
- Never delete the worktree you are standing in — `wt` refuses too.
- Never delete the remote branch; the pull request points at it.
- Never pick a target on its own: "the most recent worktree" is not an
  inference to make. Ask which one.
- Never clean up on a failure path. A failed smoke, a pull request not
  opened, mid-investigation — leaving the worktree alive is correct.
  Whether to remove after a failure depends on what the session was
  trying to do, which no tool can see and only the user knows.

## Anti-triggers

"The run failed" and "done with worktree" are close in wording and
opposite in intent. When in doubt, ask.
