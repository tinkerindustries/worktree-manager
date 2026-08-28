//go:build windows

package platform

import (
	"os/exec"
	"strconv"
	"strings"
)

// listeners discovers LISTEN holders with netstat -ano plus tasklist:
// netstat lists the owning pid per LISTENING socket, and tasklist maps the
// pid to an image name (08-platform.md §3). The graceful-step weakness of
// Windows signalling is documented in 08-platform.md §3; this half of the
// package is written and cross-compiled, and proved by whatever a Windows
// runner can prove (PLAN-SCOPE.md, "Verification that needs hardware this
// machine does not have").
// listenerHelpers names what discovery shells out to here: netstat for
// the sockets and tasklist for the owning image names. Both ship with
// Windows. lsof does not exist on this platform and is never reached for.
func listenerHelpers() []string {
	return []string{"netstat", "tasklist"}
}

func listeners(ports []int) ([]Holder, error) {
	netstat, err := exec.LookPath("netstat")
	if err != nil {
		return nil, unavailableError("netstat", "it is not on PATH (netstat ships with Windows); install it, then re-run")
	}
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	out, err := exec.Command(netstat, "-ano").Output()
	if err != nil {
		return nil, unavailableError("netstat", "it failed: "+strings.TrimSpace(string(errOutput(err))))
	}
	// pidByPort: the owning pid per wanted port. netstat -ano lines look
	// like `  TCP    0.0.0.0:4200    0.0.0.0:0    LISTENING    1234`.
	var pidByPort = map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "TCP" || fields[3] != "LISTENING" {
			continue
		}
		addr := fields[1]
		port, perr := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
		if perr != nil || !want[port] {
			continue
		}
		pid, ierr := strconv.Atoi(fields[4])
		if ierr != nil {
			continue
		}
		pidByPort[port] = pid
	}
	if len(pidByPort) == 0 {
		return nil, nil
	}

	tasklist, err := exec.LookPath("tasklist")
	if err != nil {
		return nil, unavailableError("tasklist", "it is not on PATH (tasklist ships with Windows); install it, then re-run")
	}
	var holders []Holder
	for port, pid := range pidByPort {
		cmd := exec.Command(tasklist, "/FO", "CSV", "/NH", "/FI", "PID eq "+strconv.Itoa(pid))
		tout, terr := cmd.Output()
		if terr != nil {
			// The pid may have exited between netstat and tasklist; the
			// holder is gone, which is the outcome the reap wanted.
			continue
		}
		name := parseTasklistName(string(tout))
		holders = append(holders, Holder{PID: pid, Command: name, Port: port})
	}
	return holders, nil
}

// allListeners discovers every LISTENING TCP socket with one netstat run
// (no port filter), then resolves the distinct pids against tasklist.
func allListeners() ([]Holder, error) {
	netstat, err := exec.LookPath("netstat")
	if err != nil {
		return nil, unavailableError("netstat", "it is not on PATH (netstat ships with Windows); install it, then re-run")
	}
	out, err := exec.Command(netstat, "-ano").Output()
	if err != nil {
		return nil, unavailableError("netstat", "it failed: "+strings.TrimSpace(string(errOutput(err))))
	}
	// portsByPid: every LISTENING port, keyed by owning pid.
	portsByPid := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "TCP" || fields[3] != "LISTENING" {
			continue
		}
		addr := fields[1]
		port, perr := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
		if perr != nil {
			continue
		}
		pid, ierr := strconv.Atoi(fields[4])
		if ierr != nil {
			continue
		}
		portsByPid[pid] = append(portsByPid[pid], port)
	}
	if len(portsByPid) == 0 {
		return nil, nil
	}

	tasklist, err := exec.LookPath("tasklist")
	if err != nil {
		return nil, unavailableError("tasklist", "it is not on PATH (tasklist ships with Windows); install it, then re-run")
	}
	var holders []Holder
	for pid, ports := range portsByPid {
		cmd := exec.Command(tasklist, "/FO", "CSV", "/NH", "/FI", "PID eq "+strconv.Itoa(pid))
		tout, terr := cmd.Output()
		if terr != nil {
			continue // the pid exited between the two runs
		}
		name := parseTasklistName(string(tout))
		for _, port := range ports {
			holders = append(holders, Holder{PID: pid, Command: name, Port: port})
		}
	}
	sortHolders(holders)
	return holders, nil
}

// parseTasklistName extracts the image name from one tasklist CSV line:
// `"server.exe","1234","Console","1","5,000 K"` — the first quoted field.
func parseTasklistName(out string) string {
	line := out
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if fields := parseTasklistFields(line); len(fields) > 0 {
		return fields[0]
	}
	return "(unknown)"
}

// signalTerm posts the close message without /F — the graceful step that
// console applications do not receive and GUI applications may ignore
// (08-platform.md §3). The coordinator reports which path it took.
func signalTerm(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
}

// signalKill force-terminates with /F — the escalation after the wait.
func signalKill(pid int) error {
	return exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// alive reports whether the pid still has a tasklist entry.
//
// The answer is in the output, never in the exit code: tasklist exits 0
// for a filter that matches nothing and prints "INFO: No tasks are running
// which match the specified criteria" instead. Reading the exit code alone
// made alive() answer true for every pid, including one that had just been
// force-terminated — which is exactly the reading the reaper's escalation
// must not get wrong.
func alive(pid int) bool {
	want := strconv.Itoa(pid)
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH", "/FI", "PID eq "+want).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if fields := parseTasklistFields(line); len(fields) > 1 && fields[1] == want {
			return true
		}
	}
	return false
}

// parseTasklistFields splits one tasklist CSV line into its quoted fields:
// `"server.exe","1234","Console","1","5,000 K"`. The memory column carries
// a comma inside its quotes, so the split follows the quoting rather than
// the commas.
func parseTasklistFields(line string) []string {
	var fields []string
	rest := strings.TrimSpace(line)
	for strings.HasPrefix(rest, `"`) {
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			break
		}
		fields = append(fields, rest[1:1+end])
		rest = strings.TrimPrefix(rest[1+end+1:], ",")
	}
	return fields
}
