package cli

// fixturecopy_test.go holds the one helper the fixture-copying tests share.
// It carries no build tag on purpose: the copiers live in both the tagged
// acceptance file and the untagged ones, so the helper has to compile in
// both configurations.

import "os"

// writeFixtureFile writes one copied fixture file with a mode this test
// controls rather than one inherited from the checkout.
//
// Propagating the source's permissions verbatim made the copy depend on
// whatever modes the CI checkout happened to leave — a read-only file or a
// directory without the write bit turned into "permission denied" on the
// runner while passing on a developer's machine. Only the executable bit is
// carried across, because a hook script has to stay runnable.
func writeFixtureFile(target string, data []byte, mode os.FileMode) error {
	perm := os.FileMode(0o644)
	if mode.Perm()&0o111 != 0 {
		perm = 0o755
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(target, data, perm)
}
