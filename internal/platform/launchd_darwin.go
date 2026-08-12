//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// launchdDomain is the per-user GUI domain the agent runs in — LaunchAgents
// in ~/Library/LaunchAgents belong to the user's GUI session.
func launchdDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// loadLaunchAgent registers the plist with launchd and starts the agent.
// bootout runs first so re-installing is idempotent; a service that is not
// loaded makes bootout fail, which is expected and ignored.
func loadLaunchAgent(plistPath string) error {
	exec.Command("launchctl", "bootout", launchdDomain()+"/"+LaunchAgentLabel).Run()
	if err := launchctl("bootstrap", launchdDomain(), plistPath); err != nil {
		return err
	}
	return launchctl("kickstart", launchdDomain()+"/"+LaunchAgentLabel)
}

// launchdRunning asks launchd whether the agent is running, independent of
// whether the socket answers — the observation daemon status needs in order
// to distinguish "registered but stopped" from "running but unreachable".
func launchdRunning() (bool, error) {
	out, err := exec.Command("launchctl", "print", launchdDomain()+"/"+LaunchAgentLabel).Output()
	if err != nil {
		return false, nil // not loaded, or not in this domain: not running
	}
	return strings.Contains(string(out), "state = running"), nil
}
