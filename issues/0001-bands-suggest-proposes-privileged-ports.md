# `wt bands suggest` proposes privileged ports

- **Status:** open
- **Found:** 2026-09-12, on Ubuntu 24.04 while bringing the repository up on a Linux host
- **Component:** `internal/coord` (`bands.suggest`)
- **Severity:** the primitive's output is unusable as given; nothing downstream refuses it

## What happens

`bands suggest` searches the port space from 1 upward. Against an empty
ledger it therefore proposes base 1, and a band of ports 1–8:

```
$ wt bands suggest
app       worktree-manager
RESOURCE  BASE  SPAN  RANGE
coord     1     8     1..8
```

Ports below 1024 are privileged: binding one needs root or
`CAP_NET_BIND_SERVICE` on Linux, and root on macOS. A development
coordinator started by `wt init`'s `start` hook runs as the developer, so
every port in that band is unbindable. Anyone who takes the suggestion at
face value reserves a band whose first eight ports can never be bound, and
finds out at the first `wt init` rather than here.

## Why the search starts at 1

`suggestBase` (`internal/coord/onboard.go:166`) has no floor:

```go
for base := 1; base+span-1 <= 65535; base++ {
```

The bound is stated the same way in the no-base-fits note
(`internal/coord/onboard.go:140`, "anywhere in 1..%d"), so 1 is deliberate
as the low end of the search space rather than an off-by-one. What is
missing is the idea that part of that space is not available to a
non-root process.

## Nothing downstream catches it

The suggestion is not refused anywhere later, so the unusable band can be
reserved and allocated from:

- `parseBases` accepts any port in 1..65535
  (`internal/cli/spec_explain.go:173`), so `--base coord=1` is valid input.
- `reserveBand` checks only completeness against the spec — every port
  resource has a base, every base names a port resource
  (`internal/coord/bands.go:67`–`79`). It does not range-check the base.
- There is no privileged-port floor anywhere in `internal/`.

So the failure surfaces as a bind error from the application's own start
hook, a long way from the decision that caused it.

(Read, not executed: reserving base 1 against the live ledger was not
attempted, because it would have written a real band. The claim above is
from the validation code, which has no such check.)

## A second, subtler case

The same absence of a floor means a suggestion can also land inside the
ephemeral port range, which the kernel hands out as source ports for
outbound connections:

```
$ cat /proc/sys/net/ipv4/ip_local_port_range
32768   60999
```

A band there is bindable, so nothing refuses it and nothing fails
immediately — it fails intermittently, when a transient outbound
connection happens to hold the port at the moment a worktree starts. That
is a worse failure than the privileged one because it is not
reproducible. `bands suggest` has no knowledge of the range today.

## Why this matters more than it looks

`bands suggest` is one of the two onboarding primitives (with `ports
scan`) that the worktree-onboarding skill leans on for phase 3. The
division of labour is that the skill "chooses only where the bases go,
not how large they are" — the coordinator computes the span
(`bandSpan`, `internal/coord/bands.go:240`: slot ceiling × ports per slot)
and proposes the position. A primitive whose proposal cannot be used
pushes that judgment back onto the skill while still looking like an
answer.

For contrast, this repository's own recorded band is `coord=7840` — a
human choice, clear of both problems, and the value the artefacts in
`CLAUDE.md` record.

## The decision needed

What floor should the search start from? This is a policy call, which is
why it is written up rather than fixed:

1. **1024** — the correctness minimum. Fixes the privileged case and
   nothing else; still free to propose inside the ephemeral range.
2. **Above the ephemeral range** — awkward, because the range is
   configurable and differs per platform (and reading
   `/proc/sys/net/ipv4/ip_local_port_range` is Linux-only, which would
   put a `GOOS` branch outside `internal/platform`, the only package
   permitted one).
3. **A fixed conventional floor** such as 7000 — clear of privileged
   ports, clear of the usual ephemeral ranges, and clear of the common
   development ports (3000, 4200, 5173, 8080) that a suggestion should
   not squat on. Arbitrary, but arbitrary and stated.

Option 3 is the one that matches how the rest of the system behaves:
a documented constant with the reasoning beside it, no inference and no
platform branch. Option 1 alone would leave the intermittent failure in
place.

Whichever is chosen, the reasoning belongs next to the constant, and the
no-base-fits note should state the floor rather than "1..65535" so the
bound it reports is the bound it searched.

## Not addressed here

`suggestBase`'s busy set is registered band ranges plus host-global
reservations. It does not consult `ports scan`, so a suggestion may name
a range something on the machine is already listening on. That appears
deliberate — the docstring says the suggestion is "constrained, never
classifying", and combining the two primitives is the skill's job — so it
is recorded as context, not as part of this issue.
