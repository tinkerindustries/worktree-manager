package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// EnsurePrivateDir creates dir (and its parents) restricted to the owning
// user, and probes that it is actually writable — the store's permission
// model, 0700 directory and 0600 files (docs/ARCHITECTURE.md §12.2,
// 08-platform.md §3 "File permissions"). The probe exists so an unwritable
// store root produces a clear error naming the path and the ownership
// problem at open time, not a rename failure at the end of a long operation
// (02-coordination.md §14).
//
// Windows sets the equivalent ACL; where that cannot be done — and with only
// the standard library it cannot — the coordinator refuses to write
// credentials rather than writing them world-readable (08-platform.md §4.6).
// The Windows refusal path is compiled but not verified in this phase.
func EnsurePrivateDir(dir string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf(
			"store root %s: Windows restricts private state with an ACL, and this build cannot set one (08-platform.md §4.6); refusing to write credentials world-readable — the Windows ACL path is phase 8",
			dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf(
			"store root %s cannot be created: %w — the coordinator writes the store as the user who started it; check that %s is owned by you and is on a writable filesystem",
			dir, err, filepath.Dir(dir))
	}
	// MkdirAll succeeds when the directory already exists, even owned by
	// someone else — the probe is what catches that. Running as root in a
	// container makes the mode bit moot, but the coordinator runs as the
	// user on the host, where this is the real check.
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
