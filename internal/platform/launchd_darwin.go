//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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
//
// The wait between the two is not optional. `launchctl bootout` returns
// before launchd has finished tearing the service down, so a bootstrap
// issued immediately after it finds the label still present and fails with
// "Bootstrap failed: 5: Input/output error" — launchd's way of saying
// already loaded. Every second `wt daemon install` failed that way, and it
// left the coordinator booted out but not back in: stopped, with an error
// blaming the registration path. So bootout is followed by a poll until the
// label is genuinely gone.
func loadLaunchAgent(plistPath string) error {
	exec.Command("launchctl", "bootout", launchdDomain()+"/"+LaunchAgentLabel).Run()
	waitForLaunchdUnload()
	if err := launchctl("bootstrap", launchdDomain(), plistPath); err != nil {
		return fmt.Errorf("%w — if it reports \"Bootstrap failed: 5\", the agent is still loaded: run 'launchctl bootout %s/%s', then re-run",
			err, launchdDomain(), LaunchAgentLabel)
	}
	return launchctl("kickstart", launchdDomain()+"/"+LaunchAgentLabel)
}

// launchdUnloadTimeout bounds the wait for a bootout to take effect. A
// teardown normally completes in well under a second; the bound exists so a
// wedged service reports a bootstrap failure naming the remedy rather than
// hanging an install indefinitely.
const launchdUnloadTimeout = 5 * time.Second

// waitForLaunchdUnload polls until launchd no longer knows the label, or
// the timeout expires. Expiry is not an error here: the bootstrap that
// follows produces the real diagnosis, and it names the remedy.
func waitForLaunchdUnload() {
	deadline := time.Now().Add(launchdUnloadTimeout)
	for time.Now().Before(deadline) {
		if !launchdKnowsLabel() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// launchdKnowsLabel reports whether launchd still has the label in the
// user's GUI domain, loaded in any state. `launchctl print` failing is the
// signal that it is gone.
func launchdKnowsLabel() bool {
	return exec.Command("launchctl", "print", launchdDomain()+"/"+LaunchAgentLabel).Run() == nil
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
