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

	// NetworkEndpoints returns the names of every container still attached
	// to a network — item 2's widening of the label rail. A compose
	// teardown only ever discovers objects carrying the project's own
	// label, but a container the application attached to that network
	// through the Docker API at runtime carries no such label and still
	// blocks the network's removal ("network ... has active endpoints").
	// Anything this returns joined the project's own network, which is
	// inside the worktree's blast radius even when it is outside the
	// project label.
	NetworkEndpoints(network string) ([]string, error)
	// DisconnectContainer force-disconnects one container from one
	// network, the fallback teardownProject reaches for when force-
	// removing the container outright fails — disconnecting still lets
	// the network removal proceed, which is the only thing the caller
	// actually needs.
	DisconnectContainer(network, container string) error

	// WithHost returns a copy of this seam bound to a specific docker
	// endpoint (a DOCKER_HOST value), overriding whatever DOCKER_HOST or
	// docker context the ambient environment carries. It exists because
	// the namespace driver's default — the coordinator's own ambient
	// environment — is only ever right for one VM at a time: colima start
	// sets the host's docker context as a side effect, so with two
	// worktrees' machines running, the ambient daemon addresses whichever
	// one was started most recently, and a namespace bound to the other
	// one's machine (item 4) must never run its calls there. An empty
	// endpoint means "use the ambient environment", exactly like a seam
	// that was never bound at all — the namespace driver relies on this to
	// fall back for a resource that declares no machine: binding, and for
	// a platform whose runner has no separate endpoint to give.
	WithHost(endpoint string) Docker
}

// ErrUnavailable is what every docker-touching operation returns when the
// binary is absent or the daemon is unreachable. It is a marker type so
// callers can distinguish "cannot run at all" from "ran and failed".
type ErrUnavailable struct{ Reason string }

func (e *ErrUnavailable) Error() string { return e.Reason }
