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

// listenersLsof discovers LISTEN holders with lsof: `lsof -nP
// -iTCP:<port> -sTCP:LISTEN -F pc` prints one `p<pid>` record and one
// `c<command>` record per process.
//
// lsof exits 1 when nothing matches — that is the empty-holder case, not
// an error. Any other failure (the binary is absent, the invocation
// failed) is unavailable, never a silent find of nothing.
func listenersLsof(ports []int) ([]Holder, error) {
	var holders []Holder
	for _, port := range ports {
		lsof, err := exec.LookPath("lsof")
		if err != nil {
			return nil, unavailableError("lsof", "it is not installed or not on PATH; install lsof (macOS ships it, Linux: apt-get install lsof), then re-run")
		}
		out, err := exec.Command(lsof, "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-F", "pc").Output()
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

// errOutput recovers the command's stderr from an ExitError, for reporting.
func errOutput(err error) []byte {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.Stderr
	}
	return nil
}
