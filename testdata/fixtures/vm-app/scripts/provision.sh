#!/bin/sh
# The install hook's payload: waits for the worktree's VM — the machine
# driver's apply launched its warm-up in the background, measured in
# minutes (B4.4), so this wait is what init actually blocks on — then
# creates the worktree's egress network on the VM's docker with the
# worktree's /22. The subnet argument is the worktree's own cidr
# allocation, resolved from the descriptor by the hook runner, never
# hardcoded.
set -eu

profile=""
subnet=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --profile) profile="${2:?--profile needs a value}"; shift 2 ;;
    --subnet) subnet="${2:?--subnet needs a value}"; shift 2 ;;
    *) echo "provision.sh: unknown argument: $1" >&2; exit 2 ;;
  esac
done
: "${profile:?--profile is required}"
: "${subnet:?--subnet is required}"

# The VM's dockerd warms up in the background; wait for it, bounded, so a
# dead VM fails the hook instead of hanging init forever.
attempts=0
until docker --context "$profile" info >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 60 ]; then
    echo "provision.sh: the $profile VM's docker did not come up within 5 minutes; check 'colima status $profile'" >&2
    exit 1
  fi
  sleep 5
done

if ! docker --context "$profile" network inspect wt-egress >/dev/null 2>&1; then
  docker --context "$profile" network create --subnet "$subnet" wt-egress >/dev/null
fi
echo "provisioned $profile: egress network wt-egress on $subnet"
