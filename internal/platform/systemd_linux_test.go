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

// TestSystemdRegisterStepsRestartsWhatIsAlreadyRunning is the regression
// test for an upgrade that did nothing. The sequence used `enable --now`,
// and --now only *starts* a unit — against one already running it is not an
// error, it is a no-op. So install.sh replaced wtd on disk, drove
// `wt daemon install`, and the old coordinator went on serving from the
// replaced (now unlinked) inode until the next logout, with the installer
// and `wt daemon status` both reporting success.
//
// The sequence is asserted rather than run: no test may register anything
// with the machine's own systemd.
func TestSystemdRegisterStepsRestartsWhatIsAlreadyRunning(t *testing.T) {
	steps := systemdRegisterSteps()

	var flat []string
	for _, s := range steps {
		flat = append(flat, strings.Join(s.args, " "))
		if s.failure == "" {
			t.Errorf("step %q has no failure phrase; every step reports what it was doing", strings.Join(s.args, " "))
		}
	}
	got := strings.Join(flat, "; ")

	// --now is the bug: it would leave a running coordinator untouched.
	for _, s := range flat {
		if strings.Contains(s, "--now") {
			t.Errorf("the sequence uses `--now`, which does nothing to an already-running unit: %s", got)
		}
	}

	want := []string{
		"daemon-reload",
		"enable " + SystemdServiceFilename + " " + SystemdSocketFilename,
		// The socket first: it owns the listener, so a changed
		// ListenStream binds only once the socket has restarted.
		"restart " + SystemdSocketFilename,
		// Then the service, which is what execs the newly installed binary.
		"restart " + SystemdServiceFilename,
	}
	if len(flat) != len(want) {
		t.Fatalf("sequence = %q, want %q", got, strings.Join(want, "; "))
	}
	for i := range want {
		if flat[i] != want[i] {
			t.Errorf("step %d = %q, want %q (whole sequence: %s)", i, flat[i], want[i], got)
		}
	}

	// The units must be enabled for the login start as well as restarted:
	// a coordinator that only runs until reboot is not registered.
	if !strings.Contains(got, "enable ") {
		t.Errorf("the sequence never enables the units, so nothing starts at login: %s", got)
	}
}
