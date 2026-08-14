//go:build windows

package platform

// private_mode_windows.go is the Windows half of the store database's
// permission model: there is no umask on Windows and mode bits are
// meaningless on NTFS, so the privacy of wt.db and its WAL and SHM
// sidecars comes from the current-user ACL that EnsurePrivateDir set on
// the store root — files created inside the directory inherit it, and
// where the ACL cannot be set EnsurePrivateDir already refused to hold
// credentials at all (08-platform.md §4.6). Both halves are no-ops here;
// the refusal path is EnsurePrivateDir's own.

// WithPrivateUmask runs fn unchanged: no umask exists on Windows, and the
// store's privacy is the store root's ACL, which files created inside
// inherit.
func WithPrivateUmask(fn func() error) error { return fn() }

// VerifyPrivateFileModes checks nothing on Windows: mode bits are
// meaningless on NTFS and the current-user ACL on the store root covers
// every file created inside it.
func VerifyPrivateFileModes(...string) error { return nil }
