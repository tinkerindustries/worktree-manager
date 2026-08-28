//go:build darwin || linux

package platform

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// listeners discovers LISTEN holders on unix. Two implementations, chosen
// by what the machine offers (08-platform.md §3: "lsof | lsof or
// /proc/net"): on Linux the /proc filesystem — /proc/net/tcp for the
// LISTEN sockets, /proc/<pid>/fd for the owner, /proc/<pid>/comm for the
// command — needs no external tool and works inside containers. Where
// /proc is not available (macOS) the platform's lsof is used with its -F
// protocol. A machine whose lsof does not honour the protocol (busybox's
// applet, which ignores every flag) is exactly why /proc is preferred
// where it exists.
func listeners(ports []int) ([]Holder, error) {
	if _, err := os.Stat("/proc/net/tcp"); err == nil {
		return listenersProc(ports)
	}
	return listenersLsof(ports)
}

// allListeners discovers every LISTEN TCP socket, with the same two
// implementations as listeners: /proc where it exists, lsof otherwise.
func allListeners() ([]Holder, error) {
	if _, err := os.Stat("/proc/net/tcp"); err == nil {
		return allListenersProc()
	}
	return allListenersLsof()
}

// allListenersLsof discovers every LISTEN holder with one lsof run:
// `lsof -nP -iTCP -sTCP:LISTEN -F pcn` prints, per open socket, a `p<pid>`
// record, a `c<command>` record and an `n<name>` record carrying the
// socket name (`TCP *:4200 (LISTEN)`), from which the port is read. lsof
// exits 1 when nothing listens at all — the empty-report case, not an
// error; any other failure is unavailable, never a silent find of nothing.
//
// The records are grouped by pid, so a process with several listening
// sockets yields one holder per (pid, port) with the pid and command
// repeated per file, as lsof -F prints them.
func allListenersLsof() ([]Holder, error) {
	cmd, err := HelperCommand("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn")
	if err != nil {
		return nil, unavailableError("lsof", lsofMissing(err))
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // nothing matches: no TCP listener at all
		}
		return nil, unavailableError("lsof", "it failed: "+strings.TrimSpace(string(errOutput(err))))
	}
	type proc struct {
		command string
		ports   map[int]bool
	}
	procs := map[int]*proc{}
	cur := -1
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			if pid, perr := strconv.Atoi(strings.TrimPrefix(line, "p")); perr == nil {
				cur = pid
				if procs[pid] == nil {
					procs[pid] = &proc{ports: map[int]bool{}}
				}
			}
		case strings.HasPrefix(line, "c"):
			if cur >= 0 {
				procs[cur].command = renderCommand(strings.TrimPrefix(line, "c"))
			}
		case strings.HasPrefix(line, "n"):
			if cur >= 0 {
				if port, ok := socketNamePort(strings.TrimPrefix(line, "n")); ok {
					procs[cur].ports[port] = true
				}
			}
		}
	}
	var holders []Holder
	for pid, p := range procs {
		if len(p.ports) == 0 {
			// The socket name did not parse (an unusual lsof dialect):
			// the process is reported once with the port unknown, and the
			// bounded coverage is the coordinator's note to write.
			holders = append(holders, Holder{PID: pid, Command: p.command, Port: 0})
			continue
		}
		for port := range p.ports {
			holders = append(holders, Holder{PID: pid, Command: p.command, Port: port})
		}
	}
	sortHolders(holders)
	return holders, nil
}

// socketNamePort extracts the port from an lsof socket name:
// `TCP *:4200 (LISTEN)` → 4200. The parenthesised state is stripped, the
// last colon segment is parsed.
func socketNamePort(name string) (int, bool) {
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = strings.TrimSpace(name[:i])
	}
	i := strings.LastIndexByte(name, ':')
	if i < 0 || i == len(name)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(name[i+1:])
	if err != nil {
		return 0, false
	}
	return port, true
}

// allListenersProc reads every LISTEN socket of /proc/net/tcp and tcp6
// (no port filter), resolves each socket inode to a pid through
// /proc/<pid>/fd, and reports one holder per (pid, port).
func allListenersProc() ([]Holder, error) {
	inodePort := map[string]int{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		inodes, err := listeningInodesAll(f)
		if err != nil {
			return nil, unavailableError("/proc/net", "it could not be read: "+err.Error())
		}
		for inode, port := range inodes {
			inodePort[inode] = port
		}
	}
	if len(inodePort) == 0 {
		return nil, nil
	}
	// pid → set of listening socket inodes, from /proc/<pid>/fd.
	pidInodes := map[int]map[string]bool{}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, unavailableError("/proc", "it could not be read: "+err.Error())
	}
	for _, d := range procs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", d.Name(), "fd"))
		if err != nil {
			continue // the process exited mid-scan, or is not ours to read
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", d.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			if inode, ok := strings.CutPrefix(link, "socket:["); ok {
				inode = strings.TrimSuffix(inode, "]")
				if _, wanted := inodePort[inode]; wanted {
					if pidInodes[pid] == nil {
						pidInodes[pid] = map[string]bool{}
					}
					pidInodes[pid][inode] = true
				}
			}
		}
	}
	seen := map[[2]int]bool{}
	var holders []Holder
	for pid, inodes := range pidInodes {
		comm := procComm(pid)
		for inode := range inodes {
			key := [2]int{pid, inodePort[inode]}
			if seen[key] {
				continue // the same pid+port on tcp and tcp6: one holder
			}
			seen[key] = true
			holders = append(holders, Holder{PID: pid, Command: comm, Port: inodePort[inode]})
		}
	}
	sortHolders(holders)
	return holders, nil
}

// listeningInodesAll parses one /proc/net/tcp file: every LISTEN socket,
// mapped from its inode (the last column) to the port (the hex local
// address). No port filter — this is the `ports scan` half of discovery.
func listeningInodesAll(path string) (map[string]int, error) {
	out := map[string]int{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // tcp6 may be absent; that is fine
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			continue // the header
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[3] != "0A" { // TCP_LISTEN
			continue
		}
		addr := fields[1]
		portHex := addr[strings.LastIndex(addr, ":")+1:]
		port, err := strconv.ParseInt(portHex, 16, 32)
		if err != nil {
			continue
		}
		out[fields[9]] = int(port)
	}
	return out, sc.Err()
}

// listenersLsof discovers LISTEN holders with lsof: `lsof -nP
// -iTCP:<port> -sTCP:LISTEN -F pc` prints one `p<pid>` record and one
// `c<command>` record per process.
//
// lsof exits 1 when nothing matches — that is the empty-holder case, not
// an error. Any other failure (the binary is absent, the invocation
// failed) is unavailable, never a silent find of nothing.
// lsofMissing is the one sentence for an unresolvable lsof. Resolution is
// HelperCommand's, so what doctor reports reachable and what the port scan
// can run are the same binary; the detail names the locations searched.
func lsofMissing(err error) string {
	return "it is not installed or not on PATH; install lsof (macOS ships it, Linux: apt-get install lsof), then re-run: " + err.Error()
}

func listenersLsof(ports []int) ([]Holder, error) {
	var holders []Holder
	for _, port := range ports {
		cmd, err := HelperCommand("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-F", "pc")
		if err != nil {
			return nil, unavailableError("lsof", lsofMissing(err))
		}
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				continue // nothing matches: no holder on this port
			}
			return nil, unavailableError("lsof", "it failed: "+strings.TrimSpace(string(errOutput(err))))
		}
		holders = append(holders, parsePids(port, string(out))...)
	}
	return holders, nil
}

// listenersProc reads the LISTEN sockets of /proc/net/tcp and tcp6, maps
// each wanted port to its socket inode, and resolves the inode to a pid
// through /proc/<pid>/fd. The command comes from /proc/<pid>/comm.
func listenersProc(ports []int) ([]Holder, error) {
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	// inode → port for every LISTEN socket on a wanted port.
	inodePort := map[string]int{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		inodes, err := listeningInodes(f, want)
		if err != nil {
			return nil, unavailableError("/proc/net", "it could not be read: "+err.Error())
		}
		for inode, port := range inodes {
			inodePort[inode] = port
		}
	}
	if len(inodePort) == 0 {
		return nil, nil
	}
	// pid → set of listening socket inodes, from /proc/<pid>/fd.
	pidInodes := map[int]map[string]bool{}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, unavailableError("/proc", "it could not be read: "+err.Error())
	}
	for _, d := range procs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", d.Name(), "fd"))
		if err != nil {
			continue // the process exited mid-scan, or is not ours to read
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", d.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			if inode, ok := strings.CutPrefix(link, "socket:["); ok {
				inode = strings.TrimSuffix(inode, "]")
				if _, wanted := inodePort[inode]; wanted {
					if pidInodes[pid] == nil {
						pidInodes[pid] = map[string]bool{}
					}
					pidInodes[pid][inode] = true
				}
			}
		}
	}
	var holders []Holder
	for pid, inodes := range pidInodes {
		comm := procComm(pid)
		for inode := range inodes {
			holders = append(holders, Holder{PID: pid, Command: comm, Port: inodePort[inode]})
		}
	}
	return holders, nil
}

// listeningInodes parses one /proc/net/tcp file: every LISTEN socket on a
// wanted port, mapped from its inode (the last column) to the port (the
// hex local address). Lines look like
// ` 0: 0100007F:9A3C 00000000:0000 0A ... 0 12345 987654 1 1`.
func listeningInodes(path string, want map[int]bool) (map[string]int, error) {
	out := map[string]int{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // tcp6 may be absent; that is fine
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			continue // the header
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[3] != "0A" { // TCP_LISTEN
			continue
		}
		addr := fields[1]
		portHex := addr[strings.LastIndex(addr, ":")+1:]
		port, err := strconv.ParseInt(portHex, 16, 32)
		if err != nil || !want[int(port)] {
			continue
		}
		inode := fields[9]
		out[inode] = int(port)
	}
	return out, sc.Err()
}

// procComm reads a process's command name.
func procComm(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(data))
}

// signalTerm sends SIGTERM — the graceful step of B7.1's SIGTERM, wait,
// SIGKILL sequence.
func signalTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// signalKill sends SIGKILL — the escalation after the three-second wait.
func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// alive reports whether the process exists, via the null signal. ESRCH
// means it is gone (or the pid was reused and is now something else's —
// the three-second window makes that vanishingly unlikely).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
