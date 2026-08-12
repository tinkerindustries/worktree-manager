#!/bin/sh
# The install hook's payload: provisions the worktree's VM profile. Phase 8
# runs this against the allocated machine resource; the machine driver's
# apply already created the profile by then.
set -eu
profile="${1:?profile is required}"
echo "provisioning VM profile: $profile"
