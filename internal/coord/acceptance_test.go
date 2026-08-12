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

	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// TestAcceptanceCoordinatorProbeSeesPublishedPort is exit criterion 5: the
// probe run from the coordinator — the Handler's own probe seam, the one
// allocation consults — sees a port published by a container.
//
// This test process runs inside a container whose network namespace is not
// the daemon's publishing namespace (the daemon binds published ports on
// the host's loopback, invisible from here), so the container publishes into
// the coordinator's own namespace — the relationship revision 2 relies on:
// probing runs in the coordinator's network namespace, and a port a
// container publishes there is visible to it (ARCHITECTURE.md §8.4).
func TestAcceptanceCoordinatorProbeSeesPublishedPort(t *testing.T) {
	if err := driver.NewDocker().Version(); err != nil {
		t.Fatalf("acceptance test needs a docker daemon: %v", err)
	}
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	if _, reply := h.Connect(protocol.KindHost, ""); reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))

	// A free port, then a container publishing it into our namespace.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	me, err := os.Hostname()
	if err != nil {
		t.Fatalf("our container id: %v", err)
	}
	name := fmt.Sprintf("wtp4-probe-%d", port)
	if out, err := exec.Command("docker", "run", "-d", "--name", name,
		"--network", "container:"+me,
		"busybox:latest", "httpd", "-f", "-p", fmt.Sprint(port)).CombinedOutput(); err != nil {
		t.Fatalf("publishing the port: %s", strings.TrimSpace(string(out)))
	}
	defer exec.Command("docker", "rm", "-f", name).Run()

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

	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
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
