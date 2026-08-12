package driver

// Docker is the docker CLI seam the namespace driver shells out to. The
// real implementation (execDocker in docker_exec.go) runs the docker binary
// with the caller's environment, so DOCKER_HOST and friends are honoured;
// tests install fakes. The namespace driver is the only consumer this phase
// — no docker client library exists anywhere (PLAN-SCOPE.md, "New
// third-party dependencies").
//
// Every method that needs a reachable daemon reports it: an absent binary or
// an unreachable daemon surfaces as ErrUnavailable, which the caller maps to
// an unavailable probe (does not block allocation) or an unavailable teardown
// (does block freeing the slot). Everything else is a genuine failure.
type Docker interface {
	// Version probes the daemon: nil when reachable, ErrUnavailable when
	// the binary is absent or the daemon is unreachable.
	Version() error

	// ListContainers returns the container IDs carrying the project label
	// com.docker.compose.project=<project>.
	ListContainers(project string) ([]string, error)
	// ListNetworks returns the network IDs carrying the project label.
	ListNetworks(project string) ([]string, error)
	// ListVolumes returns the volume names carrying the project label.
	ListVolumes(project string) ([]string, error)

	// ListNetworksAll returns the name of every network on the daemon,
	// regardless of label — the cidr driver's probe input, which asks
	// whether any existing network overlaps a derived slice
	// (03-drivers.md §4.3).
	ListNetworksAll() ([]string, error)
	// NetworkSubnet returns the network's IPv4 subnet as a CIDR string,
	// or "" when the network declares none.
	NetworkSubnet(name string) (string, error)

	// RemoveContainers force-removes the containers by ID.
	RemoveContainers(ids []string) error
	// RemoveNetworks removes the networks by ID.
	RemoveNetworks(ids []string) error
	// RemoveVolumes removes the volumes by name.
	RemoveVolumes(names []string) error
}

// ErrUnavailable is what every docker-touching operation returns when the
// binary is absent or the daemon is unreachable. It is a marker type so
// callers can distinguish "cannot run at all" from "ran and failed".
type ErrUnavailable struct{ Reason string }

func (e *ErrUnavailable) Error() string { return e.Reason }
