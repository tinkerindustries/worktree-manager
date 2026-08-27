package platform

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestListenersFindsARealListener is the discovery contract against the
// real platform: lsof (or netstat+tasklist) finds the LISTEN holder of a
// port this process binds, and a port nothing holds comes back empty.
func TestListenersFindsARealListener(t *testing.T) {
	// Inside a container the discovery still sees the container's own
	// namespace, and this process's listener is in it — so the test is
	// meaningful there too. The platform's tool must exist either way.
	requireDiscoveryTool(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a listener: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	// The listener may take a moment to appear to lsof; poll briefly.
	var holders []Holder
	for i := 0; i < 10; i++ {
		holders, err = Listeners([]int{port})
		if err != nil {
			t.Fatalf("Listeners: %v", err)
		}
		if len(holders) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(holders) == 0 {
		t.Fatalf("Listeners(%d) found nothing, want the test process's own listener", port)
	}
	if holders[0].PID <= 0 {
		t.Errorf("holder pid = %d, want a real pid", holders[0].PID)
	}
	if holders[0].Port != port {
		t.Errorf("holder port = %d, want %d", holders[0].Port, port)
	}

	// A port nothing holds is empty — and that emptiness is a real find of
	// nothing, not an error.
	free := freePlatformPort(t)
	holders, err = Listeners([]int{free})
	if err != nil {
		t.Fatalf("Listeners(%d): %v", free, err)
	}
	if len(holders) != 0 {
		t.Errorf("Listeners(%d) = %+v, want no holders", free, holders)
	}
}

// TestListenersMultiplePortsFindsEachHolder: one discovery call covers
// several ports and reports each holder with its own port.
func TestListenersMultiplePortsFindsEachHolder(t *testing.T) {
	requireDiscoveryTool(t)
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	defer l1.Close()
	l2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	defer l2.Close()
	ports := []int{l1.Addr().(*net.TCPAddr).Port, l2.Addr().(*net.TCPAddr).Port, freePlatformPort(t)}

	var holders []Holder
	for i := 0; i < 10; i++ {
		holders, err = Listeners(ports)
		if err != nil {
			t.Fatalf("Listeners: %v", err)
		}
		if len(holders) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	seen := map[int]bool{}
	for _, h := range holders {
		seen[h.Port] = true
	}
	if !seen[ports[0]] || !seen[ports[1]] {
		t.Errorf("Listeners(%v) = %+v, want holders on %d and %d", ports, holders, ports[0], ports[1])
	}
}

// TestSignalTermAndKill: signalling by pid works against a real process —
// SIGTERM first, and the escalation SIGKILL against a process that ignores
// TERM. This is the B7.1 sequence's platform half; the coordinator owns the
// sequencing.
func TestSignalTermAndKill(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; while true; do sleep 1; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the victim: %v", err)
	}
	// Reap in the background: a dead-but-unreaped process is a zombie, and
	// the null signal still "sees" a zombie, which would make the
	// post-KILL liveness check read the victim as alive.
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	defer func() {
		SignalKill(cmd.Process.Pid)
		<-done
	}()
	if !Alive(cmd.Process.Pid) {
		t.Fatal("victim died before the test signalled it")
	}
	// Let the shell install its TERM trap before the test signals it — a
	// TERM arriving during startup would kill the shell before the trap
	// exists, which is not what this test is about.
	time.Sleep(300 * time.Millisecond)
	// The TERM step. On unix the victim traps the signal and survives it.
	// On Windows there is no signal to trap: SignalTerm is taskkill
	// without /F, which asks a windowed process to close, and a console
	// process has no window — taskkill refuses, and the refusal is
	// reported rather than swallowed (08-platform.md: TERM then /F, the
	// escalation reported). Both platforms make the same statement here,
	// which is the one the escalation below depends on: TERM did not end
	// the victim.
	if err := SignalTerm(cmd.Process.Pid); err != nil && runtime.GOOS != "windows" {
		t.Fatalf("SignalTerm: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if !Alive(cmd.Process.Pid) {
		t.Fatal("victim died from the TERM step, which it must survive")
	}
	if err := SignalKill(cmd.Process.Pid); err != nil {
		t.Fatalf("SignalKill: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for Alive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(cmd.Process.Pid) {
		t.Error("victim survived SIGKILL")
	}
}

// TestInContainer reports the environment fact the reap decision reads. It
// asserts the function returns a bool (the shape), not a value.
func TestInContainer(t *testing.T) {
	_ = InContainer() // shape check; the value is an environment fact
}

// TestKillGroupKillsTheWholeTree: the hook runner's timeout path — killing
// the process group takes the shell and its children together, so a timed
// out hook cannot orphan a build or a compose up.
func TestKillGroupKillsTheWholeTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows limitation, stated rather than worked around: the
		// assertion is "no descendant of the killed shell survives", and
		// checking it needs a parent-aware process listing —
		// `ps -eo pid=,ppid=,comm=`. Windows has no such tool that can be
		// relied on (tasklist reports no parent, and wmic is gone from
		// current builds). KillGroup itself is taskkill /F /T there, whose
		// tree behaviour is the operating system's rather than this
		// package's.
		t.Skip("windows has no parent-aware process listing to verify a tree kill with")
	}
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	StartInOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	// The child sleep must be in the same group and die with it. Find it by
	// walking /proc for a sleep whose parent is our shell.
	if err := KillGroup(cmd); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}
	cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		left := countSleepChildren(t, cmd.Process.Pid)
		if left == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("a child of the killed shell survived the group kill")
}

// countSleepChildren counts the descendants of pid whose command is sleep.
func countSleepChildren(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,comm=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		var p, pp int
		if _, err := fmt.Sscanf(fields[0], "%d", &p); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(fields[1], "%d", &pp); err != nil {
			continue
		}
		children[pp] = append(children[pp], p)
	}
	count := 0
	var walk func(int)
	walk = func(p int) {
		for _, c := range children[p] {
			walk(c)
			count++
		}
	}
	walk(pid)
	return count
}

// freePlatformPort finds a port nothing is bound to right now.
func freePlatformPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// discoveryTool names the platform's own listener-discovery binary — lsof
// on macOS and Linux, netstat on Windows (paired with tasklist). Guarding
// on lsof everywhere skipped the whole discovery contract on Windows,
// which is the one platform whose implementation is not lsof.
func discoveryTool() string {
	if runtime.GOOS == "windows" {
		return "netstat"
	}
	return "lsof"
}

// requireDiscoveryTool skips when the platform's discovery binary is
// absent, naming it.
func requireDiscoveryTool(t *testing.T) {
	t.Helper()
	tool := discoveryTool()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s is not installed; the discovery test needs the platform tool", tool)
	}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("tasklist"); err != nil {
			t.Skip("tasklist is not installed; the discovery test needs it to name the holder")
		}
	}
}
