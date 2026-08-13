package store

import (
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// SpecsFileName is the coordinator's per-app spec cache. The client sends
// the spec with every allocate call (ARCHITECTURE.md §8.1), and the
// coordinator records the latest valid one per app so that reclamation —
// phase 6's coordinator-side teardown of a dead ephemeral client's entries
// — can run without any client around to supply it: a container's entry
// path is not visible from the host, so the walk-up spec lookup cannot
// reach it, and the cached spec is what the teardown computes dependent
// projects and purge refusals from. The cache is a convenience, never an
// authority: every client-initiated call still sends its own spec, and the
// cache is only read when no spec can be found any other way.
const SpecsFileName = "specs.json"

// SpecsFile is specs.json: one spec per app, the latest valid one the
// coordinator saw on an allocate call.
type SpecsFile struct {
	Versioned
	Specs map[string]spec.Spec `json:"specs"`
}

// ReadSpecs loads the spec cache; a missing file is an empty cache.
func (s *Store) ReadSpecs() (SpecsFile, error) {
	f, err := loadFile(s, SpecsFileName, func() SpecsFile {
		return SpecsFile{Versioned: Versioned{SchemaVersion: SchemaVersion}}
	})
	if err != nil {
		return SpecsFile{}, err
	}
	if f.Specs == nil {
		f.Specs = map[string]spec.Spec{}
	}
	return f, nil
}

// WriteSpecs persists the spec cache atomically.
func (s *Store) WriteSpecs(f SpecsFile) error {
	f.Versioned = Versioned{SchemaVersion: SchemaVersion}
	return s.Save(SpecsFileName, &f)
}
