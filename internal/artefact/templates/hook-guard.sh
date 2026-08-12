#!/bin/sh
# PreToolUse enforcement hook, generated per repo by worktree-manager
# phase 7 (docs/design/07-agent-surface.md §6, docs/ARCHITECTURE.md §9.6).
# Opt-in: .claude/settings.json declares it only when the repo's developer
# confirmed enforcement during onboarding (C6).
#
# It calls `wt guard --json` — the local verb that reads git and the
# descriptor and opens no socket — so an agent session keeps working with
# the coordinator down, and the coordinator never sits in the latency path
# of a file write.
#
# Caching (the phase-7 decision on plan.md §9.2, 07-agent-surface.md
# §6.3): the classification is cached per session in WT_GUARD_CACHE, keyed
# on cwd. The cache is validated by stat; when the worktree is removed
# mid-session its root vanishes, the cache drops, and the next call
# reclassifies — failing open with a one-time note, because a session
# whose worktree vanished must keep working elsewhere.
set -u

export WT_GUARD_CACHE="${WT_GUARD_CACHE:-${TMPDIR:-/tmp}/wt-guard-cache}"

if ! command -v wt >/dev/null 2>&1; then
  echo "note: wt is not on PATH; this call was allowed (the guard failed open)" >&2
  echo '{"hookSpecificOutput":{"hookEventName":{"permissionDecision":"allow"}}}'
  exit 0
fi

out="$(wt guard --json 2>&1)"
code=$?
case "$code" in
0)
  echo '{"hookSpecificOutput":{"hookEventName":{"permissionDecision":"allow"}}}'
  exit 0
  ;;
3)
  # Denied. The full reason is on stderr already (wt guard prints it);
  # echo the structured denial for the hook protocol and the reason for
  # the model, so it corrects itself and retries.
  reason="$(printf '%s\n' "$out" | sed -n 's/.*"reason": "\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$reason" ] || reason="denied by wt guard"
  printf '{"hookSpecificOutput":{"hookEventName":{"permissionDecision":"deny","denyReason":"%s"}}}\n' "$reason"
  exit 2
  ;;
*)
  # Fail open and say so once: a guard that denies on its own errors makes
  # the session unusable.
  echo "note: wt guard could not classify (exit $code); this call was allowed — the guard fails open on its own errors" >&2
  echo '{"hookSpecificOutput":{"hookEventName":{"permissionDecision":"allow"}}}'
  exit 0
  ;;
esac
