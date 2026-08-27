//go:build windows

package platform

import "strings"

// externalPath converts a realised Windows path back to the DOS spelling.
//
// realPath returns the extended-length form GetFinalPathNameByHandleW
// produces — \\?\C:\dir or \\?\UNC\server\share\dir — and that is
// deliberate (realpath_windows.go): it is what makes containment
// comparisons sound against a long path, a mapped drive and a UNC name.
// It is also a spelling most programs do not accept. git in particular
// normalises backslashes to forward slashes before opening a path, which
// turns \\?\C:\dir into //?/C:/dir and fails with "Invalid argument";
// `git worktree remove \\?\C:\...` answers "is not a working tree".
//
// So the extended form stays inside the process and this is the one
// conversion out of it: \\?\UNC\server\share becomes \\server\share and
// \\?\C:\dir becomes C:\dir. Anything without the prefix is returned
// unchanged, which makes the function safe to apply to a whole argument
// list.
func externalPath(p string) string {
	if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	if rest, ok := strings.CutPrefix(p, `\\?\`); ok {
		return rest
	}
	return p
}
