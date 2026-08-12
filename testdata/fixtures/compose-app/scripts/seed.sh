#!/bin/sh
# The seed hook's payload: initialises the database for a worktree's profile.
# Phase 5 runs this with the sticky `seed_profile` parameter.
set -eu
profile="${1:-dev}"
echo "seeding profile: $profile"
