//go:build darwin || linux

package platform

// externalPath is the identity on unix: the realised form of a path is
// already the only spelling any external tool understands.
func externalPath(p string) string { return p }
