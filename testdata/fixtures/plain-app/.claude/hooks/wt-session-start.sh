#!/bin/sh
# SessionStart tripwire, generated per repo by worktree-manager phase 7
# (docs/design/07-agent-surface.md §7, docs/ARCHITECTURE.md §9.5). On
# session start it classifies cwd and prints one sentence: a linked
# worktree of an adopted repo with no descriptor gets "run wt init"; one
# with a descriptor gets a one-line summary (slug, slot, ports). Detection
# and a sentence, no allocation — a repo that wants allocation configures
# this hook to perform it, which makes the choice explicit.
#
# Everything this script needs to know about the repo comes from its own
# managed block at the end of this file (the descriptor filename and the
# port resources); regeneration refreshes the block, never this script.
set -u

# The descriptor filename, from this file's managed block.
filename="$(sed -n 's/^# wt-field: descriptor=//p' "$0" | head -n1)"
[ -n "$filename" ] || exit 0

# Find the repo root: walk up for wt.yaml, the same rule the binaries use.
dir="$(pwd)"
root=""
while [ -n "$dir" ] && [ "$dir" != "/" ]; do
  if [ -f "$dir/wt.yaml" ]; then
    root="$dir"
    break
  fi
  dir="$(dirname "$dir")"
done
[ -n "$root" ] || exit 0          # not adopted: nothing to say

# Primary checkout or linked worktree? git's own answer. The primary
# checkout is slot 0, never managed: nothing to say.
git_dir="$(git -C "$root" rev-parse --git-dir 2>/dev/null)" || exit 0
common="$(git -C "$root" rev-parse --git-common-dir 2>/dev/null)" || exit 0
[ "$git_dir" != "$common" ] || exit 0

if [ ! -f "$root/$filename" ]; then
  echo "This worktree has no environment yet. Run: wt init"
  exit 0
fi

# The one-line summary, read from the descriptor (yaml or json — the
# emitter quotes keys and string scalars in both, so one set of patterns
# reads both). The port resources are exactly the band fields of this
# file's own managed block.
slug="$(sed -n "s/^[[:space:]]*\"*slug\"*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$root/$filename" | head -n1)"
slot="$(sed -n "s/^[[:space:]]*\"*slot\"*:[[:space:]]*\([0-9]*\).*/\1/p" "$root/$filename" | head -n1)"
ports=""
for name in $(sed -n 's/^# wt-field: band \([a-z0-9-]*\)=.*/\1/p' "$0"); do
  value="$(awk -v name="$name" '
    $0 ~ "^[ \t]*\"?" name "\"?:" { inres = 1 }
    inres && match($0, /[0-9]+/) { print substr($0, RSTART, RLENGTH); exit }
  ' "$root/$filename")"
  if [ -n "$value" ]; then
    ports="$ports $name=$value"
  fi
done
echo "wt: worktree $slug (slot $slot):$ports"

# --- managed by wt; edits below are overwritten ---
# wt-field: app=plain-app
# wt-field: band api=8200
# wt-field: descriptor=wt-env.json
# wt-field: resources=api, db, cache, shared_db
# wt-field: shared=https://api.example.com/v1, shared_db
# --- end ---
