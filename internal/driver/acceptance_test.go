//go:build acceptance

package driver

// acceptance_test.go is the live layer: the exit criteria that need a real
// docker daemon, run with `go test -tags acceptance ./...` (TESTING.md).
// Every object created here is labelled with a project name unique to this
// run and removed in t.Cleanup, so a failed test still leaves nothing
// behind. The untagged twins against a fake docker live in the drivers'
// own test files.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// freePort finds a port nothing is bound to right now.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}
	return port
}

func timeSleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// acceptanceProject mints a compose project name nothing else is using:
// unique to this run (a random suffix) and prefixed for the run's identity.
func acceptanceProject(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random project name: %v", err)
	}
	return "wtp4-" + hex.EncodeToString(b)
}

// requireDocker fails the test loudly when the daemon is unreachable — the
// acceptance tag is the contract that docker exists, and a silent skip
// would read as a pass.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := NewDocker().Version(); err != nil {
		t.Fatalf("acceptance test needs a docker daemon: %v", err)
	}
}

// createLabeledContainer creates one stopped container carrying the project
// label, the way a compose run would.
func createLabeledContainer(t *testing.T, project, name string) {
	t.Helper()
	runDockerCLI(t, "create", "--label", "com.docker.compose.project="+project,
		"--name", name, "busybox:latest", "sleep", "3600")
}

// runDockerCLI runs one docker command, failing the test on error.
func runDockerCLI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

// cleanupProject removes every object carrying the project label. Registered
// as a cleanup so a failed test still leaves nothing behind.
func cleanupProject(t *testing.T, d Docker, project string) {
	t.Helper()
	t.Cleanup(func() {
		ids, _ := d.ListContainers(project)
		if len(ids) > 0 {
			d.RemoveContainers(ids)
		}
		ids, _ = d.ListNetworks(project)
		if len(ids) > 0 {
			d.RemoveNetworks(ids)
		}
		names, _ := d.ListVolumes(project)
		if len(names) > 0 {
			d.RemoveVolumes(names)
		}
	})
}

// projectCount counts the objects carrying the project label.
func projectCount(t *testing.T, d Docker, project string) int {
	t.Helper()
	containers, err := d.ListContainers(project)
	if err != nil {
		t.Fatalf("listing project containers: %v", err)
	}
	networks, err := d.ListNetworks(project)
	if err != nil {
		t.Fatalf("listing project networks: %v", err)
	}
	volumes, err := d.ListVolumes(project)
	if err != nil {
		t.Fatalf("listing project volumes: %v", err)
	}
	return len(containers) + len(networks) + len(volumes)
}

// TestAcceptanceTeardownByLabelWithWorktreeDeleted is exit criterion 1
// against the real daemon: teardown by label succeeds with the worktree
// directory deleted first, because the handle is the resolved project name
// from the registry, never anything read from the tree. It also pins the
// namespace probe against real objects: held while the objects exist, free
// after teardown.
func TestAcceptanceTeardownByLabelWithWorktreeDeleted(t *testing.T) {
	requireDocker(t)
	d := NewDocker()
	project := acceptanceProject(t)
	cleanupProject(t, d, project)

	// The worktree is gone before teardown runs, the way `git worktree
	// remove` leaves it.
	worktree := filepath.Join(t.TempDir(), "gone")
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("deleting the worktree: %v", err)
	}

	createLabeledContainer(t, project, project+"-c1")
	runDockerCLI(t, "network", "create", "--label", "com.docker.compose.project="+project, project+"-net")
	runDockerCLI(t, "volume", "create", "--label", "com.docker.compose.project="+project, project+"-vol")

	_, env, res := nsFixture(t, d, worktree)
	// The fixture's dev project resolves through the shared derivation; use
	// the acceptance project's name as the handle instead, so the objects
	// above are exactly what teardown must find.
	env.Resolved["compose"] = spec.Resolved{Type: "namespace", Value: project}
	env.Resolved["compose_test"] = spec.Resolved{Type: "namespace", Value: project + "-test"}

	if got := (&Namespace{}).Probe(res, project, env); got != ProbeHeld {
		t.Fatalf("probe before teardown = %v, want held (the objects carry the label)", got)
	}

	if err := (&Namespace{}).Teardown(res, project, env); err != nil {
		t.Fatalf("Teardown with the worktree deleted: %v", err)
	}
	if n := projectCount(t, d, project); n != 0 {
		t.Fatalf("%d object(s) survived the teardown", n)
	}
	if got := (&Namespace{}).Probe(res, project, env); got != ProbeFree {
		t.Errorf("probe after teardown = %v, want free", got)
	}
}

// TestAcceptanceVerifyPinnedNameAgainstLiveStack is exit criterion 2 against
// the real daemon: a compose file that pins name: makes the second stack
// silently attach to the first one's containers — nothing fails — and verify
// reports the pinned name, the finding that catches it.
func TestAcceptanceVerifyPinnedNameAgainstLiveStack(t *testing.T) {
	requireDocker(t)
	d := NewDocker()
	project := acceptanceProject(t)
	pinned := project + "-pinned"
	cleanupProject(t, d, pinned)

	worktree := t.TempDir()
	composeFile := filepath.Join(worktree, "compose.yaml")
	content := "name: " + pinned + "\nservices:\n  api:\n    image: busybox:latest\n    command: sleep 3600\n"
	if err := os.WriteFile(composeFile, []byte(content), 0o644); err != nil {
		t.Fatalf("writing the compose file: %v", err)
	}

	// Two ups against the pinned project: the second attaches silently.
	up := exec.Command("docker", "compose", "-f", composeFile, "up", "-d")
	up.Dir = worktree
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("first compose up: %s", strings.TrimSpace(string(out)))
	}
	up = exec.Command("docker", "compose", "-f", composeFile, "up", "-d")
	up.Dir = worktree
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("second compose up: %s", strings.TrimSpace(string(out)))
	}

	// Nothing failed, and the container count did not double — the attach is
	// the D2 failure: silent.
	containers, err := d.ListContainers(pinned)
	if err != nil {
		t.Fatalf("listing the pinned project's containers: %v", err)
	}
	if n := len(containers); n != 1 {
		t.Fatalf("pinned project has %d containers after two ups, want 1 (the second attach must be silent)", n)
	}

	_, env, res := nsFixture(t, d, worktree)
	env.Spec.Resources[0].Files = []string{"compose.yaml"}
	findings, err := (&Namespace{}).Verify(res, pinned, env)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var found *Finding
	for i := range findings {
		if findings[i].Level == LevelError && strings.Contains(findings[i].Message, "pins name") {
			found = &findings[i]
		}
	}
	if found == nil {
		t.Fatalf("verify findings %+v do not include the pinned-name finding", findings)
	}
	if !strings.Contains(found.Message, pinned) {
		t.Errorf("the finding must name the pinned value: %+v", found)
	}
}

// TestAcceptanceTeardownContinuesPastFailure is exit criterion 6 against the
// real daemon: a volume in use by a container the tool does not own survives
// its project's teardown, and the teardown continues past that failure —
// the dependent project's other objects and the dev project's objects are
// still removed, and the report names exactly what survived.
func TestAcceptanceTeardownContinuesPastFailure(t *testing.T) {
	requireDocker(t)
	d := NewDocker()
	project := acceptanceProject(t)
	test := project + "-test"
	cleanupProject(t, d, project)
	cleanupProject(t, d, test)

	createLabeledContainer(t, project, project+"-c1")
	runDockerCLI(t, "network", "create", "--label", "com.docker.compose.project="+project, project+"-net")
	runDockerCLI(t, "volume", "create", "--label", "com.docker.compose.project="+project, project+"-vol")
	createLabeledContainer(t, test, test+"-c1")
	runDockerCLI(t, "network", "create", "--label", "com.docker.compose.project="+test, test+"-net")
	volName := test + "-vol"
	runDockerCLI(t, "volume", "create", "--label", "com.docker.compose.project="+test, volName)

	// The test project's volume is held by a container the tool does not own
	// — no project label, so teardown by label will not touch it, and the
	// volume removal must fail.
	foreign := acceptanceProject(t) + "-foreign"
	runDockerCLI(t, "run", "-d", "--name", foreign, "-v", volName+":/data", "busybox:latest", "sleep", "3600")
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", foreign).Run()
	})

	_, env, res := nsFixture(t, d, t.TempDir())
	env.Resolved["compose"] = spec.Resolved{Type: "namespace", Value: project}
	env.Resolved["compose_test"] = spec.Resolved{Type: "namespace", Value: test}

	err := (&Namespace{}).Teardown(res, project, env)
	if !isTeardownError(err) {
		t.Fatalf("Teardown = %v, want a TeardownError naming the survivor", err)
	}
	te := err.(*TeardownError)
	if len(te.Survivors) != 1 || te.Survivors[0].Kind != "volume" || !strings.Contains(te.Survivors[0].Name, volName) {
		t.Fatalf("survivors = %+v, want exactly the in-use volume %s", te.Survivors, volName)
	}
	// Everything else went: the test project's container and network, the
	// dev project's container, network and volume.
	leftover := 0
	for _, list := range []func() ([]string, error){
		func() ([]string, error) { return d.ListContainers(project) },
		func() ([]string, error) { return d.ListNetworks(project) },
		func() ([]string, error) { return d.ListVolumes(project) },
		func() ([]string, error) { return d.ListContainers(test) },
		func() ([]string, error) { return d.ListNetworks(test) },
	} {
		objs, lerr := list()
		if lerr != nil {
			t.Fatalf("listing survivors: %v", lerr)
		}
		leftover += len(objs)
	}
	if leftover != 0 {
		t.Fatalf("teardown must continue past the failure: %d objects survived beyond the volume", leftover)
	}
	vols, err := d.ListVolumes(test)
	if err != nil {
		t.Fatalf("listing the test project's volumes: %v", err)
	}
	if len(vols) != 1 {
		t.Fatalf("the in-use volume must be the only survivor, got %v", vols)
	}
}

// TestAcceptancePortProbeSeesPublishedContainerPort is exit criterion 5 at
// the driver level: the probe sees a port published by a container. The
// coordinator-level half lives in internal/coord.
//
// The probe binds on loopback in whichever network namespace this process
// occupies, and the container has to publish into that same namespace for
// the test to mean anything. Which construction achieves that depends on
// where the test runs, so publishArgs works it out rather than assuming.
// On a host — the coordinator's real situation — the daemon publishes onto
// the host's loopback and an ordinary -p is right. Inside a container
// sharing the daemon, the host's loopback is invisible and the container
// has to join this process's namespace instead.
func TestAcceptancePortProbeSeesPublishedContainerPort(t *testing.T) {
	requireDocker(t)
	port := freePort(t)
	project := acceptanceProject(t)

	name := project + "-pub"
	// Registered before the container is created: runDockerCLI fails the
	// test on a non-zero exit, and a cleanup registered after it would never
	// run — which is how an earlier version of this test leaked containers
	// onto the machine it failed on.
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	args := append([]string{"run", "-d", "--name", name}, publishArgs(t, port)...)
	args = append(args, "busybox:latest", "httpd", "-f", "-p", fmt.Sprint(port))
	runDockerCLI(t, args...)

	// The container's httpd binds the port in our namespace shortly after
	// start; poll until the probe sees it held.
	p := &Port{}
	for i := 0; i < 20; i++ {
		if got := p.Probe(nil, port, Env{}); got == ProbeHeld {
			return
		}
		timeSleep(200)
	}
	t.Fatalf("the probe never saw port %d, published by container %s, as held", port, name)
}

// publishArgs returns the docker run flags that make a container's port
// reachable from this process's network namespace.
//
// On a host the daemon publishes onto the host's loopback, which is where
// the probe binds, so -p is both correct and the arrangement the coordinator
// actually runs in. Inside a container that shares the daemon, the host's
// loopback is not this process's loopback, so the published port would be
// invisible; there the container joins this process's namespace instead.
// The two are distinguished by asking the daemon whether this process's
// hostname names a container it knows — a capability question, not a guess
// from /.dockerenv.
func publishArgs(t *testing.T, port int) []string {
	t.Helper()
	if me, err := os.Hostname(); err == nil && me != "" {
		if exec.Command("docker", "inspect", "--type", "container", me).Run() == nil {
			return []string{"--network", "container:" + me}
		}
	}
	return []string{"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, port)}
}
