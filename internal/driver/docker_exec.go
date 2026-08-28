package driver

// docker_exec.go is the real Docker seam: it shells out to the docker
// binary with the caller's environment, so DOCKER_HOST and friends are
// honoured. That environment is the coordinator's, not the user's shell's:
// wtd is started by a supervisor, and launchd's default PATH does not
// include the /usr/local/bin Docker Desktop symlinks its CLI into. The
// binary is therefore resolved through platform.HelperCommand, which falls
// back to the known install directories and hands the child a PATH that
// leads with the directory docker was found in, rather than left to the
// child's own PATH lookup. There is no docker client library — the CLI is the interface
// (PLAN-SCOPE.md, "No docker client library"; shell out to the docker
// binary, and report it as unavailable when it is absent or the daemon is
// unreachable).
//
// execDocker carries an optional host: a DOCKER_HOST value that, when set,
// is appended to every child's environment, overriding whatever DOCKER_HOST
// or docker context the coordinator's own ambient environment carries
// (os/exec keeps the last of a duplicate key, the same rule
// platform.HelperCommand already leans on for PATH). WithHost is how the
// namespace driver reaches a bound machine's own daemon instead of the
// ambient one (item 4); the zero value's empty host is the seam every
// caller had before that existed, and every caller that never binds a
// machine still gets exactly that.

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// execDocker runs the docker CLI, optionally against a specific endpoint.
type execDocker struct{ host string }

// NewDocker builds the real runner bound to the ambient environment, used
// by the coordinator's teardown path by default.
func NewDocker() Docker { return execDocker{} }

// WithHost returns a copy bound to endpoint. An empty endpoint is the same
// as never calling this: the ambient environment decides.
func (d execDocker) WithHost(endpoint string) Docker {
	return execDocker{host: endpoint}
}

// Version probes the daemon. An absent binary and an unreachable daemon are
// both ErrUnavailable — the caller reports unavailable and says what fixes
// it.
func (d execDocker) Version() error {
	cmd, err := d.dockerCmd("version", "--format", "{{.Server.Version}}")
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &ErrUnavailable{Reason: fmt.Sprintf("the docker daemon is unreachable: %s",
			strings.TrimSpace(string(out)))}
	}
	return nil
}

// ListContainers returns the container IDs carrying the project label.
func (d execDocker) ListContainers(project string) ([]string, error) {
	return d.dockerList(project, "ps", "-a",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.ID}}")
}

// ListNetworks returns the network IDs carrying the project label.
func (d execDocker) ListNetworks(project string) ([]string, error) {
	return d.dockerList(project, "network", "ls",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.ID}}")
}

// ListNetworksAll returns the name of every network on the daemon.
func (d execDocker) ListNetworksAll() ([]string, error) {
	return d.dockerList("", "network", "ls", "--format", "{{.Name}}")
}

// NetworkSubnet returns the network's first IPv4 subnet as a CIDR string,
// or "" when the network declares none. Networks without an IPAM subnet
// (macvlan, ipvlan, or a bare user-defined network) cannot overlap
// anything.
func (d execDocker) NetworkSubnet(name string) (string, error) {
	cmd, err := d.dockerCmd("network", "inspect",
		"--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", name)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("inspecting network %q: %w", name, err)
	}
	for _, subnet := range strings.Fields(string(out)) {
		return subnet, nil
	}
	return "", nil
}

// ListVolumes returns the volume names carrying the project label.
func (d execDocker) ListVolumes(project string) ([]string, error) {
	return d.dockerList(project, "volume", "ls",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Name}}")
}

// NetworkEndpoints returns the names of every container attached to
// network, whatever labels it carries — the label-only listings above
// cannot see a container the application attached at runtime through the
// Docker API rather than through compose.
func (d execDocker) NetworkEndpoints(network string) ([]string, error) {
	cmd, err := d.dockerCmd("network", "inspect",
		"--format", "{{range .Containers}}{{.Name}}{{\"\\n\"}}{{end}}", network)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspecting the endpoints of network %q: %w", network, err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// DisconnectContainer force-disconnects container from network.
func (d execDocker) DisconnectContainer(network, container string) error {
	return d.runDocker("network", "disconnect", "-f", network, container)
}

// dockerList runs a docker list command and splits its lines.
func (d execDocker) dockerList(project string, args ...string) ([]string, error) {
	cmd, err := d.dockerCmd(args...)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, platform.HelperError(fmt.Sprintf("listing docker objects for project %q", project), err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

// RemoveContainers force-removes the containers by ID.
func (d execDocker) RemoveContainers(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"rm", "-f"}, ids...)
	return d.runDocker(args...)
}

// RemoveNetworks removes the networks by ID.
func (d execDocker) RemoveNetworks(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"network", "rm"}, ids...)
	return d.runDocker(args...)
}

// RemoveVolumes removes the volumes by name.
func (d execDocker) RemoveVolumes(names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"volume", "rm"}, names...)
	return d.runDocker(args...)
}

// runDocker runs one docker command and turns a failure into an error
// carrying the CLI's stderr.
func (d execDocker) runDocker(args ...string) error {
	cmd, err := d.dockerCmd(args...)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerCmd resolves the docker CLI and builds the command to run it. The
// lookup is not cached: a user who installs Docker while the coordinator is
// resident gets it on the next teardown rather than after a wtd restart,
// and a teardown is nowhere near hot enough for a few stat calls to matter.
//
// platform.HelperCommand supplies the child's environment as well as the
// path. The CLI resolves docker-credential-<store> through its own PATH at
// runtime, and Docker Desktop installs both into the same /usr/local/bin
// that launchd's PATH omits. No command below reaches a registry, so that
// helper is not consulted today; running the CLI with a PATH it can work
// in is still the coordinator's job rather than the next command author's.
//
// d.host, when set, is appended as DOCKER_HOST after HelperCommand's own
// environment: os/exec keeps the last of a duplicate key, so this
// overrides whatever DOCKER_HOST the coordinator's ambient environment (or
// the currently-active docker context, which docker reads only when
// DOCKER_HOST is unset) would otherwise have supplied. This is the whole
// of item 4's fix — nothing about the command construction above needs to
// change, because the daemon a command reaches is entirely a function of
// this one variable.
//
// The refusal names the locations searched, which is the thing that makes
// this diagnosable, and offers both remedies — Docker absent and Docker
// installed somewhere the coordinator cannot see are different problems
// with the same symptom.
func (d execDocker) dockerCmd(args ...string) (*exec.Cmd, error) {
	cmd, err := platform.HelperCommand("docker", args...)
	if err != nil {
		return nil, &ErrUnavailable{Reason: err.Error() +
			"; install Docker Desktop (or the docker CLI) if it is absent, " +
			"or set " + platform.HelperDirsEnv + " to the directory holding it"}
	}
	if d.host != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+d.host)
	}
	return cmd, nil
}
