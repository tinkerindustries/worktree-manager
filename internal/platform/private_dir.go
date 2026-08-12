package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsurePrivateDir creates dir (and its parents) restricted to the owning
// user, and probes that it is actually writable — the store's permission
// model: 0700 directory and 0600 files on unix, an ACL granting the
// current user and denying everyone else on Windows, where mode bits are
// meaningless (docs/ARCHITECTURE.md §12.2, 08-platform.md §3 "File
// permissions"). On Windows, where the ACL cannot be set the coordinator
// refuses to write credentials rather than writing them world-readable
// (08-platform.md §4.6, the refusal path phase 2 wrote and phase 8 made
// real).
//
// The probe exists so an unwritable store root produces a clear error
// naming the path and the ownership problem at open time, not a rename
// failure at the end of a long operation (02-coordination.md §14).
func EnsurePrivateDir(dir string) error {
	return ensurePrivateDir(dir)
}

// probePrivateDir is the shared writability probe: one file created and
// removed, so a store root that exists but cannot be written produces a
// clear error at open time.
func probePrivateDir(dir string) error {
	probe := filepath.Join(dir, ".wt-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf(
			"store root %s is not writable by the coordinator: %w — the directory exists but the user running wtd cannot write it; check its owner and mode (it should be owned by you, 0700)",
			dir, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(probe)
		return fmt.Errorf("store root %s: %w", dir, err)
	}
	os.Remove(probe)
	return nil
}
