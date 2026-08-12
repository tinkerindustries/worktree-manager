# vm-app

A repository whose workload needs one VM (and one docker daemon) per
worktree, plus a subnet per worktree out of a shared address pool. It stands
in for the `mini-infra`-shaped pilot of the worktree-manager design.
Phase 8 proves the `machine` and `cidr` drivers against it; phase 0 only
needs it to exist far enough for `wt.yaml` to describe it honestly.

## Layout

- `scripts/provision.sh` — the install hook's payload: provisions the
  worktree's VM profile.
- `scripts/health.sh` — the health hook's payload: checks the profile.
- `wt.yaml` — the committed spec; the adoption signal.

## Resources (see wt.yaml)

One `machine` per worktree — a Colima profile on macOS, a WSL2 distro on
Windows — capped at 4 concurrent instances, and one `cidr`: a `/22` per
worktree carved out of `172.30.0.0/16`. The pool holds 64 `/22` blocks and
the slot ceiling is 100, so the pool is small enough to exhaust: slot 65 has
no block, and `on_exhaustion: shared-pool` falls back loudly rather than
failing (03-drivers §4.3).

## Running

Not runnable as a worktree pair yet; that is phase 8.
