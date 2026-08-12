// Command wtd is the worktree-manager coordinator.
//
// Phase 0: the coordinator itself lands in phase 2. This binary starts
// nothing and only reports its version, so that the two-binary split exists
// from the first commit and both halves cross-compile.
package main

import "fmt"

// version is the coordinator's version line. RELEASE.md (phase 2) owns the
// versioning policy; until then this is a constant.
const version = "0.0.0"

func main() {
	fmt.Printf("wtd version %s (phase 0 skeleton; coordinator lands in phase 2)\n", version)
}
