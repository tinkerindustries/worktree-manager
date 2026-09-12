#!/bin/sh
# WorktreeCreate hook, written by `wt claude install`.
#
# Claude Code hands worktree creation to this hook whenever one is
# registered and never falls back to `git worktree add` on its own, so this
# script has to answer for every repository on the machine rather than the
# adopted ones alone. A repository with a wt.yaml at its root — the
# adoption signal — gets its worktree where the spec's worktrees: block
# says, branched from what that block names, and an environment from
# `wt init`. Every other repository gets a plain worktree under
# .claude/worktrees, which is where Claude Code puts one itself.
#
# stdin   JSON: {"name": "<slug>", "cwd": "<session directory>", ...}
# stdout  the absolute path of the created worktree, and nothing else.
#
# Two environment variables adjust what happens here:
#   WT_HOOK_DESCRIPTION  the description recorded against the allocation
#   WT_HOOK_NO_ENV=1     create the worktree and skip `wt init`, for a
#                        change that does not need the repo's stack up
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
# does not. The values read here are slugs, paths and a git revision, so
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
name="$(json_field name "$input")"
cwd="$(json_field cwd "$input")"
[ -n "$name" ] || die "the hook input carried no name"
[ -n "$cwd" ] || cwd="$(pwd)"

# The main checkout, never the session's cwd: a worktree created from
# inside a worktree must not land underneath the worktree it came from.
common="$(git -C "$cwd" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" \
  || die "$cwd is not a git repository"
root="$(dirname "$common")"

wt="$(sed -n 's/^# wt-field: wt=//p' "$self" | head -n1)"
[ -x "$wt" ] || wt="$(command -v wt 2>/dev/null || true)"
# Absolute, because the spec-path call below runs from $root: a relative
# wt would resolve against the wrong directory there.
case "$wt" in
  "" | /*) ;;
  *) wt="$(CDPATH= cd -- "$(dirname -- "$wt")" && pwd)/$(basename -- "$wt")" ;;
esac

# --- what to make, and from what ----------------------------------------
#
# Claude Code names a worktree after the task that prompted it and appends a
# hash, so the name arriving here is not one anybody chose and not
# necessarily a legal slug. `--name` normalises it rather than refusing,
# because refusing costs a person their worktree over a name they never
# typed. One call answers all three questions — the slug to use downstream,
# where the tree goes, and what it branches from — so the slug the branch
# and the directory take is the same one `wt rm` will later be given.
if [ -f "$root/wt.yaml" ] && [ -n "$wt" ] && [ -x "$wt" ]; then
  adopted=1
  # Run from the main checkout, not the session's cwd. `wt spec path`
  # finds wt.yaml by walking up from where it runs, and --root only says
  # what a relative template resolves against — so a hook invoked from
  # outside the repository would look for the spec in the wrong tree and
  # fail, having already found the right one at $root/wt.yaml above.
  resolved="$(cd "$root" && "$wt" spec path --name "$name" --json --root "$root" 2>>"$log_file")" \
    || die "wt spec path could not resolve the name $name; see $log_file"
  slug="$(json_field slug "$resolved")"
  path="$(json_field path "$resolved")"
  base_spec="$(json_field base "$resolved")"
  [ -n "$slug" ] && [ -n "$path" ] && [ -n "$base_spec" ] \
    || die "wt spec path returned no slug, path or base for $name"
  # A name silently becoming a different slug is the thing worth noticing:
  # it is the identity the branch, the directory and every later `wt rm`
  # will use. wt says this on its own stderr, which goes to the log here,
  # so it is said again where the session can see it.
  if [ "$slug" != "$name" ]; then
    log_quiet "normalised the name \"$name\" to the slug \"$slug\""
  fi
else
  adopted=0
  slug="$name"
  path="$root/.claude/worktrees/$slug"
  base_spec=""
fi

if [ -e "$path" ]; then
  die "$path already exists"
fi

# --- an existing branch is adopted ---------------------------------------
#
# The branch asked for by name may already exist. A worktree removed earlier
# leaves its branch behind, Claude Code names a worktree after the task so
# the name can land on a branch nobody remembers making, and a session
# resumed onto its own branch asks for the branch it was already on.
# Refusing costs the caller a worktree over a name they did not choose, so a
# branch that exists is checked out.
#
# The collision the refusal was about is real, and the answer to it is that
# nothing here is silent. A branch only origin has is fetched first, so the
# local branch starts at the tip somebody else last pushed and the push that
# follows is a fast-forward. A branch in this repository is checked out
# exactly as it stands, and the notice names it and says what it is behind.
# Where a branch cannot be adopted at all — checked out in another worktree,
# or on origin and unobtainable — the refusal names which.
existing=""
if git -C "$root" show-ref --verify --quiet "refs/heads/$slug"; then
  existing="local"
fi

# The remote checks are skipped entirely where there is no origin. A
# repository with local-only branches is an ordinary case, not a degraded
# one, and saying "could not reach origin" about a remote that does not
# exist is noise rather than a caveat.
if [ -z "$existing" ] && git -C "$root" remote get-url origin >/dev/null 2>&1; then
  on_origin=""
  if git -C "$root" show-ref --verify --quiet "refs/remotes/origin/$slug"; then
    on_origin=1
  elif remote_heads="$(git -C "$root" ls-remote --heads origin "$slug" 2>>"$log_file")"; then
    # ls-remote is the only check that sees a branch pushed since the last
    # fetch. It needs the network, so a failure is reported as not knowing
    # rather than as an answer: an offline machine still gets its worktree.
    if [ -n "$remote_heads" ]; then
      on_origin=1
    fi
  else
    log_quiet "could not reach origin to check whether the branch $slug exists there; this repository has no branch of that name, so a new one is made"
  fi
  if [ -n "$on_origin" ]; then
    # Fetched, so the new local branch starts where origin's copy is now and
    # the push that follows is a fast-forward.
    if git -C "$root" fetch --quiet origin "$slug" >>"$log_file" 2>&1 \
        && git -C "$root" show-ref --verify --quiet "refs/remotes/origin/$slug"; then
      existing="origin"
    elif git -C "$root" show-ref --verify --quiet "refs/remotes/origin/$slug"; then
      existing="origin"
      log_quiet "could not fetch origin's copy of $slug; branching from the copy fetched last time, which may be behind"
    else
      die "the branch $slug exists on origin and its commits could not be fetched, so a new branch of that name here would collide with it at push time. Run 'git fetch origin $slug' and ask again."
    fi
  fi
fi

if [ "$existing" = "local" ]; then
  # Said, because the commits made in this worktree land on a branch that
  # already exists, and push there too where it has an upstream.
  log_quiet "the branch $slug already exists in this repository, so this worktree checks it out rather than branching a new one"
  upstream="$(git -C "$root" rev-parse --abbrev-ref --symbolic-full-name "$slug@{upstream}" 2>/dev/null || true)"
  if [ -n "$upstream" ]; then
    behind="$(git -C "$root" rev-list --count "$slug..$upstream" 2>/dev/null || true)"
    if [ -n "$behind" ] && [ "$behind" != "0" ]; then
      log_quiet "the branch $slug is $behind commit(s) behind $upstream"
    fi
  fi
  # One branch cannot be checked out in two worktrees, and git's own refusal
  # names neither the worktree holding it nor the way out. The loop reads the
  # porcelain listing rather than the table so the pairing of a worktree with
  # its branch does not depend on the width of a column.
  occupied="$(
    git -C "$root" worktree list --porcelain 2>/dev/null | while IFS= read -r line; do
      if [ "${line#worktree }" != "$line" ]; then
        holder="${line#worktree }"
      elif [ "$line" = "branch refs/heads/$slug" ]; then
        printf '%s\n' "${holder:-}"
      fi
    done
  )"
  if [ -n "$occupied" ]; then
    die "the branch $slug is already checked out at $occupied, and git will not check one branch out in two worktrees. Work in that one, or remove it with 'wt rm'."
  fi
elif [ "$existing" = "origin" ]; then
  log_quiet "the branch $slug exists on origin and not in this repository, so this worktree is made on origin's copy of it; commits made here push back to origin/$slug"
fi

# --- resolve the base ----------------------------------------------------
#
# An adopted repository branches from what its spec names, which is the
# whole point: branching from the asking session's HEAD means a worktree
# asked for from inside another worktree silently branches off that
# worktree's work, and one asked for from a checkout nobody has pulled in a
# fortnight silently branches off a fortnight-old main. Neither is visible
# at the time.
base=""
if [ -n "$existing" ]; then
  # An existing branch is its own base: the worktree is checked out where
  # that branch points, so worktrees.base is neither resolved nor fetched.
  base=""
elif [ "$adopted" = 1 ]; then
  # A remote-tracking base is only as fresh as the last fetch, so fetch it.
  # Failure here is not fatal — an offline machine still has the ref it
  # fetched last time, and the note says the base may be stale.
  case "$base_spec" in
    */*)
      remote="${base_spec%%/*}"
      branch="${base_spec#*/}"
      if git -C "$root" remote get-url "$remote" >/dev/null 2>&1; then
        git -C "$root" fetch --quiet "$remote" "$branch" >>"$log_file" 2>&1 \
          || log_quiet "could not fetch $base_spec; branching from the copy last fetched, which may be behind"
      fi
      ;;
  esac
  if base="$(git -C "$root" rev-parse --verify --quiet "$base_spec^{commit}" 2>/dev/null)"; then
    log_quiet "branching $slug from $base_spec"
  elif [ "$base_spec" = "origin/main" ]; then
    # The default, not something this repository asked for. A repository
    # with no origin is an ordinary case and must still get a worktree, so
    # this falls back to the main checkout's HEAD and says so — never to
    # the asking session's HEAD, which is the silent wrong parent.
    base="$(git -C "$root" rev-parse HEAD 2>/dev/null)" \
      || die "$root has no origin/main and no HEAD to branch from"
    log_quiet "origin/main does not resolve in this repository, so $slug branches from the main checkout's HEAD ($(git -C "$root" rev-parse --abbrev-ref HEAD 2>/dev/null)). Set worktrees.base in wt.yaml to name a base explicitly."
  else
    die "worktrees.base names $base_spec, which does not resolve in this repository. Fix worktrees.base in $root/wt.yaml, or fetch the ref it names."
  fi
else
  # Unadopted: the contract of this branch is the worktree Claude Code
  # would have made itself, which branches from the session's HEAD. That is
  # kept, and said, so a base nobody intended is at least visible.
  base="$(git -C "$cwd" rev-parse HEAD 2>/dev/null)" \
    || die "$cwd has no HEAD to branch from"
  upstream="$(git -C "$cwd" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
  behind=""
  if [ -n "$upstream" ]; then
    behind="$(git -C "$cwd" rev-list --count "HEAD..$upstream" 2>/dev/null || true)"
  fi
  if [ -n "$behind" ] && [ "$behind" != "0" ]; then
    log_quiet "branching $slug from $(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null) in $cwd, which is $behind commit(s) behind $upstream"
  else
    log_quiet "branching $slug from $(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null) in $cwd"
  fi
fi

# The branch, and the base where there is no branch to adopt. An existing
# branch is checked out as it stands, and origin's copy becomes the local
# branch and its upstream when only origin has it.
if [ "$existing" = "local" ]; then
  git -C "$root" worktree add "$path" "$slug" >>"$log_file" 2>&1 \
    || die "git worktree add failed; see $log_file"
elif [ "$existing" = "origin" ]; then
  git -C "$root" worktree add --track -b "$slug" "$path" "origin/$slug" >>"$log_file" 2>&1 \
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

# WT_HOOK_NO_ENV is the escape hatch for a change that does not need the
# repository's stack up. The hooks: chain is the repo's own — an install, an
# image build, a compose up, a seed, a health check — and paying several
# minutes of it for a worktree that only touches Markdown is a cost worth
# being able to decline. The worktree is still made; only `wt init` is
# skipped, and the repo's SessionStart tripwire says so on arrival.
if [ "${WT_HOOK_NO_ENV:-}" = "1" ]; then
  log_quiet "WT_HOOK_NO_ENV=1, so $slug has no environment yet. Run 'wt init' inside it when you need one."
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
