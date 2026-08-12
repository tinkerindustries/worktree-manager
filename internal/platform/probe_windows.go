//go:build windows

package platform

import "syscall"

// probeControl returns a nil control on Windows: SO_REUSEADDR permits
// binding an address another socket already holds, so setting it would make
// the port probe report free while the port is in use — the exact failure
// the probe exists to prevent (08-platform.md §4.1).
func probeControl() func(network, address string, c syscall.RawConn) error {
	return nil
}
