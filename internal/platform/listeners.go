// Package platform owns the reaper's platform half (M8a, 08-platform.md
// §3): listener discovery and process signalling. Discovery is `lsof` on
// unix and `netstat -ano` plus `tasklist` on Windows, finding the `LISTEN`
// holders of a set of ports (04-lifecycle.md §6). Signalling is by pid —
// never `pkill -f <appname>`, which matches every instance on the machine
// including the developer's own (B7.2). The escalate-and-wait sequencing
// lives in the coordinator, which decides who may be signalled at all;
// this package only reports facts and delivers signals.
//
// Two platform facts are load-bearing here and belong to this package
// alone:
//
//   - A discovery tool that is absent is unavailable, not a successful
//     find of nothing. The caller reports unavailable with the remedy
//     naming the install, and completes the teardown (B7.1, 08-platform.md
//     §3 "Discovery tool absent").
//   - In a container, discovery sees the container's pid and network
//     namespaces, so it finds nothing — reported as unavailable with the
//     remedy named (run on the host), never as a successful reap of zero
//     processes (04-lifecycle.md §6).
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Holder is one process listening on one port: its pid, the command name
// discovery reported, and the port. The coordinator classifies holders —
// only the spec's own binaries are ever signalled — so this package never
// sees an allowlist.
type Holder struct {
	PID     int
	Command string
	Port    int
}

// Listeners discovers the processes with a LISTEN socket on any of ports,
// in the caller's network namespace. The unix implementation shells out to
// lsof; the Windows implementation runs netstat -ano and resolves pids
// against tasklist.
//
// The error contract is the availability contract: a missing discovery
// tool, or a discovery run that cannot complete, is an error naming the
// tool and the install command — never an empty holder list, which would
// read as a successful reap of zero processes.
func Listeners(ports []int) ([]Holder, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	return listeners(ports)
}

// AllListeners discovers every process with a LISTEN TCP socket in the
// caller's network namespace — the fact `wt ports scan` reports, sorted by
// port so the report is stable (08-platform.md §3). It never classifies
// what it finds: a listener is a fact, and whether it belongs to a
// production stack is a person's judgement (plan.md §8, R6).
//
// The error contract is the same availability contract as Listeners: a
// missing discovery tool, or a run that cannot complete, is an error
// naming the tool and the install command — never an empty report, which
// would read as a successful scan of nothing.
func AllListeners() ([]Holder, error) {
	return allListeners()
}

// ListenerHelpers names the external binaries listener discovery shells
// out to on this machine, in the order it uses them. It is the same
// question the machine runner's Binary answers for the VM seam: which
// helper does this platform actually need, asked of the platform rather
// than assumed by the caller.
//
// The answer differs by platform and, on Linux, by machine: discovery
// prefers /proc/net/tcp where it exists and needs no helper at all, falls
// back to lsof where it does not, and runs netstat plus tasklist on
// Windows. An empty result means discovery needs nothing external. The
// coordinator's doctor asks this instead of naming a tool, so it cannot
// report a helper the machine never runs.
func ListenerHelpers() []string { return listenerHelpers() }

// sortHolders orders a scan report: by port (unknown ports last), then
// pid, then command — a stable report for `wt ports scan`.
func sortHolders(holders []Holder) {
	sort.Slice(holders, func(i, j int) bool {
		a, b := holders[i], holders[j]
		pa, pb := a.Port, b.Port
		if pa == 0 {
			pa = 1 << 30
		}
		if pb == 0 {
			pb = 1 << 30
		}
		if pa != pb {
			return pa < pb
		}
		if a.PID != b.PID {
			return a.PID < b.PID
		}
		return a.Command < b.Command
	})
}

// SignalTerm delivers the graceful signal: SIGTERM on unix, taskkill
// without /F on Windows (08-platform.md §3: Windows has no reliable
// equivalent of the graceful step; the weakness is documented, and the
// coordinator's report says which path it took).
func SignalTerm(pid int) error { return signalTerm(pid) }

// SignalKill delivers the escalation: SIGKILL on unix, taskkill /F on
// Windows.
func SignalKill(pid int) error { return signalKill(pid) }

// Alive reports whether a process with pid exists right now. The
// coordinator uses it after the three-second TERM wait to decide whether
// the KILL escalation is still needed.
func Alive(pid int) bool { return alive(pid) }

// InContainer reports whether this process runs inside a container. The
// coordinator uses it to refuse a reap whose discovery would see only the
// container's namespaces — reported as unavailable with the remedy named,
// never as a successful reap of zero processes (04-lifecycle.md §6).
func InContainer() bool {
	// The conventional markers: a .dockerenv file at the root (docker's
	// own marker), or a cgroup that names a container runtime. Both are
	// facts about the machine this process runs on, not guesses.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "docker") || strings.Contains(lower, "kubepods") ||
				strings.Contains(lower, "containerd") || strings.Contains(lower, "lxc") {
				return true
			}
		}
	}
	return false
}

// renderCommand shortens a command line to a command name for reporting:
// the part before the first space, else the whole line. The coordinator
// matches the spec's binaries against the base name, so the shortening
// must not strip a base name that is itself the whole command.
func renderCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "(unknown)"
	}
	if i := strings.IndexByte(cmd, ' '); i > 0 {
		return cmd[:i]
	}
	return cmd
}

// parsePids parses lsof's -F output: pairs of `p<pid>` and `c<command>`
// records. A command may span... it cannot: lsof -F c emits one line per
// record, and a command name has no newline.
func parsePids(port int, out string) []Holder {
	var holders []Holder
	cur := Holder{Port: port}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			if cur.PID != 0 {
				holders = append(holders, cur)
			}
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
			if err != nil {
				cur = Holder{Port: port}
				continue
			}
			cur = Holder{PID: pid, Port: port}
		case strings.HasPrefix(line, "c"):
			cur.Command = renderCommand(strings.TrimPrefix(line, "c"))
		}
	}
	if cur.PID != 0 {
		holders = append(holders, cur)
	}
	return holders
}

// unavailableError builds the discovery-tool error naming the install
// command, per 08-platform.md §3.
func unavailableError(tool, detail string) error {
	return fmt.Errorf("listener discovery needs %s, which is unavailable: %s", tool, detail)
}

// errOutput recovers the command's stderr from an ExitError, for reporting.
func errOutput(err error) []byte {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.Stderr
	}
	return nil
}
