//go:build darwin || linux

package platform

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// listeners discovers LISTEN holders with lsof: `lsof -nP -iTCP:<port>
// -sTCP:LISTEN -F pc` prints one `p<pid>` record and one `c<command>`
// record per process. lsof is the discovery tool of record for unix
// (08-platform.md §3); a machine without it gets the unavailable error
// naming the install.
//
// lsof exits 1 when nothing matches — that is the empty-holder case, not
// an error. Any other failure (the binary is absent, the invocation
// failed) is unavailable, never a silent find of nothing.
func listeners(ports []int) ([]Holder, error) {
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

// errOutput recovers the command's stderr from an ExitError, for reporting.
func errOutput(err error) []byte {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.Stderr
	}
	return nil
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
