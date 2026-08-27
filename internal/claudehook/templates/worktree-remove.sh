#!/bin/sh
# WorktreeRemove hook, written by `wt claude install`. The mirror of
# wt-worktree-create.sh: an adopted repository's worktree is torn down by
# `wt rm`, which releases the allocated resources and then removes the
# worktree, and every other repository's is removed by git. Neither is
# forced, so uncommitted work in the worktree stops the removal and Claude
# Code shows why.
#
# Set WT_HOOK_RM_FLAGS=--force to downgrade wt rm's safety checks to
# warnings for these hook-driven removals.
#
# stdin  JSON: {"worktree_path": "<absolute path>", ...}
set -eu

self="$0"
hook_dir="$(CDPATH= cd -- "$(dirname -- "$self")" && pwd)"
log_file="$hook_dir/wt-worktree-hook.log"

log() {
  printf '%s remove: %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$1" >>"$log_file" 2>/dev/null || true
  printf 'wt-worktree-remove: %s\n' "$1" >&2
}
die() { log "$1"; exit 1; }

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
path="$(json_field worktree_path "$input")"
[ -n "$path" ] || die "the hook input carried no worktree_path"

# Already gone is the outcome the caller asked for.
[ -d "$path" ] || exit 0

common="$(git -C "$path" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" \
  || die "$path is not a git worktree"
root="$(dirname "$common")"

wt="$(sed -n 's/^# wt-field: wt=//p' "$self" | head -n1)"
[ -x "$wt" ] || wt="$(command -v wt 2>/dev/null || true)"

if [ -f "$root/wt.yaml" ] && [ -n "$wt" ] && [ -x "$wt" ]; then
  # wt rm refuses to remove the worktree its caller is standing in, so the
  # worktree is addressed by slug from the main checkout. The slug is the
  # worktree directory's name, which is what `wt spec path` produced when
  # the create hook made it.
  # shellcheck disable=SC2086
  "$wt" rm --cwd "$root" --slug "$(basename "$path")" ${WT_HOOK_RM_FLAGS:-} >&2 \
    || die "wt rm refused to remove $path"
else
  git -C "$root" worktree remove "$path" >&2 \
    || die "git worktree remove refused to remove $path"
fi
