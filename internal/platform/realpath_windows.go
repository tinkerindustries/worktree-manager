//go:build windows

package platform

// realpath_windows.go is Windows path realisation (08-platform.md §4.5):
// long paths beyond 260 characters need the opt-in for most tools, and
// .claude/worktrees/<slug> nested inside an already-deep repository path
// reaches it more easily than it looks. Go's own file operations handle
// long paths via the \\?\ prefix, so the coordinator's comparisons must
// too; and UNC paths and mapped drives name the same directory while
// comparing unequal as strings — the same class of problem as
// case-insensitivity, with a different cause.
//
// GetFinalPathNameByHandleW resolves both at once: it returns the final
// path in the \\?\C:\... or \\?\UNC\server\share\... form, so two
// spellings of one directory (a mapped drive and its UNC target, a
// symlink and its target) land on the same string. That is what makes
// containment comparisons sound on Windows.
//
// What is not handled here, stated: the opt-in is for the OTHER programs
// that reach the long path — git, docker, the user's editor. The tool
// cannot fix those; it can only keep its own comparisons correct.

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

// winFILE_READ_ATTRIBUTES is the minimum access for GetFinalPathNameByHandleW.
const winFILE_READ_ATTRIBUTES = 0x0080

// realPath resolves abs to its final form. A path that does not exist yet
// is resolved through its deepest existing ancestor and the missing tail
// re-appended, exactly like the unix resolver.
func realPath(abs string) (string, error) {
	if p, err := finalPath(abs); err == nil {
		return p, nil
	}
	tail := []string{}
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("resolving %s: no existing ancestor", abs)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
		p, err := finalPath(cur)
		if err != nil {
			continue
		}
		for _, seg := range tail {
			p = filepath.Join(p, seg)
		}
		return p, nil
	}
}

// finalPath opens p (a directory too, via FILE_FLAG_BACKUP_SEMANTICS)
// and asks the kernel for its final path — symlinks resolved, drive
// letters and UNC names both reduced to the volume form.
func finalPath(p string) (string, error) {
	pw, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", fmt.Errorf("encoding %s: %w", p, err)
	}
	h, err := syscall.CreateFile(pw,
		winFILE_READ_ATTRIBUTES,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)
	getFinal := syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")
	// First call with a nil buffer: the function returns the required
	// size (0 on any other failure).
	n, _, _ := getFinal.Call(uintptr(h), 0, 0, 0)
	if n == 0 {
		return "", fmt.Errorf("resolving %s: GetFinalPathNameByHandleW failed", p)
	}
	buf := make([]uint16, n+1)
	r1, _, e1 := getFinal.Call(uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n), 0) // flags 0: FILE_NAME_NORMALIZED | VOLUME_NAME_DOS
	if r1 == 0 {
		return "", fmt.Errorf("resolving %s: %w", p, e1)
	}
	return syscall.UTF16ToString(buf), nil
}
