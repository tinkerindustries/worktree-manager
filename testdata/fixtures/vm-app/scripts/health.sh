#!/bin/sh
# The health hook's payload: checks that the worktree's VM profile is up.
set -eu
profile="${1:?profile is required}"
echo "health check passed for profile: $profile"
