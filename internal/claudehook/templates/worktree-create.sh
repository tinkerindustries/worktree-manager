#!/bin/sh
# WorktreeCreate hook, written by `wt claude install`.
#
# Claude Code hands worktree creation to this hook whenever one is
# registered and never falls back to `git worktree add` on its own, so this
# script has to answer for every repository on the machine rather than the
# adopted ones alone. A repository with a wt.yaml at its root — the
# adoption signal — gets its worktree where the spec's worktrees: block
# says and an environment from `wt init`. Every other repository gets a
# plain worktree under .claude/worktrees, which is where Claude Code puts
# one itself, and nothing else is said or done.
#
# stdin   JSON: {"name": "<slug>", "cwd": "<session directory>", ...}
# stdout  the absolute path of the created worktree, and nothing else.
#
# The wt binary this script calls is recorded in the managed block at the
# end of the file. Re-running `wt claude install` refreshes the block and
# leaves edits above it alone.
set -eu

self="$0"
hook_dir="$(CDPATH= cd -- "$(dirname -- "$self")" && pwd)"
log_file="$hook_dir/wt-worktree-hook.log"

log() {
  printf '%s create: %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$1" >>"$log_file" 2>/dev/null || true
  printf 'wt-worktree-create: %s\n' "$1" >&2
}
# log_quiet is for a notice rather than a fault: Claude Code may or may not
# surface a successful hook's stderr, so the log is the copy that keeps.
log_quiet() {
  printf '%s create: %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$1" >>"$log_file" 2>/dev/null || true
  printf 'wt: %s\n' "$1" >&2
}
die() { log "$1"; exit 1; }

# One field from a flat JSON object, jq where it exists and sed where it
# does not. The two values read here are a slug and an absolute path, so
# the only escapes worth undoing are the ones a path can carry.
json_field() {
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$2" | jq -r --arg k "$1" '.[$k] // empty'
    return
  fi
  printf '%s' "$2" \
    | sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\(\([^"\\]\|\\.\)*\)".*/\1/p' \
    | sed -e 's|\\/|/|g' -e 's|\\\\|\\|g'
}

input="$(cat)"
slug="$(json_field name "$input")"
cwd="$(json_field cwd "$input")"
[ -n "$slug" ] || die "the hook input carried no name"
[ -n "$cwd" ] || cwd="$(pwd)"

# The main checkout, never the session's cwd: a worktree created from
# inside a worktree must not land underneath the worktree it came from.
common="$(git -C "$cwd" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" \
  || die "$cwd is not a git repository"
root="$(dirname "$common")"

# Branch from what the session has checked out, which is what Claude Code's
# own worktree creation does.
base="$(git -C "$cwd" rev-parse HEAD 2>/dev/null)" \
  || die "$cwd has no HEAD to branch from"

wt="$(sed -n 's/^# wt-field: wt=//p' "$self" | head -n1)"
[ -x "$wt" ] || wt="$(command -v wt 2>/dev/null || true)"

if [ -f "$root/wt.yaml" ] && [ -n "$wt" ] && [ -x "$wt" ]; then
  adopted=1
  path="$("$wt" spec path --slug "$slug" --root "$root" 2>>"$log_file")" \
    || die "wt spec path refused the slug $slug"
else
  adopted=0
  path="$root/.claude/worktrees/$slug"
fi

if [ -e "$path" ]; then
  die "$path already exists"
fi

if git -C "$root" show-ref --verify --quiet "refs/heads/$slug"; then
  log "branch $slug already exists; checking it out into $path"
  git -C "$root" worktree add "$path" "$slug" >>"$log_file" 2>&1 \
    || die "git worktree add failed; see $log_file"
else
  git -C "$root" worktree add -b "$slug" "$path" "$base" >>"$log_file" 2>&1 \
    || die "git worktree add failed; see $log_file"
fi

# An unadopted repository is finished here: a worktree in Claude Code's own
# location, made the way Claude Code would have made it. The one thing said
# is how to change that, and it is said once, on stderr, without failing
# anything.
if [ "$adopted" = 0 ]; then
  log_quiet "$root has no wt.yaml, so this worktree gets no isolated ports, databases or state paths. To give this repository per-worktree environments, run the worktree-onboarding skill in it: /worktree-onboarding"
  printf '%s\n' "$path"
  exit 0
fi

# A failed wt init leaves a usable worktree with no environment. Report it
# and hand the path back anyway: failing here would throw away the worktree
# too, and the repo's SessionStart tripwire says the same thing on arrival.
description="${WT_HOOK_DESCRIPTION:-claude code worktree $slug}"
if ! "$wt" init --cwd "$path" --description "$description" >&2; then
  log "wt init failed in $path; the worktree has no environment yet"
fi

printf '%s\n' "$path"
