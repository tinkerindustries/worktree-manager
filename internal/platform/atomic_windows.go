//go:build windows

package platform

// syncDir is a no-op on Windows, stated as bounded coverage: opening a
// directory handle fails there (the unix directory-fsync pattern has no
// Windows equivalent in the standard library), and the durability the
// unix fsync buys is provided by NTFS journaling plus MoveFileEx with
// MOVEFILE_REPLACE_EXISTING — the rename the standard library's os.Rename
// performs. The store opened on Windows with phase 8's ACL work, which is
// what made this step reachable and this statement necessary
// (08-platform.md §4.6).
func syncDir(dir string) error { return nil }
