//go:build !darwin

package platform

// loadLaunchAgent and launchdRunning are darwin's launchd surface. On every
// other platform the call sites are unreachable — the prefix-less
// registration path refuses with ErrNoSupervisor before launchd is ever
// consulted — so these stubs exist only to keep the common file compiling.
func loadLaunchAgent(plistPath string) error { return ErrNoSupervisor }

func launchdRunning() (bool, error) { return false, nil }

// uninstallLaunchAgent refuses off darwin.
func uninstallLaunchAgent(prefix string) (UninstallSupervisorResult, error) {
	return UninstallSupervisorResult{}, ErrNoSupervisor
}
