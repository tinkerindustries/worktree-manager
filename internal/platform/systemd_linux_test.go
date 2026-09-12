//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
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

// TestSystemdUnitsAgreeOnTheListener is the regression test for a Linux
// install that could never start. The service unit passed --activate and
// --addr together, which wtd refuses as mutually exclusive (two things
// cannot both decide where it listens), so systemd restarted it until it
// hit the start limit; and the socket unit's ListenStream was a unix
// socket path, left over from the transport that preceded HTTP, so even a
// service that had started would have been handed a listener no client
// dials. The address belongs to the socket unit and nothing else.
func TestSystemdUnitsAgreeOnTheListener(t *testing.T) {
	const addr = "127.0.0.1:7999"
	const token = "0123456789abcdef"

	svc := string(systemdServiceUnit("/usr/local/bin/wtd", token, []string{"host.docker.internal"}))
	if strings.Contains(svc, "--addr") {
		t.Errorf("the service unit passes --addr alongside --activate, which wtd refuses:\n%s", svc)
	}
	for _, want := range []string{
		`ExecStart="/usr/local/bin/wtd" --activate`,
		`--container-token "` + token + `"`,
		`--allow-host "host.docker.internal"`,
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("the service unit lacks %s:\n%s", want, svc)
		}
	}

	// The pinned address is the socket unit's, verbatim: the listener the
	// client dials and the listener systemd hands over are one socket.
	sock := string(systemdSocketUnit(addr))
	if !strings.Contains(sock, "ListenStream="+addr+"\n") {
		t.Errorf("the socket unit does not listen on the pinned address %s:\n%s", addr, sock)
	}
	if strings.Contains(sock, "/wt/sock") {
		t.Errorf("the socket unit listens on a unix socket; the transport is HTTP over TCP:\n%s", sock)
	}
	if strings.Contains(sock, "SocketMode") {
		t.Errorf("the socket unit carries SocketMode, which means nothing for a TCP listener:\n%s", sock)
	}

	// A registration that pinned no address still has to name one, because
	// the socket unit is what decides: the compiled-in default.
	if d := string(systemdSocketUnit("")); !strings.Contains(d, "ListenStream="+DefaultCoordinatorAddr+"\n") {
		t.Errorf("the socket unit for an unpinned registration does not fall back to %s:\n%s", DefaultCoordinatorAddr, d)
	}
}
