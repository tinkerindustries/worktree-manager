# `staticcheck ./...` reports unused code on Windows

- **Status:** open
- **Found:** 2026-09-24, on Windows 11 while checking an unrelated change in `internal/treecheck`
- **Component:** `internal/platform`
- **Severity:** a Windows contributor cannot get the "must print nothing" gate CLAUDE.md asks for; CI does not notice

## What happens

On a Windows host, `staticcheck ./...` prints three findings and exits
non-zero:

```
internal\platform\listeners.go:150:6: func renderCommand is unused (U1000)
internal\platform\listeners.go:164:6: func parsePids is unused (U1000)
internal\platform\task_windows.go:288:5: var errNoTaskScheduler is unused (U1000)
```

`GOOS=linux staticcheck ./...` and `GOOS=darwin staticcheck ./...` on the
same checkout both print nothing, and CI runs staticcheck on
`ubuntu-latest` only, so the gate stays green.

## How to reproduce

1. On Windows, at `origin/main` (b345056), run `staticcheck ./...`.
2. Run `GOOS=linux staticcheck ./...` for comparison.

## Why

`renderCommand` and `parsePids` sit in the shared `listeners.go` but are
only called from the unix listener scan; `errNoTaskScheduler` is declared
in `task_windows.go` and never read. All three date from the initial
implementation (86eb1f4).

## What would fix it

Move the two helpers into the unix-only listener file, drop or use
`errNoTaskScheduler`, and consider a `GOOS=windows staticcheck ./...` step in
CI so the Windows build is linted as well as compiled.
