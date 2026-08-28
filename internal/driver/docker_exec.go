package driver

// docker_exec.go is the real Docker seam: it shells out to the docker
// binary with the caller's environment, so DOCKER_HOST and friends are
// honoured. That environment is the coordinator's, not the user's shell's:
// wtd is started by a supervisor, and launchd's default PATH does not
// include the /usr/local/bin Docker Desktop symlinks its CLI into. The
// binary is therefore resolved through platform.LookHelper, which falls
// back to the known install directories, rather than left to the child's
// own PATH lookup. There is no docker client library — the CLI is the interface
// (PLAN-SCOPE.md, "No docker client library"; shell out to the docker
// binary, and report it as unavailable when it is absent or the daemon is
// unreachable).

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// execDocker runs the docker CLI.
type execDocker struct{}

// NewDocker builds the real runner, used by the coordinator's teardown path
// by default.
func NewDocker() Docker { return execDocker{} }

// Version probes the daemon. An absent binary and an unreachable daemon are
// both ErrUnavailable — the caller reports unavailable and says what fixes
// it.
func (execDocker) Version() error {
	bin, err := dockerBin()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return &ErrUnavailable{Reason: fmt.Sprintf("the docker daemon is unreachable: %s",
			strings.TrimSpace(string(out)))}
	}
	return nil
}

// ListContainers returns the container IDs carrying the project label.
func (d execDocker) ListContainers(project string) ([]string, error) {
	return dockerList(project, "ps", "-a",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.ID}}")
}

// ListNetworks returns the network IDs carrying the project label.
func (d execDocker) ListNetworks(project string) ([]string, error) {
	return dockerList(project, "network", "ls",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.ID}}")
}

// ListNetworksAll returns the name of every network on the daemon.
func (d execDocker) ListNetworksAll() ([]string, error) {
	return dockerList("", "network", "ls", "--format", "{{.Name}}")
}

// NetworkSubnet returns the network's first IPv4 subnet as a CIDR string,
// or "" when the network declares none. Networks without an IPAM subnet
// (macvlan, ipvlan, or a bare user-defined network) cannot overlap
// anything.
func (d execDocker) NetworkSubnet(name string) (string, error) {
	bin, err := dockerBin()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(bin, "network", "inspect",
		"--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", name).Output()
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
	return dockerList(project, "volume", "ls",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Name}}")
}

// dockerList runs a docker list command and splits its lines.
func dockerList(project string, args ...string) ([]string, error) {
	bin, err := dockerBin()
	if err != nil {
		return nil, err
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("listing docker objects for project %q: %w", project, err)
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
	return runDocker(args...)
}

// RemoveNetworks removes the networks by ID.
func (d execDocker) RemoveNetworks(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"network", "rm"}, ids...)
	return runDocker(args...)
}

// RemoveVolumes removes the volumes by name.
func (d execDocker) RemoveVolumes(names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"volume", "rm"}, names...)
	return runDocker(args...)
}

// runDocker runs one docker command and turns a failure into an error
// carrying the CLI's stderr.
func runDocker(args ...string) error {
	bin, err := dockerBin()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerBin resolves the docker CLI to an absolute path. The lookup is not
// cached: a user who installs Docker while the coordinator is resident
// gets it on the next teardown rather than after a wtd restart, and a
// teardown is nowhere near hot enough for a few stat calls to matter.
//
// The refusal names the locations searched, which is the thing that makes
// this diagnosable, and offers both remedies — Docker absent and Docker
// installed somewhere the coordinator cannot see are different problems
// with the same symptom.
func dockerBin() (string, error) {
	bin, err := platform.LookHelper("docker")
	if err != nil {
		return "", &ErrUnavailable{Reason: err.Error() +
			"; install Docker Desktop (or the docker CLI) if it is absent, " +
			"or set " + platform.HelperDirsEnv + " to the directory holding it"}
	}
	return bin, nil
}
