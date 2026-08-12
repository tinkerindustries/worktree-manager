# vm-app

A repository whose workload needs one VM (and one docker daemon) per
worktree, plus a subnet per worktree out of a shared address pool. It stands
in for the `mini-infra`-shaped pilot of the worktree-manager design.
Phase 8 proves the `machine` and `cidr` drivers against it; phase 0 only
needed it to exist far enough for `wt.yaml` to describe it honestly.

## Layout

- `scripts/provision.sh` — the install hook's payload: waits for the
  worktree's VM (the machine driver's apply launched its warm-up in the
  background) and creates the worktree's egress network on the VM's docker
  with the worktree's `/22`.
- `scripts/health.sh` — the health hook's payload: the VM is up and its
  egress network carries exactly this worktree's `/22`.
- `bin/server.go` — the worktree's allocation reader: prints the VM
  profile and the egress cidr from the descriptor, and refuses to run in
  an uninitialised worktree naming `wt init`. Built by the build hook.
- `wt.yaml` — the committed spec; the adoption signal.

## Resources (see wt.yaml)

One `machine` per worktree — a Colima profile on macOS, a WSL2 distro on
Windows — capped at 4 concurrent instances, and one `cidr`: a `/22` per
worktree carved out of `172.30.0.0/16`. The pool holds 64 `/22` blocks and
the slot ceiling is 100, so the pool is small enough to exhaust: slot 65 has
no block, and `on_exhaustion: shared-pool` falls back loudly rather than
failing (03-drivers §4.3).

## Running

Two worktrees of `vm-app` run side by side on macOS: `wt init` in each
allocates a slot, the machine driver creates and starts a Colima profile
per worktree (warm-up in the background), and the install hook creates each
worktree's egress network with its own `/22` — disjoint by construction,
four `/24`s per worktree, no coordination (B5.1). The fifth worktree is
refused by the capacity guard, naming what is running and how to tear one
down. The live gate needs Colima and is reported not_run by the phase-8
implementation run (no Colima on the implementation machine); the driver's
rails are proved against a fake runner.
