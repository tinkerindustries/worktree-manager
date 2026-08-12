#!/bin/sh
# The health hook's payload: the worktree's VM is up, and its egress
# network carries exactly this worktree's /22. A second worktree's network
# with a different subnet fails this check, which is how a cidr collision
# would surface — and a VM that never came up fails it as "docker:
# unreachable", the honest shape of a machine that did not materialise.
set -eu

profile=""
subnet=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --profile) profile="${2:?--profile needs a value}"; shift 2 ;;
    --subnet) subnet="${2:?--subnet needs a value}"; shift 2 ;;
    *) echo "health.sh: unknown argument: $1" >&2; exit 2 ;;
  esac
done
: "${profile:?--profile is required}"
: "${subnet:?--subnet is required}"

docker --context "$profile" info >/dev/null
actual="$(docker --context "$profile" network inspect wt-egress --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}')"
if [ "$actual" != "$subnet" ]; then
  echo "health.sh: the $profile egress network wt-egress carries '$actual', want '$subnet' — the worktree's cidr allocation moved, or the network belongs to another worktree" >&2
  exit 1
fi
echo "health check passed: $profile up, wt-egress on $subnet"
