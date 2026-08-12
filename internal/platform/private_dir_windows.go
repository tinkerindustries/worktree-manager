//go:build windows

package platform

// private_dir_windows.go is the Windows half of the store's permission
// model (08-platform.md §4.6): mode bits are meaningless on NTFS, so the
// private state directory is protected by an ACL granting the current
// user and denying everyone else — and where that cannot be done, the
// coordinator refuses rather than writing credentials world-readable.
// The refusal is the phase-2 contract made real: phase 2 wrote the
// refusal path, this phase makes it the outcome of an actual ACL attempt
// and its read-back verification.

import (
	"fmt"
	"os"
	"path/filepath"
)

// ensurePrivateDirWindows creates the store root and sets its private
// ACL. The order matters: MkdirAll first (with a plain directory), then
// the ACL, then the writability probe — a store root whose ACL cannot be
// set refuses here, before any credential is written anywhere.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf(
			"store root %s cannot be created: %w — the coordinator writes the store as the user who started it; check that %s is owned by you and is on a writable filesystem",
			dir, err, filepath.Dir(dir))
	}
	if err := windowsSetACL(dir); err != nil {
		return fmt.Errorf(
			"store root %s: %v — refusing to write credentials world-readable; the ACL that grants you and denies everyone else could not be set (08-platform.md §4.6)",
			dir, err)
	}
	return probePrivateDir(dir)
}

// windowsSetACL is the seam the ACL-refusal test drives: the real
// implementation is setPrivateACL (sec_windows.go), which applies the
// current-user-only ACL and verifies the read-back. A test overrides the
// seam to prove the refusal path without needing a filesystem that
// actually refuses ACLs.
var windowsSetACL = func(path string) error { return setPrivateACL(path) }
