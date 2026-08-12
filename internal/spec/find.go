package spec

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindSpecPath locates the committed spec by walking up from startDir to the
// worktree root and stopping there — never above it. The rule is fixed here
// and constant everywhere: the generated descriptor reader (phase 5) depends
// on it, and it is the reason the fixture specs live at their fixture roots.
//
// The first directory that contains a wt.yaml is the worktree root; the walk
// never continues past it, so a wt.yaml in a parent directory cannot shadow
// or be shadowed by the repo's own.
func FindSpecPath(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", startDir, err)
	}
	for {
		candidate := filepath.Join(dir, SpecFilename)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", &NotAdoptedError{Dir: startDir}
		}
		dir = parent
	}
}

// NotAdoptedError is the "no wt.yaml anywhere up the tree" refusal. The
// verbs report it as not adopted rather than failing obscurely
// (PLAN-SCOPE.md, definition of done).
type NotAdoptedError struct {
	Dir string
}

func (e *NotAdoptedError) Error() string {
	return fmt.Sprintf("no %s found walking up from %s; this repository has not adopted the tooling", SpecFilename, e.Dir)
}
