package driver

// namespace_test.go exercises the namespace driver against a fake docker
// seam: probe by label, teardown by label across the project and its
// dependents, the reserved-name refusal, teardown continuing past a failure,
// and the pinned-name finding. The docker-tagged twins against the real
// daemon live in acceptance_test.go. The machine-binding rails (items 3
// and 4) and the network-endpoint widening (item 2) are exercised further
// down, against nsFixtureWithMachine and the fakeMachine from
// machine_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// fakeDocker is the test seam: objects keyed by project, optional failures
// per object, and a log of what was removed in what order.
//
// host and the three log fields are pointer-or-shared so that WithHost's
// copy (a distinct *fakeDocker value, host set, everything else shared)
// still writes into the same log the test holds: the namespace driver's
// real calls run on the bound copy, never on the value a test constructed
// with newFakeDocker, and a log that did not survive the copy would leave
// every binding test unable to see what happened.
type fakeDocker struct {
	host       string
	versionErr error
	containers map[string][]string // project → container ids
	networks   map[string][]string
	volumes    map[string][]string
	// allNetworks maps every network name to its subnet, the cidr
	// driver's probe input (ListNetworksAll / NetworkSubnet).
	allNetworks map[string]string
	failRemove  map[string]error // "container:<id>", "network:<id>", "volume:<name>" → error
	removed     *[]string

	// networkEndpoints and disconnectErr are item 2's fixture: containers
	// still attached to a network regardless of the compose project label,
	// and the disconnect fallback's optional failures.
	networkEndpoints map[string][]string // network → container names
	disconnectErr    map[string]error    // "network:container" → error
	disconnected     *[]string           // "network:container" entries, in order

	// hostsUsed is item 4's fixture: every host a docker-touching call
	// actually ran under, "" for the ambient (never bound) seam, in call
	// order.
	hostsUsed *[]string
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers:       map[string][]string{},
		networks:         map[string][]string{},
		volumes:          map[string][]string{},
		allNetworks:      map[string]string{},
		failRemove:       map[string]error{},
		removed:          &[]string{},
		networkEndpoints: map[string][]string{},
		disconnectErr:    map[string]error{},
		disconnected:     &[]string{},
		hostsUsed:        &[]string{},
	}
}

// WithHost returns a copy bound to endpoint, sharing every log and every
// object map with the receiver — exactly the contract execDocker's own
// WithHost carries, and the one the namespace driver relies on to run its
// real calls on the bound copy while a test still inspects the original.
func (f *fakeDocker) WithHost(endpoint string) Docker {
	cp := *f
	cp.host = endpoint
	return &cp
}

func (f *fakeDocker) recordHost() { *f.hostsUsed = append(*f.hostsUsed, f.host) }

func (f *fakeDocker) Version() error { f.recordHost(); return f.versionErr }

func (f *fakeDocker) ListContainers(p string) ([]string, error) {
	f.recordHost()
	return f.containers[p], nil
}
func (f *fakeDocker) ListNetworks(p string) ([]string, error) {
	f.recordHost()
	return f.networks[p], nil
}
func (f *fakeDocker) ListVolumes(p string) ([]string, error) {
	f.recordHost()
	return f.volumes[p], nil
}

func (f *fakeDocker) ListNetworksAll() ([]string, error) {
	f.recordHost()
	names := make([]string, 0, len(f.allNetworks))
	for name := range f.allNetworks {
		names = append(names, name)
	}
	return names, nil
}
func (f *fakeDocker) NetworkSubnet(name string) (string, error) {
	f.recordHost()
	return f.allNetworks[name], nil
}

func (f *fakeDocker) RemoveContainers(ids []string) error {
	f.recordHost()
	return f.remove("container", ids, f.containers)
}
func (f *fakeDocker) RemoveNetworks(ids []string) error {
	f.recordHost()
	return f.remove("network", ids, f.networks)
}
func (f *fakeDocker) RemoveVolumes(names []string) error {
	f.recordHost()
	return f.remove("volume", names, f.volumes)
}

// remove models docker's batch semantics: objects are removed in order until
// the first failure, which fails the whole call.
func (f *fakeDocker) remove(kind string, ids []string, byProject map[string][]string) error {
	for _, id := range ids {
		if err := f.failRemove[kind+":"+id]; err != nil {
			return err
		}
		*f.removed = append(*f.removed, kind+":"+id)
	}
	for project, list := range byProject {
		kept := list[:0]
		for _, id := range list {
			if !containsStr(ids, id) {
				kept = append(kept, id)
			}
		}
		byProject[project] = kept
	}
	return nil
}

// NetworkEndpoints returns the containers item 2's fixture attached to
// network, whatever label they carry (or none).
func (f *fakeDocker) NetworkEndpoints(network string) ([]string, error) {
	f.recordHost()
	return f.networkEndpoints[network], nil
}

// DisconnectContainer force-disconnects container from network, removing
// it from the fixture's endpoint list on success — mirroring the real
// daemon, so a caller that re-lists endpoints afterward sees it gone.
func (f *fakeDocker) DisconnectContainer(network, container string) error {
	f.recordHost()
	if err := f.disconnectErr[network+":"+container]; err != nil {
		return err
	}
	*f.disconnected = append(*f.disconnected, network+":"+container)
	kept := f.networkEndpoints[network][:0]
	for _, c := range f.networkEndpoints[network] {
		if c != container {
			kept = append(kept, c)
		}
	}
	f.networkEndpoints[network] = kept
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// nsFixture builds the compose-app-shaped spec and environment: a dev
// project and its dependent test project, resolved through the shared
// derivation.
func nsFixture(t *testing.T, d Docker, worktree string) (*spec.Spec, Env, *spec.Resource) {
	t.Helper()
	max := 32
	s := &spec.Spec{
		Version: 1, App: "compose-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "namespace", Name: "compose", Kind: strPtr("compose"),
				Template: strPtr("{app}-{slug}-{slot}"), Files: []string{"compose.yaml", "compose.test.yaml"}},
			{Type: "namespace", Name: "compose_test", Kind: strPtr("compose"),
				Template: strPtr("{compose}-test")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("fixture spec does not validate: %v", err)
	}
	home := filepath.Dir(worktree)
	ctx := spec.Context{
		App: "compose-app", Slug: "wt-1", Slot: 1,
		Home: home, Worktree: worktree,
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the fixture: %v", err)
	}
	env := Env{
		Spec: s, App: "compose-app", Slug: "wt-1", Slot: 1,
		Home: home, Worktree: worktree,
		Resolved: table,
		Docker:   d,
	}
	return s, env, spec.ResourceByName(s, "compose")
}

func nsValue(t *testing.T, s *spec.Spec, env Env, name string) string {
	t.Helper()
	v := env.Resolved[name].Value
	p, ok := v.(string)
	if !ok {
		t.Fatalf("resolved %s = %#v, want a string", name, v)
	}
	return p
}

func TestNamespaceProbeByLabel(t *testing.T) {
	_, env, res := nsFixture(t, newFakeDocker(), t.TempDir())
	project := nsValue(t, env.Spec, env, "compose")

	t.Run("clean project probes free", func(t *testing.T) {
		if got := (&Namespace{}).Probe(res, project, env); got != ProbeFree {
			t.Errorf("Probe = %v, want free", got)
		}
	})

	t.Run("a hit means an incomplete teardown", func(t *testing.T) {
		d := newFakeDocker()
		d.containers[project] = []string{"c1"}
		env.Docker = d
		if got := (&Namespace{}).Probe(res, project, env); got != ProbeHeld {
			t.Errorf("Probe = %v, want held (a leftover means an incomplete teardown)", got)
		}
	})

	t.Run("unreachable daemon is unavailable", func(t *testing.T) {
		d := newFakeDocker()
		d.versionErr = &ErrUnavailable{Reason: "the docker daemon is unreachable"}
		env.Docker = d
		if got := (&Namespace{}).Probe(res, project, env); got != ProbeUnavailable {
			t.Errorf("Probe = %v, want unavailable", got)
		}
	})

	t.Run("no docker seam is unavailable", func(t *testing.T) {
		env.Docker = nil
		if got := (&Namespace{}).Probe(res, project, env); got != ProbeUnavailable {
			t.Errorf("Probe = %v, want unavailable", got)
		}
	})

	t.Run("plain kind is always free", func(t *testing.T) {
		plain := &spec.Resource{Type: "namespace", Name: "plain", Kind: strPtr("plain"),
			Template: strPtr("{app}-{slug}")}
		if got := (&Namespace{}).Probe(plain, "anything", env); got != ProbeFree {
			t.Errorf("plain Probe = %v, want free", got)
		}
	})
}

// TestNamespaceTeardownByLabelIncludingDependents is the B3.2 rail: tearing
// down the dev project also tears down the dependent test project, in
// container-network-volume order, dependents first.
func TestNamespaceTeardownByLabelIncludingDependents(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")
	test := nsValue(t, env.Spec, env, "compose_test")

	d.containers[dev] = []string{"c-dev"}
	d.networks[dev] = []string{"n-dev"}
	d.volumes[dev] = []string{"v-dev"}
	d.containers[test] = []string{"c-test"}
	d.networks[test] = []string{"n-test"}
	d.volumes[test] = []string{"v-test"}

	if err := (&Namespace{}).Teardown(res, dev, env); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(d.containers[dev]) != 0 || len(d.networks[dev]) != 0 || len(d.volumes[dev]) != 0 {
		t.Errorf("the dev project's objects survived: %+v", d)
	}
	if len(d.containers[test]) != 0 || len(d.networks[test]) != 0 || len(d.volumes[test]) != 0 {
		t.Errorf("the dependent test project's objects survived: %+v", d)
	}
	// Order: the dependent project's objects leave before the dev project's
	// (its network is shared, so it must), and each project goes containers,
	// networks, volumes.
	want := []string{
		"container:c-test", "network:n-test", "volume:v-test",
		"container:c-dev", "network:n-dev", "volume:v-dev",
	}
	if strings.Join(*d.removed, ",") != strings.Join(want, ",") {
		t.Errorf("removal order = %v, want %v", *d.removed, want)
	}
}

// TestNamespaceTeardownHandleFromRegistry is exit criterion 1's untagged
// half: teardown succeeds with the worktree directory deleted first, because
// the handle is the resolved project name from the registry — nothing is
// read from the tree (03-drivers.md §2.2).
func TestNamespaceTeardownHandleFromRegistry(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "gone-worktree")
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, worktree)
	dev := nsValue(t, env.Spec, env, "compose")
	d.containers[dev] = []string{"c1"}

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("test setup: the worktree must not exist")
	}
	if err := (&Namespace{}).Teardown(res, dev, env); err != nil {
		t.Fatalf("Teardown with the worktree deleted must succeed: %v", err)
	}
	if len(d.containers[dev]) != 0 {
		t.Errorf("the container survived: %+v", d.containers)
	}
}

// TestNamespaceTeardownReservedNameRefused is exit criterion 4: a namespace
// resolving to a reserved name is refused, naming the reservation — the
// B8.2 rail that label-based teardown must never reach a co-resident
// production stack.
func TestNamespaceTeardownReservedNameRefused(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")
	d.containers[dev] = []string{"c1"}

	env.Reservations = []Reservation{
		{Ports: []int{5319, 5320}, Names: []string{dev}, Note: "compose-app production stack (compose.prod.yaml)"},
	}
	err := (&Namespace{}).Teardown(res, dev, env)
	if !isRefusal(err) {
		t.Fatalf("teardown of a reserved name must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "compose-app production stack") {
		t.Errorf("the refusal must name the reservation: %v", err)
	}
	if !strings.Contains(err.Error(), dev) {
		t.Errorf("the refusal must name the project: %v", err)
	}
	if len(d.containers[dev]) != 1 {
		t.Errorf("the reserved project's objects must be untouched: %+v", d.containers)
	}
}

// TestNamespaceTeardownUnavailable: an unreachable daemon is unavailable,
// not a partial failure — nothing was attempted, and the slot must not be
// freed.
func TestNamespaceTeardownUnavailable(t *testing.T) {
	d := newFakeDocker()
	d.versionErr = &ErrUnavailable{Reason: "the docker daemon is unreachable: Cannot connect"}
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")

	err := (&Namespace{}).Teardown(res, dev, env)
	if !isErrUnavailable(err) {
		t.Fatalf("Teardown with the daemon down = %v, want unavailable", err)
	}
	if len(*d.removed) != 0 {
		t.Errorf("nothing may be attempted while the daemon is unreachable: %v", *d.removed)
	}
}

// TestNamespaceTeardownContinuesPastFailure is exit criterion 6's untagged
// half: a failure on one project's volume does not stop the teardown of the
// rest — everything that survived is reported, and the other project is
// still fully torn down.
func TestNamespaceTeardownContinuesPastFailure(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")
	test := nsValue(t, env.Spec, env, "compose_test")

	d.containers[dev] = []string{"c-dev"}
	d.networks[dev] = []string{"n-dev"}
	d.volumes[dev] = []string{"v-dev"}
	d.containers[test] = []string{"c-test"}
	d.networks[test] = []string{"n-test"}
	d.volumes[test] = []string{"v-test"}
	// The test project's volume is in use by a container the tool does not
	// own (not labeled with the project), so removing it fails.
	d.failRemove["volume:v-test"] = &ErrUnavailable{Reason: "volume is in use by an unlabeled container"}

	err := (&Namespace{}).Teardown(res, dev, env)
	if !isTeardownError(err) {
		t.Fatalf("Teardown = %v, want a TeardownError listing the survivor", err)
	}
	te := err.(*TeardownError)
	if len(te.Survivors) != 1 || te.Survivors[0].Kind != "volume" || te.Survivors[0].Name != "v-test" {
		t.Fatalf("survivors = %+v, want exactly the in-use volume v-test", te.Survivors)
	}
	if !strings.Contains(te.Error(), "v-test") {
		t.Errorf("the error must name what survived: %v", te)
	}
	// The dev project was still fully torn down, and the test project's
	// containers and network went too.
	if len(d.containers[dev]) != 0 || len(d.networks[dev]) != 0 || len(d.volumes[dev]) != 0 {
		t.Errorf("the dev project must be fully torn down despite the failure: %+v", d)
	}
	if len(d.containers[test]) != 0 || len(d.networks[test]) != 0 {
		t.Errorf("teardown must continue past the failure: %+v", d)
	}
	if len(d.volumes[test]) != 1 {
		t.Errorf("the in-use volume must be the only survivor: %+v", d.volumes)
	}
}

// TestNamespaceVerifyPinnedName is exit criterion 2's untagged half: verify
// reads the compose files a namespace governs and reports a pinned name: —
// the finding that catches the silent-attach failure (D2).
func TestNamespaceVerifyPinnedName(t *testing.T) {
	worktree := t.TempDir()
	file := filepath.Join(worktree, "compose.yaml")
	os.WriteFile(file, []byte("name: compose-app-prod\nservices:\n  api:\n    image: x\n"), 0o644)
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, worktree)
	dev := nsValue(t, env.Spec, env, "compose")

	findings, err := (&Namespace{}).Verify(res, dev, env)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var pinned []Finding
	for _, f := range findings {
		if f.Level == LevelError && strings.Contains(f.Message, "pins name") {
			pinned = append(pinned, f)
		}
	}
	if len(pinned) != 1 {
		t.Fatalf("findings = %+v, want exactly one pinned-name finding", findings)
	}
	if !strings.Contains(pinned[0].Message, "compose-app-prod") {
		t.Errorf("the finding must name the pinned value: %+v", pinned[0])
	}
	if !strings.Contains(pinned[0].Message, "silently attach") {
		t.Errorf("the finding must explain the silent attach: %+v", pinned[0])
	}
	if !strings.Contains(pinned[0].Message, dev) {
		t.Errorf("the finding must name the -p that beats the file: %+v", pinned[0])
	}
}

// TestNamespaceVerifyCannotCheckFromHere: a governed file that is not
// present is reported as a finding, never silently passed.
func TestNamespaceVerifyCannotCheckFromHere(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")

	findings, err := (&Namespace{}).Verify(res, dev, env)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want two cannot-check findings (compose.yaml, compose.test.yaml)", findings)
	}
	for _, f := range findings {
		if f.Level != LevelWarning || !strings.Contains(f.Message, "cannot be checked from here") {
			t.Errorf("finding = %+v, want a cannot-check warning", f)
		}
	}
}

func TestNamespaceBlastRadiusNamesSilentAttach(t *testing.T) {
	br := (&Namespace{}).BlastRadius(&spec.Resource{Name: "compose"}, nil)
	if !strings.Contains(br, "silently joins") {
		t.Errorf("BlastRadius = %q, want prose naming the silent attach", br)
	}
}

// --- items 3 and 4: the machine binding ----------------------------------

// nsFixtureWithMachine builds a compose namespace bound to a machine
// resource ("vm"), for items 3 and 4: the resolved instance name is
// deterministic ({app}-{slug}-{slot}), and env.Machine is the caller's own
// runner — typically a *fakeMachine from machine_test.go — so a test
// controls List() and DockerEndpoint() independently of the docker seam.
func nsFixtureWithMachine(t *testing.T, d Docker, m platform.MachineRunner) (Env, *spec.Resource, string) {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "vm-compose-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "machine", Name: "vm", Template: strPtr("{app}-{slug}-{slot}")},
			{Type: "namespace", Name: "compose", Kind: strPtr("compose"),
				Template: strPtr("{app}-{slug}-{slot}"), Machine: strPtr("vm")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("fixture spec does not validate: %v", err)
	}
	worktree := t.TempDir()
	home := filepath.Dir(worktree)
	ctx := spec.Context{App: "vm-compose-app", Slug: "wt-1", Slot: 1, Home: home, Worktree: worktree}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the fixture: %v", err)
	}
	env := Env{
		Spec: s, App: "vm-compose-app", Slug: "wt-1", Slot: 1,
		Home: home, Worktree: worktree,
		Resolved: table, Docker: d, Machine: m,
	}
	instance, ok := table["vm"].Value.(string)
	if !ok {
		t.Fatalf("resolved vm = %#v, want a string", table["vm"].Value)
	}
	return env, spec.ResourceByName(s, "compose"), instance
}

// TestNamespaceTeardownUsesTheBoundMachinesEndpoint is item 4's core claim:
// every docker call a bound namespace's teardown makes runs against the
// bound machine's own endpoint, never the ambient seam.
func TestNamespaceTeardownUsesTheBoundMachinesEndpoint(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine()
	env, res, instance := nsFixtureWithMachine(t, d, m)
	m.instances = []platform.MachineInstance{{Name: instance, Running: true}}
	project := nsValue(t, env.Spec, env, "compose")
	d.containers[project] = []string{"c1"}

	if err := (&Namespace{}).Teardown(res, project, env); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	wantHost := "unix:///fake/" + instance + "/docker.sock"
	if len(*d.hostsUsed) == 0 {
		t.Fatal("no docker call was recorded")
	}
	for _, h := range *d.hostsUsed {
		if h != wantHost {
			t.Errorf("hostsUsed = %v, want every call bound to %s", *d.hostsUsed, wantHost)
		}
	}
}

// TestNamespaceProbeUsesTheBoundMachinesEndpoint is Probe's half of item 4.
// Probe never gates allocation for a namespace (03-drivers.md §4.2) and so
// is unreachable from ProbeFromRegistry today, but the method is still
// part of the driver contract and must not read the wrong daemon if
// anything ever calls it directly.
func TestNamespaceProbeUsesTheBoundMachinesEndpoint(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine()
	env, res, instance := nsFixtureWithMachine(t, d, m)
	m.instances = []platform.MachineInstance{{Name: instance, Running: true}}
	project := nsValue(t, env.Spec, env, "compose")

	if got := (&Namespace{}).Probe(res, project, env); got != ProbeFree {
		t.Fatalf("Probe = %v, want ProbeFree (no objects yet)", got)
	}
	wantHost := "unix:///fake/" + instance + "/docker.sock"
	if len(*d.hostsUsed) == 0 {
		t.Fatal("no docker call was recorded")
	}
	for _, h := range *d.hostsUsed {
		if h != wantHost {
			t.Errorf("hostsUsed = %v, want every call bound to %s", *d.hostsUsed, wantHost)
		}
	}
}

// TestNamespaceTeardownWithNoMachineBindingUsesAmbient: a namespace with no
// machine: field is unaffected by items 3 and 4 — every call still runs
// against the ambient seam, exactly as before either rail existed.
func TestNamespaceTeardownWithNoMachineBindingUsesAmbient(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")
	d.containers[dev] = []string{"c1"}

	if err := (&Namespace{}).Teardown(res, dev, env); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(*d.hostsUsed) == 0 {
		t.Fatal("no docker call was recorded")
	}
	for _, h := range *d.hostsUsed {
		if h != "" {
			t.Errorf("a namespace with no machine: binding must always use the ambient seam, got host %q", h)
		}
	}
}

// TestNamespaceTeardownFallsBackToAmbientWhenEndpointUnsupported: WSL2 (or
// any platform with no separate per-machine endpoint) reports
// ErrDockerEndpointUnsupported, and the namespace driver falls back to the
// ambient seam rather than failing — the same behaviour a namespace with
// no binding always had, not a degraded one.
func TestNamespaceTeardownFallsBackToAmbientWhenEndpointUnsupported(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine()
	m.endpointErr = platform.ErrDockerEndpointUnsupported
	env, res, instance := nsFixtureWithMachine(t, d, m)
	m.instances = []platform.MachineInstance{{Name: instance, Running: true}}
	project := nsValue(t, env.Spec, env, "compose")
	d.containers[project] = []string{"c1"}

	if err := (&Namespace{}).Teardown(res, project, env); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(*d.hostsUsed) == 0 {
		t.Fatal("no docker call was recorded")
	}
	for _, h := range *d.hostsUsed {
		if h != "" {
			t.Errorf("an unsupported endpoint must fall back to the ambient seam, got host %q", h)
		}
	}
}

// TestNamespaceTeardownReportsErrorWhenEndpointResolutionFails: an endpoint
// error that is *not* the unsupported sentinel must never be read as
// "fall back to ambient" — that is exactly the cross-worktree guess this
// rail exists to refuse, so it is reported instead, and no docker call
// runs at all.
func TestNamespaceTeardownReportsErrorWhenEndpointResolutionFails(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine()
	m.endpointErr = errors.New("cannot determine the home directory")
	env, res, instance := nsFixtureWithMachine(t, d, m)
	m.instances = []platform.MachineInstance{{Name: instance, Running: true}}
	project := nsValue(t, env.Spec, env, "compose")

	err := (&Namespace{}).Teardown(res, project, env)
	te, ok := err.(*TeardownError)
	if !ok {
		t.Fatalf("Teardown = %v, want a *TeardownError naming the unresolved endpoint", err)
	}
	if len(te.Survivors) != 1 || !strings.Contains(te.Survivors[0].Reason, "cannot determine the home directory") {
		t.Errorf("survivors = %+v, want the underlying reason named", te.Survivors)
	}
	if len(*d.hostsUsed) != 0 {
		t.Errorf("no docker call may run when the endpoint cannot be resolved: %v", *d.hostsUsed)
	}
}

// TestNamespaceTeardownUnavailableWhenBoundButNoRunnerInstalled: a
// namespace bound to a machine on a coordinator with no runner installed
// at all is unavailable, not silently ambient.
func TestNamespaceTeardownUnavailableWhenBoundButNoRunnerInstalled(t *testing.T) {
	d := newFakeDocker()
	env, res, _ := nsFixtureWithMachine(t, d, nil)
	project := nsValue(t, env.Spec, env, "compose")

	err := (&Namespace{}).Teardown(res, project, env)
	if !isErrUnavailable(err) {
		t.Fatalf("Teardown with no runner installed for a bound machine = %v, want unavailable", err)
	}
	if len(*d.hostsUsed) != 0 {
		t.Errorf("no docker call may run: %v", *d.hostsUsed)
	}
}

// TestNamespaceTeardownVacuousWhenBoundMachineGone is item 3's core claim:
// a namespace whose bound machine instance does not exist on the runner is
// torn down vacuously — no docker call at all, no survivor, no held slot.
func TestNamespaceTeardownVacuousWhenBoundMachineGone(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine() // m.instances is empty: the bound machine is gone
	env, res, _ := nsFixtureWithMachine(t, d, m)
	project := nsValue(t, env.Spec, env, "compose")
	d.containers[project] = []string{"c1"} // would-be objects, never checked

	if err := (&Namespace{}).Teardown(res, project, env); err != nil {
		t.Fatalf("a namespace whose bound machine is gone must be torn down vacuously, got %v", err)
	}
	if len(*d.hostsUsed) != 0 {
		t.Errorf("no docker call may run against a deleted machine's namespace: %v", *d.hostsUsed)
	}
}

// TestNamespaceTeardownNotVacuousWhenMachineListFails: the deliberately
// narrower half of item 3 — the runner itself cannot answer whether the
// machine exists (Docker Desktop merely being stopped is exactly this
// shape), so "cannot tell" falls through to the ordinary path rather than
// being read as "gone".
func TestNamespaceTeardownNotVacuousWhenMachineListFails(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine()
	m.listErr = errors.New("colima: connection refused")
	env, res, instance := nsFixtureWithMachine(t, d, m)
	project := nsValue(t, env.Spec, env, "compose")
	_ = instance

	if err := (&Namespace{}).Teardown(res, project, env); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(*d.hostsUsed) == 0 {
		t.Error("the ordinary teardown path must still run when the machine's existence cannot be determined")
	}
}

// TestNamespaceTeardownReservationCheckedBeforeVacuous: the host-global
// reservation refusal (matchReservation) still runs before anything else,
// including item 3's vacuous-teardown check — a reserved name is refused
// even when this namespace's own bound machine happens to be gone.
func TestNamespaceTeardownReservationCheckedBeforeVacuous(t *testing.T) {
	d := newFakeDocker()
	m := newFakeMachine() // no instances: would otherwise be vacuous
	env, res, _ := nsFixtureWithMachine(t, d, m)
	project := nsValue(t, env.Spec, env, "compose")
	env.Reservations = []Reservation{{Names: []string{project}, Note: "co-resident stack"}}

	err := (&Namespace{}).Teardown(res, project, env)
	if !isRefusal(err) {
		t.Fatalf("the reservation refusal must run before the vacuous-teardown check, got %v", err)
	}
}

// --- item 2: the network-endpoint widening -------------------------------

// TestNamespaceTeardownForceRemovesForeignNetworkEndpoint: a container
// attached to the project's network without the compose-project label is
// force-removed before the network itself, and named in the report's
// notes — never silently.
func TestNamespaceTeardownForceRemovesForeignNetworkEndpoint(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")

	d.networks[dev] = []string{"n-dev"}
	d.networkEndpoints["n-dev"] = []string{"agent-sidecar"}

	err := (&Namespace{}).Teardown(res, dev, env)
	te, ok := err.(*TeardownError)
	if !ok {
		t.Fatalf("Teardown = %v, want a *TeardownError carrying the removal note", err)
	}
	if len(te.Survivors) != 0 {
		t.Fatalf("nothing may survive a successful force-remove: %+v", te.Survivors)
	}
	if !containsStr(*d.removed, "container:agent-sidecar") {
		t.Errorf("removed = %v, want the foreign container force-removed", *d.removed)
	}
	if len(d.networks[dev]) != 0 {
		t.Errorf("the network itself must still be removed: %v", d.networks[dev])
	}
	if len(te.Notes) != 1 || !strings.Contains(te.Notes[0], "agent-sidecar") || !strings.Contains(te.Notes[0], "n-dev") {
		t.Errorf("notes = %v, want the foreign container named", te.Notes)
	}
}

// TestNamespaceTeardownDisconnectsWhenForceRemoveFails: when the foreign
// container cannot be force-removed outright, the driver falls back to
// disconnecting it so the network removal can still proceed, and reports
// that fallback too.
func TestNamespaceTeardownDisconnectsWhenForceRemoveFails(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")

	d.networks[dev] = []string{"n-dev"}
	d.networkEndpoints["n-dev"] = []string{"agent-sidecar"}
	d.failRemove["container:agent-sidecar"] = errors.New("container is running")

	err := (&Namespace{}).Teardown(res, dev, env)
	te, ok := err.(*TeardownError)
	if !ok {
		t.Fatalf("Teardown = %v, want a *TeardownError carrying the disconnect note", err)
	}
	if len(te.Survivors) != 0 {
		t.Fatalf("a successful disconnect must not be reported as a survivor: %+v", te.Survivors)
	}
	if !containsStr(*d.disconnected, "n-dev:agent-sidecar") {
		t.Errorf("disconnected = %v, want the foreign container disconnected", *d.disconnected)
	}
	if len(te.Notes) != 1 || !strings.Contains(te.Notes[0], "agent-sidecar") || !strings.Contains(te.Notes[0], "disconnected") {
		t.Errorf("notes = %v, want the fallback disconnect named", te.Notes)
	}
}

// TestNamespaceTeardownForeignEndpointBothFailSurvives: when both the
// force-remove and the disconnect fallback fail, the container is a
// genuine survivor naming both attempts — this is the one case item 2's
// widening can still leave something behind.
func TestNamespaceTeardownForeignEndpointBothFailSurvives(t *testing.T) {
	d := newFakeDocker()
	_, env, res := nsFixture(t, d, t.TempDir())
	dev := nsValue(t, env.Spec, env, "compose")

	d.networks[dev] = []string{"n-dev"}
	d.networkEndpoints["n-dev"] = []string{"agent-sidecar"}
	d.failRemove["container:agent-sidecar"] = errors.New("container is running")
	d.disconnectErr["n-dev:agent-sidecar"] = errors.New("network disconnect refused")

	err := (&Namespace{}).Teardown(res, dev, env)
	te, ok := err.(*TeardownError)
	if !ok {
		t.Fatalf("Teardown = %v, want a *TeardownError", err)
	}
	var found *Survivor
	for i := range te.Survivors {
		if te.Survivors[i].Name == "agent-sidecar" {
			found = &te.Survivors[i]
		}
	}
	if found == nil {
		t.Fatalf("survivors = %+v, want the foreign container named", te.Survivors)
	}
	if !strings.Contains(found.Reason, "force-removing") || !strings.Contains(found.Reason, "disconnecting") {
		t.Errorf("the survivor's reason must name both failed attempts: %s", found.Reason)
	}
}
