//go:build !darwin && !linux && !windows

package platform

// helperDirs is the fallback list everywhere else: empty. LookHelper still
// searches PATH, so a platform this project does not support loses
// nothing it had.
func helperDirs() []string { return nil }
