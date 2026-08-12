package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestWtNeverImportsCoordinatorPackages enforces the package boundary that
// keeps WT_HOME unreadable by a client: cmd/wt may never import
// internal/store, internal/coord, internal/driver or internal/fleet — they
// are coordinator-only, and the separation is what makes the store's
// location a coordinator secret (ARCHITECTURE.md §8.2; CLAUDE.md, "The two
// binaries"). The check runs `go list -deps` over the real dependency
// graph, so a transitive import is caught too, not only a direct one.
func TestWtNeverImportsCoordinatorPackages(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/mrgeoffrich/worktree-manager/cmd/wt")
	cmd.Dir = "../.." // the module root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps cmd/wt: %v", err)
	}
	forbidden := []string{
		"github.com/mrgeoffrich/worktree-manager/internal/store",
		"github.com/mrgeoffrich/worktree-manager/internal/coord",
		"github.com/mrgeoffrich/worktree-manager/internal/driver",
		"github.com/mrgeoffrich/worktree-manager/internal/fleet",
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		for _, f := range forbidden {
			if line == f || strings.HasPrefix(line, f+"/") {
				t.Errorf("cmd/wt transitively imports the coordinator-only package %s; a client must never read the store", line)
			}
		}
	}
}
