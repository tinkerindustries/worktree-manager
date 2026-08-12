//go:build linux

package platform

import (
	"os"
	"strconv"
	"testing"
)

// TestActivatedListenerRefusesForeignHandoff: LISTEN_FDS naming another
// process, or more than one descriptor, is refused — the handoff is only
// valid for this process and for exactly the one socket the unit owns.
//
// Linux-only, because socket activation is systemd's: every other platform's
// ActivatedListener reports "not activated" unconditionally (systemd_other.go),
// so there is no handoff to refuse and the assertions below would fail there.
func TestActivatedListenerRefusesForeignHandoff(t *testing.T) {
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_PID", "999999")
	if _, err := ActivatedListener(); err == nil {
		t.Error("a handoff naming another pid succeeded")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	if _, err := ActivatedListener(); err == nil {
		t.Error("a two-descriptor handoff succeeded, want a refusal")
	}
}
