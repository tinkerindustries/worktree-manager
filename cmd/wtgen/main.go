// Command wtgen regenerates the phase-R2 API artefacts in place: the
// OpenAPI 3.1 document at api/openapi.yaml and the Go client at
// internal/api/client/client.go, both derived from the route table and
// the *Args/*Result types in internal/api (plan.md §5, phase R2). It
// writes the two files and nothing else; CI regenerates on a clean tree
// and fails if git reports a diff. Run from the repository root:
//
//	go run ./cmd/wtgen
//
// The generator is not a shipped binary — dist/build.sh builds only wt
// and wtd — and it takes no arguments.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/apigen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "wtgen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	openapi, err := apigen.OpenAPI()
	if err != nil {
		return err
	}
	clientSrc, err := apigen.Client()
	if err != nil {
		return err
	}
	for _, out := range []struct {
		path string
		data []byte
	}{
		{"api/openapi.yaml", openapi},
		{"internal/api/client/client.go", clientSrc},
	} {
		if err := os.MkdirAll(filepath.Dir(out.path), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(out.path), err)
		}
		if err := os.WriteFile(out.path, out.data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", out.path, err)
		}
	}
	return nil
}
