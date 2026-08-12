package driver

// namespace_test.go exercises the namespace driver against a fake docker
// seam: probe by label, teardown by label across the project and its
// dependents, the reserved-name refusal, teardown continuing past a failure,
// and the pinned-name finding. The docker-tagged twins against the real
// daemon live in acceptance_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// fakeDocker is the test seam: objects keyed by project, optional failures
// per object, and a log of what was removed in what order.
type fakeDocker struct {
	versionErr error
	containers map[string][]string // project → container ids
	networks   map[string][]string
	volumes    map[string][]string
	// allNetworks maps every network name to its subnet, the cidr
	// driver's probe input (ListNetworksAll / NetworkSubnet).
	allNetworks map[string]string
	failRemove  map[string]error // "container:<id>", "network:<id>", "volume:<name>" → error
	removed     []string
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers:  map[string][]string{},
		networks:    map[string][]string{},
		volumes:     map[string][]string{},
		allNetworks: map[string]string{},
		failRemove:  map[string]error{},
	}
}

func (f *fakeDocker) Version() error { return f.versionErr }

func (f *fakeDocker) ListContainers(p string) ([]string, error) { return f.containers[p], nil }
func (f *fakeDocker) ListNetworks(p string) ([]string, error)   { return f.networks[p], nil }
func (f *fakeDocker) ListVolumes(p string) ([]string, error)    { return f.volumes[p], nil }

func (f *fakeDocker) ListNetworksAll() ([]string, error) {
	names := make([]string, 0, len(f.allNetworks))
	for name := range f.allNetworks {
		names = append(names, name)
	}
	return names, nil
}
func (f *fakeDocker) NetworkSubnet(name string) (string, error) { return f.allNetworks[name], nil }

func (f *fakeDocker) RemoveContainers(ids []string) error {
	return f.remove("container", ids, f.containers)
}
func (f *fakeDocker) RemoveNetworks(ids []string) error {
	return f.remove("network", ids, f.networks)
}
func (f *fakeDocker) RemoveVolumes(names []string) error {
	return f.remove("volume", names, f.volumes)
}

// remove models docker's batch semantics: objects are removed in order until
// the first failure, which fails the whole call.
func (f *fakeDocker) remove(kind string, ids []string, byProject map[string][]string) error {
	for _, id := range ids {
		if err := f.failRemove[kind+":"+id]; err != nil {
			return err
		}
		f.removed = append(f.removed, kind+":"+id)
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
	return s, env, resourceByName(s, "compose")
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
	if strings.Join(d.removed, ",") != strings.Join(want, ",") {
		t.Errorf("removal order = %v, want %v", d.removed, want)
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
	if len(d.removed) != 0 {
		t.Errorf("nothing may be attempted while the daemon is unreachable: %v", d.removed)
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
