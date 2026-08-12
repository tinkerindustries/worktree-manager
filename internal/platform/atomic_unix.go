//go:build darwin || linux

package platform

import "os"

// syncDir fsyncs a directory — the unix half of AtomicWrite's
// rename-durability sequence.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
