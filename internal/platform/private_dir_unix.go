//go:build darwin || linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// probePrivateDir is the shared writability probe (private_dir.go).

// ensurePrivateDir creates the 0700 store root and probes it writable.
// MkdirAll succeeds when the directory already exists, even owned by
// someone else — the probe is what catches that. Running as root in a
// container makes the mode bit moot, but the coordinator runs as the
// user on the host, where this is the real check.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf(
			"store root %s cannot be created: %w — the coordinator writes the store as the user who started it; check that %s is owned by you and is on a writable filesystem",
			dir, err, filepath.Dir(dir))
	}
	return probePrivateDir(dir)
}
