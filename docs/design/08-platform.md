# M8 — Platform

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** T15, and every operation whose behaviour depends on the operating system or the filesystem underneath it.
**Depended on by:** M1, M2, M3, M4, M6.

## 1. What this module answers

Everything that behaves differently on macOS, Linux and Windows, in one place, listed.

B15.2 asks for exactly this: isolate platform-specific surfaces to one module and enumerate them. The enumeration in §3 is the deliverable — a surface that is not on that list is a surface nobody checked.

B15.1 wanted one implementation in the repo's own stack, with no parallel `.sh` and `.ps1` wrappers, which mini-infra reached by retiring theirs for a single TypeScript CLI. A single binary satisfies that structurally, so the requirement now only constrains what this module must cover rather than how the tool is packaged.

## 2. The rule

Every call in this module answers one of three things: what the platform's mechanism is, whether it is available, or why it is not. A caller never branches on the operating system itself. When a caller writes `if darwin`, the branch belongs here instead.

## 3. Surface inventory

| Surface | macOS | Linux | Windows | Used by |
|---|---|---|---|---|
| Listener discovery | `lsof` | `lsof` or `/proc/net` | `netstat -ano` + `tasklist` | M4 §6 |
| Process termination | SIGTERM, then SIGKILL | same | `taskkill`, then `taskkill /F` | M4 §6 |
| VM or daemon driver | Colima profile | native dockerd | WSL2 distro | M3 §4.5 |
| Scheduler | launchd agent | systemd timer or cron | Task Scheduler | M6 §7.2 |
| Hook execution | direct | direct | shell resolution for `.cmd` and `.bat` shims | M4 §5 |
| Home and state directory | `$HOME` | `$HOME` | `%USERPROFILE%` | M2 §2 |
| Path realisation | resolve `/tmp`, `/System/Volumes/Data` | resolve symlinks | drive letters, UNC, long paths | M1 §3.1 |
| Path comparison casing | case-insensitive by default | case-sensitive | case-insensitive | M1 §3.1 |
| Port probe socket options | no `SO_REUSEADDR` on the probe | `SO_REUSEADDR` | no `SO_REUSEADDR` | M3 §4.1 |
| Exclusive file creation, rename, fsync | APFS, and virtiofs when mounted | ext4/overlayfs, bind mounts | NTFS | M2 §8, §9 |
| File permissions | mode bits | mode bits | ACLs | M2 §2 |
| Capability probes | socket, namespaces | socket, namespaces | socket | overview §6.4 |
| One-time prerequisites | Colima installed | — | WSL2 base tarball built | §6 |
| View id location | outside `WT_HOME` | outside `WT_HOME` | outside `WT_HOME` | M1 §5.1 |

The rest of this document covers the entries that carry a trap rather than merely a different command name.

## 4. Traps

### 4.1 The port probe inverts between platforms

`SO_REUSEADDR` means different things, and using one setting everywhere produces a wrong answer on one platform or the other.

On Linux and macOS it permits binding an address in `TIME_WAIT` but does not permit binding while another socket is actively listening. Setting it makes the probe *more* accurate, because a recently-closed port stops reporting as held.

On Windows it permits binding an address another socket already holds. Setting it makes the probe report free when the port is in use, which is the exact failure the probe exists to prevent.

So the probe sets `SO_REUSEADDR` on unix and leaves it unset on Windows. This is the clearest example of why B15.2 wants the enumeration: the code reads as one line of socket setup, and the consequence of getting it wrong is a slot allocated on top of a running service.

### 4.2 Containment must match the filesystem's casing

M1 §3.1's containment test decides whether an agent's write lands inside its worktree. A case-sensitive comparison on a case-insensitive filesystem can be walked straight past, because `/Users/geoff/repos/...` and `/Users/geoff/Repos/...` are the same directory on APFS and on NTFS and differ as strings.

The comparison follows the filesystem, not the operating system, since a case-sensitive volume on macOS is a supported configuration. That means probing the actual mount rather than assuming from `GOOS`.

This is the one platform detail in the design where getting it wrong is a security property rather than an inconvenience, because D1's guard rests on it.

### 4.3 Windows has no SIGTERM

M4 §6's reaper sends SIGTERM, waits about three seconds, then escalates to SIGKILL. Windows has no equivalent of the first step for an arbitrary process: `taskkill` without `/F` posts a close message that console applications do not receive and GUI applications may ignore.

The honest position is that graceful termination is unreliable on Windows for processes that are not console applications, and the module reports which of the two paths it took so the caller can say so. Pretending the escalation is equivalent would mean a worktree teardown that kills a desktop process without giving it a chance to release its lease, which is the situation B7.1 was written about.

### 4.4 Filesystem guarantees are a property of the mount

M2 §8 and §9 rest on exclusive creation and rename being atomic. Both hold on APFS, ext4 and NTFS, and both have now been [measured](experiments/virtiofs-guarantees.md) as holding over a Docker Desktop virtiofs bind mount, which is the configuration overview §6 requires.

Three mounts remain unmeasured and are reachable in ordinary use: Colima and Lima, WSL2 bind mounts, and a `$HOME` on a network filesystem. The last of those is where the guarantees genuinely do not hold.

This module owns the probe that answers it for an unfamiliar mount. Where a guarantee does not hold, M2's generation counter still prevents a lost update, so the outcome is more retries rather than corruption — but the caller should be told which regime it is in.

The same measurement found that neither `flock` nor POSIX record locks propagate between the macOS host and a container, while both work correctly between two containers sharing one host mount. The boundary is host-to-VM, not container-to-container, so a locking scheme validated in an all-container test will fail as soon as the host participates.

### 4.5 Path length and shape on Windows

Long paths beyond 260 characters need the opt-in, and `.claude/worktrees/<slug>` nested inside an already-deep repository path reaches it more easily than it looks. M1 §4.1's slug cap helps and does not solve it.

UNC paths and mapped drives both appear as worktree locations, and they compare unequal as strings while naming the same directory — the same class of problem as §4.2, with a different cause.

### 4.6 Permissions do not translate

M2 §2 creates the state directory `0700` and its files `0600`, because B10.1 puts seed credentials in the registry. Mode bits are meaningless on NTFS.

The Windows path sets an ACL granting the current user and denying others, and where that cannot be done the module says so rather than silently writing world-readable credentials. Overview §6.5's ownership problem — a container writing as uid 0 — is the unix half of the same concern.

## 5. Capability probes

Overview §6.4 requires that operations reaching outside the filesystem detect that they cannot see what they are meant to see, rather than succeeding emptily. The probes live here because each is platform-specific:

| Question | How |
|---|---|
| Is a docker socket reachable | attempt a version call on the configured endpoint |
| Am I in the host network namespace | compare the namespace identity against the host's where the platform exposes it |
| Am I in the host pid namespace | same |
| Is `gh` present and authenticated | invoke it |
| Is the VM driver installed | invoke it |

Each returns available, unavailable-with-a-reason, or unknown. Callers treat unknown as unavailable, because M4 §8's exit code 4 is a better outcome than a reap that silently finds nothing.

Detection is by capability rather than by sniffing for `/.dockerenv`. Capability is what every decision actually turns on, and it is directly observable.

## 6. Prerequisites

B15.3: missing platform tooling produces a clear error naming the install command, not a stack trace.

| Missing | Message names |
|---|---|
| `lsof` / `netstat` | the package, and that reaping is degraded until it is present |
| Colima | `brew install colima` |
| WSL2, or a base tarball not yet built | the enable command, and the one-time tarball build |
| `gh` | the install and `gh auth login` |
| `git` | the install |
| docker socket | the mount, per overview §6.4 |

The one-time prerequisites are the ones worth surfacing early. B15.2 names the WSL base tarball specifically because it is a build step that has to happen before anything else works, and discovering it partway through a first `init` is a bad first experience.

## 7. Support matrix

| Platform | Position |
|---|---|
| macOS | primary — the source repos' main development platform, and the one where virtiofs makes M2's guarantees uncertain |
| Linux | primary — every container, and the simplest filesystem semantics |
| Windows | supported, with the reaper's graceful step and the permission model both documented as weaker |

Container residency is a dimension of each rather than a fourth platform. A container is Linux, with reduced capabilities that §5's probes report.

## 8. Failure modes

| Situation | Behaviour |
|---|---|
| Discovery tool absent | report, degrade the reaper, complete the teardown (B7.1) |
| VM driver absent | refuse the operation, name the install |
| Scheduler unavailable | `schedule install` fails clearly; nothing else is affected |
| Filesystem guarantee probe fails | proceed on the generation counter, state the regime |
| ACL cannot be set on the state directory | refuse to write credentials there, name the path |
| Case-sensitivity of the mount cannot be determined | assume case-insensitive, which is the conservative direction for §4.2 |

## 9. Open questions

- Does Linux need a `machine` driver at all, or is a native dockerd plus per-project namespacing sufficient? Mini-infra's VM exists because the app manages the host daemon, which is a trait, not a platform.
- Where does the view id live on Windows, and does a read-only container root filesystem have any answer anywhere? Carried from M1 §5.
- Is Windows support worth its cost given the reaper and permission caveats, or is it better scoped to "works, with these two things documented as weaker"? The support matrix currently claims the second.
- Should the filesystem guarantee probe run per mount and be cached in the registry, or per invocation? Caching it in shared state means a container's answer could be read by the host, where it is wrong.
