package platform

// machine_test.go pins the runner seam's pure parsing — the halves of the
// colima and wsl listings that turn bytes into instances — and
// awaitMachineStart, the grace-period wait behind both platforms' Start
// (item 1a). The live shell-outs (colima start, wsl --distribution) cannot
// run on this Linux implementation machine (no colima, no wsl.exe) and are
// reported not_run; the driver's rails are proved against a fake runner in
// internal/driver/machine_test.go. awaitMachineStart itself carries no
// GOOS build tag, so it is exercised here — on every CI platform, gating
// on Linux — with a real subprocess rather than a fake, because the thing
// under test is the interaction between cmd.Wait() and a timer, which a
// fake process cannot reproduce.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseColimaList(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		instances, err := parseColimaList([]byte("[]"))
		if err != nil || len(instances) != 0 {
			t.Fatalf("parseColimaList([]) = %v, %v", instances, err)
		}
	})

	t.Run("no profiles writes nothing", func(t *testing.T) {
		instances, err := parseColimaList([]byte("\n"))
		if err != nil || len(instances) != 0 {
			t.Fatalf("parseColimaList(blank) = %v, %v", instances, err)
		}
	})

	t.Run("one object per line, the shape colima writes", func(t *testing.T) {
		// Verbatim from `colima list --json` on macOS: a JSON stream, one
		// object per line, with no enclosing array. The array-only tests
		// below passed for the whole time the driver could not list a
		// single profile.
		out := `{"name":"arc","status":"Stopped","arch":"aarch64","cpus":4,"memory":8589934592,"disk":64424509440}
{"name":"vm-app-wt-1-1","status":"Running","arch":"aarch64","cpus":2,"memory":4294967296,"disk":64424509440}
`
		instances, err := parseColimaList([]byte(out))
		if err != nil {
			t.Fatalf("parseColimaList: %v", err)
		}
		if len(instances) != 2 {
			t.Fatalf("instances = %v, want 2", instances)
		}
		if instances[0].Name != "arc" || instances[0].Running {
			t.Errorf("arc = %+v, want stopped", instances[0])
		}
		if instances[1].Name != "vm-app-wt-1-1" || !instances[1].Running {
			t.Errorf("vm-app-wt-1-1 = %+v, want running", instances[1])
		}
	})

	t.Run("a single object is a stream of one", func(t *testing.T) {
		instances, err := parseColimaList([]byte(`{"name":"default","status":"Running"}`))
		if err != nil {
			t.Fatalf("parseColimaList: %v", err)
		}
		if len(instances) != 1 || instances[0].Name != "default" || !instances[0].Running {
			t.Fatalf("instances = %+v, want one running default", instances)
		}
	})

	t.Run("running and stopped profiles", func(t *testing.T) {
		out := `[{"name":"default","status":"Running"},{"name":"vm-app-wt-1-1","status":"Running"},{"name":"vm-app-wt-2-2","status":"Stopped"}]`
		instances, err := parseColimaList([]byte(out))
		if err != nil {
			t.Fatalf("parseColimaList: %v", err)
		}
		if len(instances) != 3 {
			t.Fatalf("instances = %v, want 3", instances)
		}
		if instances[0].Name != "default" || !instances[0].Running {
			t.Errorf("default = %+v, want running", instances[0])
		}
		if instances[2].Name != "vm-app-wt-2-2" || instances[2].Running {
			t.Errorf("stopped profile = %+v, want not running", instances[2])
		}
	})

	t.Run("unparseable output is an error", func(t *testing.T) {
		if _, err := parseColimaList([]byte("definitely not json")); err == nil {
			t.Fatal("unparseable output must be an error, never an empty count")
		}
	})
}

func TestParseWSLNames(t *testing.T) {
	t.Run("quiet listing with the marker column", func(t *testing.T) {
		out := "* Ubuntu\n  Debian\n\n  vm-app-wt-1-1\n"
		names := parseWSLNames(out)
		want := []string{"Ubuntu", "Debian", "vm-app-wt-1-1"}
		if len(names) != len(want) {
			t.Fatalf("names = %v, want %v", names, want)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
			}
		}
	})

	t.Run("running listing", func(t *testing.T) {
		names := parseWSLNames("Ubuntu\nvm-app-wt-1-1\n")
		if len(names) != 2 || names[0] != "Ubuntu" || names[1] != "vm-app-wt-1-1" {
			t.Errorf("names = %v", names)
		}
	})
}

func TestMachineUnavailableSentinel(t *testing.T) {
	// The platform's no-runner error is the marker the driver maps to
	// unavailable; it must survive wrapping.
	err := machineUnavailable("this platform has no VM runner")
	if !errors.Is(err, ErrMachineUnavailable) {
		t.Fatalf("machineUnavailable must wrap ErrMachineUnavailable: %v", err)
	}
}

// withShortMachineStartGrace shrinks the package var for the duration of
// one test, so a grace-period test runs in milliseconds rather than
// waiting out the real two seconds the live runners use.
func withShortMachineStartGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := machineStartGrace
	machineStartGrace = d
	t.Cleanup(func() { machineStartGrace = old })
}

// helperProcess starts this test binary re-invoked as
// TestMachineStartHelperProcess, the standard exec.Command-on-itself
// fixture (the same trick os/exec's own tests use): it behaves however
// mode says, and it is the one way to get a real, short-lived child
// process without depending on a shell being on PATH, which the Windows
// advisory job cannot assume the way the Linux gating job can.
func helperProcess(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMachineStartHelperProcess")
	cmd.Env = append(os.Environ(),
		"WT_MACHINE_TEST_HELPER=1",
		"WT_MACHINE_TEST_HELPER_MODE="+mode)
	return cmd
}

// TestMachineStartHelperProcess is not a real test of this package: it is
// the subprocess body helperProcess spawns. A normal `go test` run leaves
// WT_MACHINE_TEST_HELPER unset, so it returns immediately and passes
// trivially; only the re-invocation above sets the variable and reaches
// the switch.
func TestMachineStartHelperProcess(t *testing.T) {
	if os.Getenv("WT_MACHINE_TEST_HELPER") != "1" {
		return
	}
	switch os.Getenv("WT_MACHINE_TEST_HELPER_MODE") {
	case "fail-fast":
		fmt.Fprintln(os.Stderr, "boom: missing sibling binary")
		os.Exit(3)
	case "keep-running":
		time.Sleep(2 * time.Second)
	}
	os.Exit(0)
}

// TestAwaitMachineStartReportsAnEarlyExit is 1a's core claim: a child that
// exits inside the grace period is never mistaken for a boot in progress.
// Before this existed, cmd.Start() alone reported success the instant the
// process was spawned, indistinguishable from one actually booting — a
// colima dying on a missing limactl looked exactly like colima warming up
// (ea6e8e5).
func TestAwaitMachineStartReportsAnEarlyExit(t *testing.T) {
	withShortMachineStartGrace(t, 200*time.Millisecond)

	cmd := helperProcess(t, "fail-fast")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := awaitMachineStart(cmd, &buf)
	if err == nil {
		t.Fatal("a child that exits during the grace period must be reported as an error")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("error must say the child exited during startup: %v", err)
	}
	if !strings.Contains(err.Error(), "boom: missing sibling binary") {
		t.Errorf("error must carry the child's captured output: %v", err)
	}
}

// TestAwaitMachineStartLeavesALiveChildDetached is the other half: a child
// still running when the window closes is left alone, exactly as an
// unwatched cmd.Start() would have reported — the grace period must never
// turn into a wait for the real boot, which is measured in minutes.
func TestAwaitMachineStartLeavesALiveChildDetached(t *testing.T) {
	withShortMachineStartGrace(t, 200*time.Millisecond)

	cmd := helperProcess(t, "keep-running")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// awaitMachineStart's own goroutine owns the one legal call to
	// cmd.Wait() from here on — exec.Cmd forbids calling it twice
	// concurrently — so cleanup only signals the child; that goroutine
	// reaps it once the kill lands.
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})

	if err := awaitMachineStart(cmd, &buf); err != nil {
		t.Fatalf("a still-running child must report nil, got %v", err)
	}
}
