package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWrite is the repository's one atomic write path: temp file in the
// same directory, fsync the file, rename, fsync the directory. Same
// directory matters because a rename across filesystems is not atomic
// (02-coordination.md §9, 11.1).
//
// It was born as the store's write path (phase 3, internal/store) and moved
// here in phase 5 because the client-side emitters — the descriptor and the
// .env managed block — owe the same guarantee and a client never imports
// the coordinator's store. `store.AtomicWrite` is now a one-line delegate,
// so the sequence exists once and the "same directory, fsync, rename"
// invariant cannot drift between the two halves.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// fsync the directory so the rename itself is durable. On Windows this
	// fails — the Windows store refuses to open in the first place, so the
	// write path is never reached there (08-platform.md §4.6).
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		return fmt.Errorf("writing %s: fsyncing %s: %w", path, dir, err)
	}
	return nil
}
