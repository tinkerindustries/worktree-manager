# Worktree Manager

Worktree Manager gives each git worktree of a repository its own ports,
Docker Compose project, subnets, state paths and VMs, so several worktrees
of one repository can run side by side without colliding. It is built for
running several coding agents, or several branches, on one machine at once.

A repository opts in by committing a `wt.yaml` at its root. The spec
declares the resources the application uses and which of them each
worktree gets its own copy of. `wt init` allocates those resources for a
worktree, writes them to a descriptor file and a managed block in `.env`,
and runs the repository's hooks. `wt rm` tears them down again.

## Components

- `wt` is the command-line client. It runs once per operation.
- `wtd` is the coordinator, a per-user background process. It owns the
  allocation registry and every operation that changes the machine.
  `wt daemon install` registers it with launchd on macOS, systemd on Linux
  or the Task Scheduler on Windows.
- `WorktreeMenu.app` is an optional macOS menu bar app that shows the
  registry and `wt doctor`'s findings.
- The `worktree-onboarding` skill for Claude Code audits a repository and
  writes its `wt.yaml`.

## Installing

Download the archive for your platform from the
[releases page](../../releases), then:

```sh
tar xzf wt-<version>-<os>-<arch>.tar.gz
cd wt-<version>-<os>-<arch>
./install.sh
wt daemon status
```

On Windows, unpack the zip and run `install.ps1`. The installer checks the
binaries against `SHA256SUMS`, copies them into place and registers the
coordinator. [`RELEASE.md`](RELEASE.md) covers the installer flags,
container installs, the menu bar app and rolling back.

To build from source with Go 1.26:

```sh
go build ./cmd/wt ./cmd/wtd
```

## Using it with Claude Code

```sh
wt claude install
```

This registers `wt` as Claude Code's `WorktreeCreate` and `WorktreeRemove`
hooks and installs the onboarding skill. A worktree Claude Code creates in
a repository with a `wt.yaml` then gets its environment allocated. Other
repositories get a plain worktree.

To adopt a repository, run `/worktree-onboarding` in Claude Code from its
main checkout.

## Everyday commands

| Command | What it does |
|---|---|
| `wt init --description <text>` | allocate this worktree's resources and run its hooks |
| `wt show --brief` | this worktree's ports and paths, and what it shares with other worktrees |
| `wt start` | run the hooks that bring the stack up |
| `wt list` | every allocated worktree across every repository |
| `wt doctor` | report drift, with the command that fixes each finding |
| `wt rm --slug <slug>` | check the worktree is safe to remove, tear it down and remove it |
| `wt cleanup` | remove worktrees whose pull requests have merged |

`wt help` lists every verb and flag.

## Documentation

- [`docs/wt.md`](docs/wt.md): the model a worktree runs under, as an
  adopted repository sees it.
- [`docs/design/03-drivers.md`](docs/design/03-drivers.md): the `wt.yaml`
  schema.
- [`ARCHITECTURE.md`](ARCHITECTURE.md): how the code is laid out.
- [`TESTING.md`](TESTING.md): how the test layers work.
- [`RELEASE.md`](RELEASE.md): cutting and installing a release.
- [`testdata/fixtures/plain-app`](testdata/fixtures/plain-app): a small
  adopted repository to read alongside the docs.

## Licence

MIT. See [`LICENSE`](LICENSE).
