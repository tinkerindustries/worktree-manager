package driver

// namespace.go is the namespace driver: a derived name, compose-specific
// behaviour for kind: compose (docker objects found and destroyed by their
// project label) and a plain string for kind: plain. The rails of
// 03-drivers.md §4.2 bind it: a hit on the project label means a previous
// teardown was incomplete, not that the slot is taken; teardown is by
// label, never by reading a compose file — the coordinator never creates a
// container, so it holds no handle from creation and has to find the
// objects again by asking the daemon for everything carrying
// com.docker.compose.project=<name> (the discover-rather-than-create rule,
// ARCHITECTURE.md §7.1).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// Namespace is the namespace driver.
type Namespace struct{}

// Type reports the resource type.
func (*Namespace) Type() string { return "namespace" }

// HasApply reports that the application's own compose run creates the
// containers; the driver never does.
func (*Namespace) HasApply() bool { return false }

// HasTeardown reports that the driver destroys the project's objects.
func (*Namespace) HasTeardown() bool { return true }

// GatesAllocation reports that a namespace hit does not skip a candidate
// slot: a hit means a previous teardown was incomplete, not that the slot
// is taken (03-drivers.md §4.2).
func (*Namespace) GatesAllocation() bool { return false }

// Derive returns the resolved project name, exactly as spec.Resolve
// computes it.
func (*Namespace) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	v, ok := table[r.Name]
	if !ok {
		return nil, fmt.Errorf("namespace driver: no resolved value for resource %q", r.Name)
	}
	return v.Value, nil
}

// Probe reports, for kind: compose, whether any object already carries the
// project label. A hit is a diagnostic — a previous teardown was incomplete
// — never a claim on the slot. Without a reachable daemon the probe is
// unavailable, which does not block allocation.
func (*Namespace) Probe(r *spec.Resource, value any, env Env) ProbeResult {
	if spec.NamespaceKind(r) != "compose" {
		// plain: a string the application uses however it likes; nothing
		// external holds it.
		return ProbeFree
	}
	project, ok := value.(string)
	if !ok || project == "" {
		return ProbeUnavailable
	}
	if env.Docker == nil {
		return ProbeUnavailable
	}
	if err := env.Docker.Version(); err != nil {
		return ProbeUnavailable
	}
	objs, err := projectObjects(env.Docker, project)
	if err != nil {
		return ProbeUnavailable
	}
	if objs > 0 {
		return ProbeHeld
	}
	return ProbeFree
}

// Teardown destroys the project's objects by label: containers, then
// networks, then volumes, for the project and every dependent project the
// spec declares (B2.2, B3.2) — the test stack's objects go with the dev
// stack's. The handle is the resolved project name from the registry, never
// anything read from the worktree (03-drivers.md §2.2): the directory is
// routinely gone by the time teardown runs.
//
// Two rails are structural. A resolved project name matching a host-global
// reservation is refused rather than torn down, naming the reservation —
// label-based teardown is the one operation that could otherwise reach a
// co-resident production stack the tool knows nothing else about (B8.2).
// And teardown continues past a failure, collecting what survived: stopping
// early would leave more behind than continuing does (03-drivers.md §5).
func (*Namespace) Teardown(r *spec.Resource, value any, env Env) error {
	if spec.NamespaceKind(r) != "compose" {
		return nil // plain: nothing the tool created, nothing it destroys
	}
	project, ok := value.(string)
	if !ok || project == "" {
		return fmt.Errorf("namespace driver: teardown of resource %q: value %#v is not a project name", r.Name, value)
	}
	if res := matchReservation(project, env.Reservations); res != nil {
		held := "ports " + strings.Join(res.PortsStrings(), ", ")
		if len(res.Ports) == 0 {
			held = "the reserved name"
		}
		return &RefusalError{Reason: fmt.Sprintf(
			"refusing to tear down compose project %q: it matches the host-global reservation %q (%s) — label-based teardown must never reach a co-resident stack the tool knows nothing else about",
			project, res.Note, held)}
	}
	if env.Docker == nil {
		return &ErrUnavailable{Reason: "the docker seam is not installed; the compose project cannot be torn down"}
	}
	if err := env.Docker.Version(); err != nil {
		return err // ErrUnavailable: nothing was attempted, so nothing survived to report
	}

	var survivors []Survivor
	for _, p := range nsProjectOrder(r, value, env) {
		survivors = append(survivors, teardownProject(env.Docker, p, r.Name)...)
	}
	if len(survivors) > 0 {
		return &TeardownError{Resource: r.Name, Survivors: survivors}
	}
	return nil
}

// teardownProject removes one project's objects in container, network,
// volume order, continuing past a failure and collecting what survived.
func teardownProject(d Docker, project, resource string) []Survivor {
	var survivors []Survivor

	ids, err := d.ListContainers(project)
	if err != nil {
		survivors = append(survivors, Survivor{Kind: "containers", Name: project, Resource: resource,
			Reason: fmt.Sprintf("listing them failed: %v", err)})
	} else if len(ids) > 0 {
		if err := d.RemoveContainers(ids); err != nil {
			survivors = append(survivors, Survivor{Kind: "container", Name: strings.Join(ids, " "), Resource: resource,
				Reason: fmt.Sprintf("removing them failed: %v", err)})
		}
	}

	ids, err = d.ListNetworks(project)
	if err != nil {
		survivors = append(survivors, Survivor{Kind: "networks", Name: project, Resource: resource,
			Reason: fmt.Sprintf("listing them failed: %v", err)})
	} else if len(ids) > 0 {
		if err := d.RemoveNetworks(ids); err != nil {
			survivors = append(survivors, Survivor{Kind: "network", Name: strings.Join(ids, " "), Resource: resource,
				Reason: fmt.Sprintf("removing them failed: %v", err)})
		}
	}

	names, err := d.ListVolumes(project)
	if err != nil {
		survivors = append(survivors, Survivor{Kind: "volumes", Name: project, Resource: resource,
			Reason: fmt.Sprintf("listing them failed: %v", err)})
	} else if len(names) > 0 {
		if err := d.RemoveVolumes(names); err != nil {
			survivors = append(survivors, Survivor{Kind: "volume", Name: strings.Join(names, " "), Resource: resource,
				Reason: fmt.Sprintf("removing them failed: %v", err)})
		}
	}
	return survivors
}

// nsProjectOrder returns the project and every dependent project the spec
// declares, dependents first: a namespace resource whose template
// references this resource's name is a dependent (03-drivers.md §3.3 —
// compose_test derives from compose), and a dependent's objects must leave
// before the parent's network does. Values come from the denormalised
// table, never from the worktree; the root project's value is the caller's
// own handle.
func nsProjectOrder(r *spec.Resource, value any, env Env) []string {
	if env.Spec == nil {
		return []string{fmt.Sprint(value)}
	}
	var order []string
	seen := make(map[string]bool)
	var visit func(name string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		// Dependents first: a namespace whose template references this one.
		for i := range env.Spec.Resources {
			dep := &env.Spec.Resources[i]
			if dep.Type != "namespace" || dep.Template == nil {
				continue
			}
			if templateReferences(*dep.Template, name) {
				visit(dep.Name)
			}
		}
		if v := resolvedValue(env, name); v != "" {
			order = append(order, v)
		}
	}
	visit(r.Name)
	if len(order) == 0 {
		return []string{fmt.Sprint(value)}
	}
	// The root's value is the caller's own handle — authoritative even when
	// the table lacks a row for it.
	order[len(order)-1] = fmt.Sprint(value)
	return order
}

// resolvedValue returns the resolved value of a resource from the
// denormalised table, or the empty string when the table lacks it.
func resolvedValue(env Env, name string) string {
	if v, ok := env.Resolved[name]; ok {
		if s, ok := v.Value.(string); ok {
			return s
		}
	}
	return ""
}

// templateReferences reports whether the template references the named
// resource.
func templateReferences(t, name string) bool {
	for _, v := range templateVarsOf(t) {
		if v == name {
			return true
		}
	}
	return false
}

// templateVarsOf extracts the {var} references of a template. It is the
// driver's own read-only scan — the spec package's evaluator is not
// exported, and this is a syntactic scan of the same rule.
func templateVarsOf(t string) []string {
	var names []string
	for i := 0; i < len(t); {
		if t[i] != '{' {
			i++
			continue
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			return names
		}
		names = append(names, t[i+1:i+j])
		i += j + 1
	}
	return names
}

// projectObjects lists every object carrying the project label.
func projectObjects(d Docker, project string) (int, error) {
	containers, err := d.ListContainers(project)
	if err != nil {
		return 0, err
	}
	networks, err := d.ListNetworks(project)
	if err != nil {
		return 0, err
	}
	volumes, err := d.ListVolumes(project)
	if err != nil {
		return 0, err
	}
	return len(containers) + len(networks) + len(volumes), nil
}

// Apply is the optional half's absent side: the application's own compose
// run creates the containers — the tool never does, which is exactly why
// teardown has to discover by label (ARCHITECTURE.md §7.1).
func (*Namespace) Apply(*spec.Resource, any, Env) (ApplyResult, error) { return ApplyResult{}, nil }

// Verify reports a compose file that pins name: — the finding that catches
// the silent-attach failure (D2). A compose file that pins name: makes the
// second worktree attach to the first one's containers, which is worse than
// a port collision precisely because nothing fails; where one is present
// the invocation must pass -p explicitly, which beats the file's name:
// (B3.1). Verify is the one operation permitted to read inside the
// worktree, because it changes nothing and may report "cannot check from
// here" (03-drivers.md §8).
func (*Namespace) Verify(r *spec.Resource, value any, env Env) ([]Finding, error) {
	if spec.NamespaceKind(r) != "compose" {
		return nil, nil
	}
	var findings []Finding
	for _, file := range r.Files {
		p := filepath.Join(env.Worktree, file)
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				findings = append(findings, Finding{Resource: r.Name, Kind: "compose-file-absent", Level: LevelWarning,
					Message: fmt.Sprintf("compose file %s cannot be checked from here: it is not present in the worktree (the directory may be gone)", file)})
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		name, perr := pinnedComposeName(data)
		if perr != nil {
			findings = append(findings, Finding{Resource: r.Name, Kind: "compose-file-unparseable", Level: LevelWarning,
				Message: fmt.Sprintf("compose file %s could not be parsed: %v", file, perr)})
			continue
		}
		if name != "" {
			findings = append(findings, Finding{Resource: r.Name, Kind: "compose-name-pinned", Level: LevelError,
				Message: fmt.Sprintf("compose file %s pins name: %q — a second worktree would silently attach to the first one's containers (D2); pass -p %s explicitly, which beats the file's name:", file, name, value)})
		}
	}
	return findings, nil
}

// pinnedComposeName reads the top-level name: key of a compose file. Compose
// is infrastructure the tool must understand in order to tear down by label
// at all; reading one key of a governed file is not repo domain knowledge
// (03-drivers.md §4.2).
func pinnedComposeName(data []byte) (string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	name, _ := doc["name"].(string)
	return name, nil
}

// BlastRadius is the shared-block prose for a namespace resource.
func (*Namespace) BlastRadius(r *spec.Resource, s *spec.Spec) string {
	return "the compose project name is shared: every worktree attaches to the same containers, volumes and networks, and the second worktree silently joins the first one's stack"
}

// matchReservation returns the host-global reservation whose reserved name
// equals the project, or nil. The rail is on names: the ledger's reserved
// names are the compose project names a person declared as co-resident
// production, and label-based teardown must never reach them
// (03-drivers.md §4.2, B8.2).
func matchReservation(project string, reservations []Reservation) *Reservation {
	for i := range reservations {
		for _, n := range reservations[i].Names {
			if n == project {
				return &reservations[i]
			}
		}
	}
	return nil
}

// isErrUnavailable reports whether err is an ErrUnavailable.
func isErrUnavailable(err error) bool {
	var ue *ErrUnavailable
	return errors.As(err, &ue)
}

// isTeardownError reports whether err is a TeardownError.
func isTeardownError(err error) bool {
	var te *TeardownError
	return errors.As(err, &te)
}
