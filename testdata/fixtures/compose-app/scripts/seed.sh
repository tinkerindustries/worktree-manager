#!/bin/sh
# The seed hook's payload: records which profile this worktree seeds with.
# The database itself is a file the server creates on first use, so the
# seed is honest about what it did: it chose the profile, and it copied
# nothing.
set -eu
profile="dev"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --profile)
      if [ "$#" -lt 2 ]; then
        echo "seed: --profile needs a value" >&2
        exit 2
      fi
      profile="$2"
      shift 2
      ;;
    *)
      echo "seed: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done
echo "seeded profile '$profile': the database file is created by the server on first use; nothing was copied"
