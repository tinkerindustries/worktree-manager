#!/usr/bin/env python3
"""Probe the guarantees M2 8 and 9 rest on, across a virtiofs bind mount.

Roles run on the host and inside a container against the same directory.
Timestamps travel in filenames, never in file contents, so the clock sync
phase cannot be confounded by the content tearing that phase 3 measures.
"""
import argparse, json, os, sys, time, fcntl, errno

def now():
    return time.time()

def spin_until(t):
    while True:
        d = t - time.time()
        if d <= 0:
            return
        if d > 0.002:
            time.sleep(d - 0.002)

def scan(d, prefix):
    try:
        for n in os.listdir(d):
            if n.startswith(prefix):
                return n
    except FileNotFoundError:
        pass
    return None

def touch(path):
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o644)
    os.close(fd)

# ---------------------------------------------------------------- phase 1
# Filesystem NTP. Host pings, container pongs, host reads the reply. The
# exchange with the smallest round trip gives the tightest offset estimate.

def sync_host(d, n):
    os.makedirs(d, exist_ok=True)
    best = None
    lat = []
    for k in range(n):
        t1 = now()
        touch(os.path.join(d, f"ping-{k}-{t1!r}"))
        name = None
        deadline = t1 + 10
        while name is None and time.time() < deadline:
            name = scan(d, f"pong-{k}-")
        if name is None:
            return {"error": f"no pong for exchange {k}"}
        t2 = now()
        tc = float(name.rsplit("-", 1)[1])
        rtt = t2 - t1
        lat.append(rtt)
        off = tc - (t1 + t2) / 2
        if best is None or rtt < best["rtt"]:
            best = {"rtt": rtt, "offset": off, "k": k}
    lat.sort()
    return {"offset": best["offset"], "min_rtt": best["rtt"],
            "median_rtt": lat[len(lat) // 2], "max_rtt": lat[-1], "n": n}

def sync_container(d, n):
    for k in range(n):
        name = None
        deadline = time.time() + 15
        while name is None and time.time() < deadline:
            name = scan(d, f"ping-{k}-")
        if name is None:
            return {"error": f"no ping for exchange {k}"}
        tc = now()
        touch(os.path.join(d, f"pong-{k}-{tc!r}"))
    return {"ok": True}

# ---------------------------------------------------------------- phase 2
# O_CREAT|O_EXCL contention. Every worker on both sides attempts the same
# index at the same wall-clock instant. Two winners for one index means
# exclusive creation is not atomic across the boundary.

def excl(d, rounds, start, interval, offset, worker, role):
    os.makedirs(d, exist_ok=True)
    wins = []
    errs = {}
    for i in range(rounds):
        spin_until(start + offset + i * interval)
        try:
            fd = os.open(os.path.join(d, f"lock-{i}"),
                         os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o644)
            os.close(fd)
            wins.append(i)
        except FileExistsError:
            pass
        except OSError as e:
            errs[errno.errorcode.get(e.errno, str(e.errno))] = \
                errs.get(errno.errorcode.get(e.errno, str(e.errno)), 0) + 1
    return {"role": role, "worker": worker, "wins": wins, "errors": errs}

# ---------------------------------------------------------------- phase 3
# Atomic replace. The writer publishes whole generations through a temp file
# in the same directory; the reader must never observe a mixed or short one.

CHUNK = 64
BLOCKS = 4096          # 256 KiB payload

def rename_write(path, seconds):
    tmp = path + ".tmp"
    g = 0
    end = time.time() + seconds
    while time.time() < end:
        g += 1
        body = (f"{g:016d}".encode() * (CHUNK // 16)) * BLOCKS
        with open(tmp, "wb") as f:
            f.write(body)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    return {"generations": g}

def rename_read(path, seconds):
    total = torn = short = missing = other = 0
    seen = set()
    end = time.time() + seconds
    expect = CHUNK * BLOCKS
    while time.time() < end:
        try:
            with open(path, "rb") as f:
                data = f.read()
        except FileNotFoundError:
            missing += 1
            continue
        except OSError:
            other += 1
            continue
        total += 1
        if not data:
            continue
        if len(data) != expect:
            short += 1
            continue
        head = data[:16]
        if data != head * (expect // 16):
            torn += 1
        else:
            seen.add(int(head))
    return {"reads": total, "torn": torn, "short": short,
            "missing": missing, "errors": other, "distinct_generations": len(seen)}

# ---------------------------------------------------------------- phase 4
# Advisory locking. If the far side acquires while this side holds, flock
# does not propagate across the boundary.

def flock_hold(path, seconds):
    f = open(path, "a+")
    fcntl.flock(f.fileno(), fcntl.LOCK_EX)
    time.sleep(seconds)
    fcntl.flock(f.fileno(), fcntl.LOCK_UN)
    f.close()
    return {"held": seconds}

def flock_try(path, delay):
    time.sleep(delay)
    f = open(path, "a+")
    try:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.flock(f.fileno(), fcntl.LOCK_UN)
        return {"acquired": True}
    except OSError as e:
        return {"acquired": False, "errno": errno.errorcode.get(e.errno, e.errno)}
    finally:
        f.close()

# POSIX record locks are a separate mechanism from flock and can propagate
# differently, so they are measured separately.

def posix_hold(path, seconds):
    f = open(path, "a+")
    fcntl.lockf(f.fileno(), fcntl.LOCK_EX)
    time.sleep(seconds)
    fcntl.lockf(f.fileno(), fcntl.LOCK_UN)
    f.close()
    return {"held": seconds}

def posix_try(path, delay):
    time.sleep(delay)
    f = open(path, "a+")
    try:
        fcntl.lockf(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.lockf(f.fileno(), fcntl.LOCK_UN)
        return {"acquired": True}
    except OSError as e:
        return {"acquired": False, "errno": errno.errorcode.get(e.errno, e.errno)}
    finally:
        f.close()

# ---------------------------------------------------------------- phase 6
# The failure this design actually fears: the container probes a name before
# it exists, caches the negative result, the host then creates it, and the
# container's exclusive create succeeds off the stale cache. Each round makes
# the container look first and attempt only after the host has committed.

def negcache_container(d, rounds):
    os.makedirs(d, exist_ok=True)
    false_wins = []
    correct = 0
    for i in range(rounds):
        lock = os.path.join(d, f"n-{i}")
        os.path.exists(lock)                    # poison: negative lookup
        touch(os.path.join(d, f"ready-{i}"))
        deadline = time.time() + 10
        while not os.path.exists(os.path.join(d, f"done-{i}")):
            if time.time() > deadline:
                return {"error": f"host never signalled round {i}"}
        try:
            fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o644)
            os.close(fd)
            false_wins.append(i)                # created a file the host already made
        except FileExistsError:
            correct += 1
    return {"rounds": rounds, "correct_EEXIST": correct,
            "false_wins": len(false_wins), "sample": false_wins[:5]}

def negcache_host(d, rounds):
    os.makedirs(d, exist_ok=True)
    for i in range(rounds):
        deadline = time.time() + 10
        while not os.path.exists(os.path.join(d, f"ready-{i}")):
            if time.time() > deadline:
                return {"error": f"container never readied round {i}"}
        touch(os.path.join(d, f"n-{i}"))
        touch(os.path.join(d, f"done-{i}"))
    return {"rounds": rounds}

# ----------------------------------------------------------------------

def main():
    p = argparse.ArgumentParser()
    p.add_argument("cmd")
    p.add_argument("--dir", default="/mnt")
    p.add_argument("--path")
    p.add_argument("--n", type=int, default=50)
    p.add_argument("--rounds", type=int, default=1500)
    p.add_argument("--start", type=float, default=0.0)
    p.add_argument("--interval", type=float, default=0.004)
    p.add_argument("--offset", type=float, default=0.0)
    p.add_argument("--worker", default="0")
    p.add_argument("--role", default="?")
    p.add_argument("--seconds", type=float, default=5.0)
    p.add_argument("--delay", type=float, default=1.0)
    a = p.parse_args()

    if a.cmd == "sync-host":
        r = sync_host(a.dir, a.n)
    elif a.cmd == "sync-container":
        r = sync_container(a.dir, a.n)
    elif a.cmd == "excl":
        r = excl(a.dir, a.rounds, a.start, a.interval, a.offset, a.worker, a.role)
    elif a.cmd == "rename-write":
        r = rename_write(a.path, a.seconds)
    elif a.cmd == "rename-read":
        r = rename_read(a.path, a.seconds)
    elif a.cmd == "flock-hold":
        r = flock_hold(a.path, a.seconds)
    elif a.cmd == "flock-try":
        r = flock_try(a.path, a.delay)
    elif a.cmd == "posix-hold":
        r = posix_hold(a.path, a.seconds)
    elif a.cmd == "posix-try":
        r = posix_try(a.path, a.delay)
    elif a.cmd == "negcache-host":
        r = negcache_host(a.dir, a.rounds)
    elif a.cmd == "negcache-container":
        r = negcache_container(a.dir, a.rounds)
    else:
        print("unknown command", file=sys.stderr)
        return 2
    print(json.dumps(r))
    return 0

sys.exit(main())
