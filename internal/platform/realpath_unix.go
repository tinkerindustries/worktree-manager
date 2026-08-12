//go:build darwin || linux

package platform

import (
	"fmt"
	"path/filepath"
)

// realPath resolves an absolute path through symlinks; a path that does
// not exist yet is resolved through its deepest existing ancestor and the
// missing tail re-appended.
func realPath(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	// Walk up to the nearest existing ancestor and re-join the missing tail.
	// EvalSymlinks of that ancestor gives the tail a resolved base to sit on,
	// so a new file under a symlinked directory resolves correctly too.
	tail := []string{}
	cur := abs
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("resolving %s: no existing ancestor: %w", abs, err)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
		resolved, rerr := filepath.EvalSymlinks(cur)
		if rerr != nil {
			continue
		}
		for _, seg := range tail {
			resolved = filepath.Join(resolved, seg)
		}
		return resolved, nil
	}
}
