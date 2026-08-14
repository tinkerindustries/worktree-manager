//go:build acceptance

package coord

// acceptance_test.go is the coordinator's live layer, run with
// `go test -tags acceptance ./...`: exit criterion 5 (the probe run from
// the coordinator sees a port published to the host by a container) and
// exit criterion 3 against the real docker runner (an unreachable daemon
// leaves the entry in tearing-down with the slot held). Every object created
// here is removed before the test returns.

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// TestAcceptanceCoordinatorProbeSeesPublishedPort is exit criterion 5: the
// probe run from the coordinator — the Handler's own probe seam, the one
// allocation consults — sees a port published by a container.
//
// This is the relationship revision 2 relies on: probing runs in the
// coordinator's network namespace, and a port a container publishes there is
// visible to it (ARCHITECTURE.md §8.4). publishArgs picks the construction
// that puts the published port in this process's namespace, which differs
// between a host and a container sharing the daemon.
func TestAcceptanceCoordinatorProbeSeesPublishedPort(t *testing.T) {
	if err := driver.NewDocker().Version(); err != nil {
		t.Fatalf("acceptance test needs a docker daemon: %v", err)
	}
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	if _, err := h.Connect(api.KindHost, ""); err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))

	// A free port, then a container publishing it into our namespace.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	name := fmt.Sprintf("wtp4-probe-%d", port)
	// Registered before the container is created, so a failed start still
	// cleans up rather than leaving the container on the machine.
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	args := append([]string{"run", "-d", "--name", name}, publishArgs(t, port)...)
	args = append(args, "busybox:latest", "httpd", "-f", "-p", fmt.Sprint(port))
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("publishing the port: %s", strings.TrimSpace(string(out)))
	}

	// The container's httpd binds the port in our namespace shortly after
	// start; poll until the coordinator's probe reports the slot held.
	sp := teardownSpec(t)
	sp.Resources = []spec.Resource{{Type: "port", Name: "api"}}
	resources := map[string]spec.Resolved{"api": {Type: "port", Value: port}}
	for i := 0; i < 8; i++ {
		if got := h.H.Probe(sp, 1, resources); got == ProbeHeld {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the coordinator's probe never saw port %d, published by a container, as held", port)
}

// TestAcceptanceTeardownUnavailableMovesEntryToTearingDown is exit criterion
// 3 against the real docker runner: with the daemon unreachable, teardown is
// unavailable (exit 4), the entry moves to tearing-down with a note, and the
// slot is not freed — the distinction that an unavailable teardown blocks
// freeing the slot (plan.md §3).
func TestAcceptanceTeardownUnavailableMovesEntryToTearingDown(t *testing.T) {
	// Point the docker CLI at an endpoint nothing listens on. The real
	// runner honours DOCKER_HOST, so the daemon is genuinely unreachable for
	// this test while the machine's real daemon stays untouched.
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")

	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	h.H.Docker = driver.NewDocker()

	resp := h.H.Teardown(sess, ref, sp, nil)
	if resp.Error == nil || resp.Error.Code != 4 {
		t.Fatalf("teardown = %+v, want exit code 4 (unavailable)", resp.Error)
	}
	if !strings.Contains(resp.Error.Msg, "unreachable") {
		t.Errorf("the error must name the cause: %q", resp.Error.Msg)
	}
	if resp.Error.Remedy == "" {
		t.Error("the error must carry a remedy")
	}

	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	e := registryEntry(reg, ref.App, ref.Slug)
	if e == nil || e.State != store.StateTearingDown {
		t.Fatalf("entry = %+v, want tearing-down", e)
	}
	if !strings.Contains(e.TeardownNote, "unreachable") {
		t.Errorf("the entry's note must name what went wrong: %q", e.TeardownNote)
	}
	res2, perr := allocate(t, h, sess, sp, "wt-2")
	if perr != nil || res2.Slot != 2 {
		t.Fatalf("the slot must stay held: second allocation = slot %d / %+v, want 2", res2.Slot, perr)
	}
}

// publishArgs returns the docker run flags that make a container's port
// reachable from this process's network namespace — the coordinator's, since
// the coordinator is what probes.
//
// On a host the daemon publishes onto the host's loopback, which is where the
// probe binds, and that is the arrangement the coordinator really runs in.
// Inside a container sharing the daemon, the host's loopback is not this
// process's loopback, so the container joins this process's namespace instead.
// The two are told apart by asking the daemon whether this process's hostname
// names a container it knows.
func publishArgs(t *testing.T, port int) []string {
	t.Helper()
	if me, err := os.Hostname(); err == nil && me != "" {
		if exec.Command("docker", "inspect", "--type", "container", me).Run() == nil {
			return []string{"--network", "container:" + me}
		}
	}
	return []string{"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, port)}
}
