package driver

// docker_exec.go is the real Docker seam: it shells out to the docker
// binary with the caller's environment, so DOCKER_HOST and friends are
// honoured. There is no docker client library — the CLI is the interface
// (PLAN-SCOPE.md, "No docker client library"; shell out to the docker
// binary, and report it as unavailable when it is absent or the daemon is
// unreachable).

import (
	"fmt"
	"os/exec"
	"strings"
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
	if _, err := exec.LookPath("docker"); err != nil {
		return &ErrUnavailable{Reason: "docker is not installed or not on PATH; install Docker Desktop (or the docker CLI), then re-run"}
	}
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
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

// ListVolumes returns the volume names carrying the project label.
func (d execDocker) ListVolumes(project string) ([]string, error) {
	return dockerList(project, "volume", "ls",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Name}}")
}

// dockerList runs a docker list command and splits its lines.
func dockerList(project string, args ...string) ([]string, error) {
	out, err := exec.Command("docker", args...).Output()
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
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}
