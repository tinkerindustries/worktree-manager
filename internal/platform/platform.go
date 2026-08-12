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
		return false, nil
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
		return false, nil // the upper-cased spelling names the same file
	} else if os.IsNotExist(err) {
		return true, nil
	} else {
		return false, fmt.Errorf("case-sensitivity probe of %s: %w", dir, err)
	}
}
