#!/bin/sh
# plain-app's entry point: resolves its configuration from the environment,
# which the worktree allocation delivers through the .env managed block.
# The port comes from the allocation; the state paths from the allocation.
set -eu
api_port="${API_PORT:?API_PORT is unset}"
db_path="${DB_PATH:?DB_PATH is unset}"
cache_path="${CACHE_PATH:-$db_path.cache}"
echo "plain-app listening on 127.0.0.1:$api_port"
echo "db: $db_path"
echo "cache: $cache_path"
