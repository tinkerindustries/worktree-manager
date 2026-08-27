//go:build windows

package platform

import (
	"fmt"
	"syscall"
)

// fileIdentity reports the Windows equivalent of dev, inode and mtime.
//
// Go's os.Stat does not carry them on Windows — Win32FileAttributeData has
// no volume serial and no file index — so the answer comes from
// GetFileInformationByHandle, which reports both: dwVolumeSerialNumber is
// the volume (dev) and the 64-bit nFileIndexHigh:nFileIndexLow pair is the
// file's identity on that volume (inode). FILE_FLAG_BACKUP_SEMANTICS is
// what lets the handle be a directory as well as a file, and the share
// flags are the full set so opening the path never blocks anybody.
//
// A filesystem that reports no file index (a file index of zero, which
// ReFS's 128-bit ids surface as) has no identity to compare, and that is
// an error rather than a zero that would compare equal to another zero:
// the guard cache must never treat two different directories as the same
// one.
func fileIdentity(path string) (dev, ino uint64, mtime int64, err error) {
	pw, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("encoding %s: %w", path, err)
	}
	h, err := syscall.CreateFile(pw,
		winFILE_READ_ATTRIBUTES,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, 0, 0, err
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return 0, 0, 0, fmt.Errorf("file identity of %s: %w", path, err)
	}
	ino = uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	if ino == 0 {
		return 0, 0, 0, fmt.Errorf("file identity of %s: the filesystem reports no file index", path)
	}
	return uint64(info.VolumeSerialNumber), ino, info.LastWriteTime.Nanoseconds(), nil
}
