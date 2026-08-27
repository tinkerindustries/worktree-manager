// Package platform owns every behaviour that depends on the operating system
// or the filesystem underneath it — the enumerated surface of
// docs/design/08-platform.md §3, client half (M8b: path realisation and the
// mount's case sensitivity). It is the only package in this repository
// permitted to branch on GOOS; a caller never writes `if darwin`
// (08-platform.md §2).
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// RealPath returns the absolute, symlink-resolved form of path.
//
// Both rails of 01-identity.md §3.1 depend on it. On macOS the resolution is
// not optional: /tmp is a symlink to /private/tmp, and paths under /Users can
// arrive via /System/Volumes/Data, so an unresolved comparison reports a path
// outside the tree that is in fact inside it.
//
// A path that does not exist yet (a write to a new file) is resolved through
// its deepest existing ancestor and the missing tail re-appended, so a not-
// yet-created file inside the tree resolves to the same form as the tree
// itself. Without this the containment test could never approve a first
// write.
//
// The per-OS resolver is realPath (realpath_unix.go for symlink resolution,
// realpath_windows.go for GetFinalPathNameByHandleW), and canonical applies
// the platform's canonical form on top.
func RealPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	resolved, err := realPath(abs)
	if err != nil {
		return "", err
	}
	return canonical(resolved), nil
}

// ExternalPath renders a realised path in the spelling an external tool
// accepts — the "path for an external tool" conversion.
//
// RealPath's output is for this process: it is the form containment
// comparisons and registry keys are built on, and on Windows that is the
// extended-length \\?\C:\... spelling GetFinalPathNameByHandleW returns.
// git rejects it (it normalises the backslashes and then cannot open the
// result), so a realised path handed to git, docker or any other program
// must come back through here first. On unix it is the identity, so the
// conversion can be applied unconditionally at the point a path leaves
// the process; a path that carries no prefix is returned unchanged, so
// applying it to a whole argument list is safe.
//
// The pair is deliberate and asymmetric: realise on the way in, convert
// on the way out. Nothing stores the external form.
func ExternalPath(p string) string { return externalPath(p) }

// SamePath reports whether two paths name the same file or directory,
// comparing their realised forms so two spellings of one directory compare
// equal. Two paths that both fail to realise are the same only when they are
// literally equal: a path that does not exist has no identity beyond its
// spelling. One realising and the other not is the error, because the answer
// is then unknown rather than false.
//
// Getting this wrong once made init refuse its own worktree as a slug
// collision, which also broke idempotence, so there is one of these.
func SamePath(a, b string) (bool, error) {
	ra, errA := RealPath(a)
	rb, errB := RealPath(b)
	switch {
	case errA == nil && errB == nil:
		return ra == rb, nil
	case errA != nil && errB != nil:
		return a == b, nil
	case errA != nil:
		return false, errA
	default:
		return false, errB
	}
}

// canonical applies the platform's path canonicalisation on top of symlink
// resolution. On macOS, /System/Volumes/Data is a firmlink rather than a
// symlink — EvalSymlinks does not resolve it — so /Users/geoff and
// /System/Volumes/Data/Users/geoff name the same directory while differing as
// strings. Stripping the prefix is the platform's own canonical form
// (08-platform.md §3, "Path realisation": resolve /tmp, /System/Volumes/Data).
// This is the only GOOS branch in the package.
func canonical(p string) string {
	if runtime.GOOS == "darwin" {
		const firmlink = "/System/Volumes/Data"
		if p == firmlink {
			return "/"
		}
		if strings.HasPrefix(p, firmlink+"/") {
			return strings.TrimPrefix(p, firmlink)
		}
	}
	return p
}

// CaseSensitive probes the mount holding dir and reports whether it compares
// paths case-sensitively.
//
// Containment must follow the filesystem, not the operating system: a
// case-sensitive comparison on a case-insensitive mount can be walked straight
// past, because /Users/geoff/repos/x and /Users/geoff/Repos/x are the same
// directory on APFS and NTFS and differ as strings (08-platform.md §4.2). A
// case-sensitive volume on macOS is supported, so the probe runs against the
// actual mount rather than assuming from GOOS. Windows is the one platform
// where the answer is known without probing (NTFS is case-insensitive and the
// probe would need write access to the volume root).
//
// The probe creates one file and checks whether a differently-cased spelling
// of the same name collides with it. Where the probe cannot determine the
// answer it returns an error; the caller assumes case-insensitive, which is
// the conservative direction for the containment property (08-platform.md §8).
func CaseSensitive(dir string) (bool, error) {
	if runtime.GOOS == "windows" {
		// Windows answers without probing — NTFS is case-insensitive
		// unless a directory was marked otherwise, and the conservative
		// answer is the one that already holds. The directory is still
		// checked: "a directory that is not there cannot be probed" is
		// the contract on every platform, and a caller that gets an
		// answer for a path that does not exist has been told something
		// about nothing.
		fi, err := os.Stat(dir)
		if err != nil {
			return false, fmt.Errorf("case-sensitivity probe of %s: %w", dir, err)
		}
		if !fi.IsDir() {
			return false, fmt.Errorf("case-sensitivity probe of %s: not a directory", dir)
		}
		return false, nil
	}
	// The answer is a property of the mount and cannot change under a
	// running process, so it is probed once per directory. Without the
	// cache, a read-only predicate like identity.Contains writes a file
	// into the worktree root on every call.
	if v, ok := caseSensitiveCache.Load(dir); ok {
		return v.(bool), nil
	}
	randBytes := make([]byte, 8)
	if _, err := rand.Read(randBytes); err != nil {
		return false, fmt.Errorf("case-sensitivity probe of %s: %w", dir, err)
	}
	// Lowercase letters and digits only, so the upper-cased variant differs
	// solely in case.
	base := "wtcaseprobe" + hex.EncodeToString(randBytes)
	probe := filepath.Join(dir, base)
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("case-sensitivity probe of %s: %w", dir, err)
	}
	f.Close()
	defer os.Remove(probe)
	if _, err := os.Stat(filepath.Join(dir, strings.ToUpper(base))); err == nil {
		caseSensitiveCache.Store(dir, false) // the upper-cased spelling names the same file
		return false, nil
	} else if os.IsNotExist(err) {
		caseSensitiveCache.Store(dir, true)
		return true, nil
	} else {
		return false, fmt.Errorf("case-sensitivity probe of %s: %w", dir, err)
	}
}

// caseSensitiveCache holds each probed directory's answer for the life of
// the process. Failures are not cached: the next call probes again.
var caseSensitiveCache sync.Map
