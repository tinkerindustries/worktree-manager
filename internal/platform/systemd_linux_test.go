//go:build linux

package platform

// systemd_linux_test.go is the linux-only half of the supervisor tests:
// the unit-file escaping and the activated-listener handoff. The
// registration content tests live in the shared daemon_test.go, guarded by
// a runtime.GOOS switch, so they run on every platform's CI.

import (
	"strings"
	"testing"
)

// TestSystemdEscapeExec pins the ExecStart quoting: a path with a space or
// a dollar must survive systemd's argument splitting.
func TestSystemdEscapeExec(t *testing.T) {
	got := systemdEscapeExec(`/home/a b/wtd$1`)
	if !strings.Contains(got, `"`) {
		t.Errorf("exec path is not quoted: %q", got)
	}
	if strings.Contains(got, " ") && !strings.HasPrefix(got, `"`) {
		t.Errorf("a path with a space must be quoted: %q", got)
	}
}
