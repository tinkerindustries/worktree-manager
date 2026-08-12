package identity

import (
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// DescriptorPath is where the descriptor lives for this tree: a fixed
// filename at the worktree root (01-identity.md §4.3). The filename comes
// from emit.descriptor.filename in the spec — never from a constant of this
// package's own — and carries no leading dot, so plain ls shows it.
func DescriptorPath(root string, d spec.Descriptor) string {
	return filepath.Join(root, d.Filename)
}
