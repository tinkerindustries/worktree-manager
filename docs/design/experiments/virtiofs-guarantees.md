# Filesystem guarantees across a virtiofs bind mount

**Date:** 2026-08-11
**Question:** do `O_CREAT|O_EXCL` and `rename` behave atomically when a directory is shared between the macOS host and a container, as [02-coordination §8 and §9](../02-coordination.md) assume?
**Script:** [`virtiofs-probe.py`](virtiofs-probe.py)

## Environment

| | |
|---|---|
| Host | macOS, Apple Silicon |
| Docker | Docker Desktop, containerd snapshotter enabled |
| Mount | `virtiofs2 on /mnt type virtiofs (rw,nosuid,nodev,relatime,ignore_atime,no_xattr)` |
| Container | `python:3.12-alpine`, linux/arm64 |
| Shared directory | under `/private/tmp`, bind-mounted at `/mnt` |

## Method

Timestamps travel in filenames rather than file contents, so the clock-synchronisation phase cannot be confounded by the content tearing a later phase measures.

**Clock offset.** Filesystem NTP: the host creates `ping-k-<t1>`, the container spots it and creates `pong-k-<tc>`, the host spots that at `t2`. Sixty exchanges; the one with the smallest round trip gives the tightest offset estimate.

**Exclusive create.** Workers on both sides sweep the same index space, each attempting `open(lock-i, O_CREAT|O_EXCL)` at the same absolute instant, with the measured clock offset applied on the container side. Two winners for one index would mean exclusive creation is not atomic across the boundary.

**Negative-lookup poisoning.** The failure the design actually fears. Per round the container stats a name that does not exist yet, signals, waits for the host to create it, and only then attempts an exclusive create. If the container trusted a cached negative lookup, it would succeed.

**Atomic replace.** A writer publishes 256 KiB generations through a temp file in the same directory followed by `os.replace`; a reader on the far side checks that every observation is a single complete generation. Run in both directions.

**Advisory locks.** One side holds, the other attempts non-blocking, in both directions, for `flock` and for POSIX record locks. Same-side controls confirm the mechanism works at all.

## Results

### Timing

| Measure | Value |
|---|---|
| Clock offset, container minus host | −0.75 ms |
| Minimum round trip | 0.39 ms |
| Median round trip | 0.70 ms |

One-way visibility of a newly created name is roughly 0.2 ms. Metadata propagation is not a source of the coarse delay that would have made the whole approach unworkable.

### Exclusive create

| Run | Workers | Pacing | Rounds | Won once | Won twice | Unclaimed |
|---|---|---|---|---|---|---|
| 1 | 2 host, 2 container | 4 ms | 1500 | 1500 | 0 | 0 |
| 2 | 3 host, 3 container | 1.2 ms | 4000 | 4000 | 0 | 0 |

In run 2 the host took 789 indices and the container 3211, so both sides were genuinely contesting throughout rather than one running ahead of the other.

Negative-lookup poisoning: 400 rounds, 400 correct `EEXIST`, zero false wins.

### Atomic replace

| Direction | Generations written | Reads | Torn | Short | Absent |
|---|---|---|---|---|---|
| Host writes, container reads | 44,040 | 6,603 | 0 | 0 | 0 |
| Container writes, host reads | 13,709 | 199,442 | 0 | 0 | 0 |

The reader never observed a mixed generation, a short file, or a window in which the target did not exist.

### Advisory locks

| Holder | Attempted by | Acquired |
|---|---|---|
| host | container | yes — no propagation |
| container | host | yes — no propagation |
| host | host (control) | no, `EAGAIN` |
| container A | container B | no, `EAGAIN` |

POSIX record locks behave identically: no propagation host↔container, correct blocking within a side.

## Conclusions

**Exclusive creation and rename are atomic across this boundary.** The lock in 02-coordination §8 and the atomic write in §9 are sound as designed on this configuration.

**Neither advisory locking mechanism crosses the boundary.** `flock` was already ruled out in overview §6.2; POSIX record locks are ruled out for the same reason and were not previously considered. The exclusive-create lock is not merely the safer option, it is the only one.

**The boundary is macOS↔VM, not container↔container.** Two containers sharing one host mount are the same Linux kernel, so locks held in that kernel are visible between them. The host is the odd participant. This matters for reasoning about who is racing whom, and it means a lock that appears to work in an all-container test will fail the moment the host joins.

**The generation counter's justification changes but it survives.** 02-coordination §8 introduced it partly because `O_CREAT|O_EXCL` might not be atomic here. That risk did not materialise. It is still required for the case the lock cannot cover: a holder that hangs past the stale timeout while a second process legitimately breaks the lock and writes. Keep it, on that argument alone.

## Limits of this result

One host, one Docker Desktop version, one filesharing implementation. Untested and not implied by these numbers:

- Colima and Lima, which this machine also runs, and which use a different mount stack;
- Docker Desktop's gRPC-FUSE and legacy osxfs implementations;
- WSL2 bind mounts on Windows;
- a `$HOME` on a network filesystem, where none of these guarantees hold and the design has never claimed they do.

Absence of an observed failure bounds a rate rather than proving impossibility. Across 5,500 contested exclusive creates with zero double wins, the rule of three puts the 95% upper bound on the failure rate near 5 × 10⁻⁴. That is low enough to build on, and it is not zero — which is the second argument for keeping the generation counter.
