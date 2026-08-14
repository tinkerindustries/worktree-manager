package platform

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestValidateListenAddr(t *testing.T) {
	t.Run("empty is valid: the caller applies the default", func(t *testing.T) {
		if err := ValidateListenAddr("", false); err != nil {
			t.Errorf("empty address = %v, want nil", err)
		}
	})

	for _, addr := range []string{"127.0.0.1:7833", "127.0.0.1:1", "[::1]:7833", "localhost:7833"} {
		if err := ValidateListenAddr(addr, false); err != nil {
			t.Errorf("ValidateListenAddr(%q) = %v, want nil", addr, err)
		}
	}

	// Off-loopback and wildcard binds are refused without --allow-remote.
	// ":7833" is the one most easily mistaken for harmless: it binds every
	// interface, which is exactly what the rule exists to prevent.
	for _, addr := range []string{"0.0.0.0:7833", ":7833", "192.0.2.1:7833"} {
		err := ValidateListenAddr(addr, false)
		if err == nil {
			t.Errorf("ValidateListenAddr(%q) accepted a non-loopback bind", addr)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") || !strings.Contains(err.Error(), "--allow-remote") {
			t.Errorf("ValidateListenAddr(%q) refusal must name loopback and the remedy, got: %v", addr, err)
		}
	}

	// --allow-remote is the deliberate decision that lifts the rule.
	for _, addr := range []string{"0.0.0.0:7833", "192.0.2.1:7833"} {
		if err := ValidateListenAddr(addr, true); err != nil {
			t.Errorf("ValidateListenAddr(%q, allowRemote) = %v, want nil", addr, err)
		}
	}

	if err := ValidateListenAddr("not-a-host-port", false); err == nil {
		t.Error("a malformed address was accepted")
	}
}

func TestValidateContainerToken(t *testing.T) {
	// Empty means containers are simply not admitted — a valid choice, and
	// the default one.
	if err := ValidateContainerToken(""); err != nil {
		t.Errorf("empty token = %v, want nil (containers not admitted)", err)
	}
	if err := ValidateContainerToken(strings.Repeat("a", MinContainerTokenLength)); err != nil {
		t.Errorf("a token of the minimum length = %v, want nil", err)
	}
	if err := ValidateContainerToken(strings.Repeat("a", MinContainerTokenLength-1)); err == nil {
		t.Error("a token one character under the minimum was accepted")
	}
	if err := ValidateContainerToken("with space 0123456789"); err == nil {
		t.Error("a token containing whitespace was accepted; it is carried in unit files and argument strings")
	}
}

// TestValidateCoordinatorConfigTreatsSettingsIndependently is the R3
// regression: before the split these were validated as a pair, so a custom
// address without a container token — the ordinary case for a second user
// on one machine — was refused outright.
func TestValidateCoordinatorConfigTreatsSettingsIndependently(t *testing.T) {
	if err := ValidateCoordinatorConfig("127.0.0.1:9001", "", false); err != nil {
		t.Errorf("a custom address with no container token = %v, want nil", err)
	}
	if err := ValidateCoordinatorConfig("", strings.Repeat("a", MinContainerTokenLength), false); err != nil {
		t.Errorf("a container token with no custom address = %v, want nil", err)
	}
	if err := ValidateCoordinatorConfig("", "", false); err != nil {
		t.Errorf("neither setting = %v, want nil", err)
	}
	if err := ValidateCoordinatorConfig("127.0.0.1:9001", "short", false); err == nil {
		t.Error("a short container token was accepted alongside a valid address")
	}
}

func TestChooseRegistrationAddr(t *testing.T) {
	t.Run("takes the default when it is free", func(t *testing.T) {
		free := freePort(t)
		def := net.JoinHostPort("127.0.0.1", strconv.Itoa(free))
		addr, chosen, err := ChooseRegistrationAddr(def)
		if err != nil {
			t.Fatalf("ChooseRegistrationAddr(%q) = %v", def, err)
		}
		if addr != def || chosen {
			t.Errorf("got (%q, chosen=%v), want (%q, chosen=false)", addr, chosen, def)
		}
	})

	t.Run("moves past a held port and reports that it moved", func(t *testing.T) {
		// Hold the default so the probe has to step past it. This is the
		// two-users-on-one-machine case the probe exists for.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("holding a port: %v", err)
		}
		defer ln.Close()
		held := ln.Addr().(*net.TCPAddr).Port
		def := net.JoinHostPort("127.0.0.1", strconv.Itoa(held))

		addr, chosen, err := ChooseRegistrationAddr(def)
		if err != nil {
			t.Fatalf("ChooseRegistrationAddr(%q) = %v", def, err)
		}
		if !chosen {
			t.Errorf("chosen=false, but the default %q was held", def)
		}
		if addr == def {
			t.Errorf("returned the held address %q", addr)
		}
		_, portStr, _ := net.SplitHostPort(addr)
		got, _ := strconv.Atoi(portStr)
		if got <= held {
			t.Errorf("chose port %d, want something above the held %d", got, held)
		}
	})

	t.Run("a malformed default is an error, not a guess", func(t *testing.T) {
		if _, _, err := ChooseRegistrationAddr("not-host-port"); err == nil {
			t.Error("a malformed default address was accepted")
		}
	})
}

// freePort returns a port that is free at the moment it is asked for.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
