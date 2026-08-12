package platform

// tcp_test.go pins the loopback TCP surface's configuration rails: the
// listener binds a literal loopback address only, the token is long enough
// to resist brute force and whitespace-free (the Windows task XML carries
// it in an <Arguments> string), and the two flags are one decision.

import "testing"

func TestValidateLoopbackTCP(t *testing.T) {
	ok := []string{"127.0.0.1:7331", "[::1]:7331"}
	for _, addr := range ok {
		if err := ValidateLoopbackTCP(addr); err != nil {
			t.Errorf("ValidateLoopbackTCP(%q) = %v, want nil", addr, err)
		}
	}
	bad := []string{
		"0.0.0.0:7331",      // all interfaces: the surface must bind loopback only
		"192.168.1.10:7331", // the LAN
		":7331",             // no host: all interfaces
		"localhost:7331",    // not a literal loopback address
		"7331",              // not host:port
		"127.0.0.1",         // no port
	}
	for _, addr := range bad {
		if err := ValidateLoopbackTCP(addr); err == nil {
			t.Errorf("ValidateLoopbackTCP(%q) accepted a non-loopback address", addr)
		}
	}
}

func TestValidateTCPToken(t *testing.T) {
	if err := ValidateTCPToken("0123456789abcdef"); err != nil {
		t.Errorf("a 16-character token refused: %v", err)
	}
	if err := ValidateTCPToken("short"); err == nil {
		t.Error("a short token accepted; the listener would be brute-forceable")
	}
	if err := ValidateTCPToken("with space 0123456789"); err == nil {
		t.Error("a token with whitespace accepted; it would split in the Windows task's Arguments")
	}
}

func TestValidateTCPConfig(t *testing.T) {
	if err := ValidateTCPConfig("", ""); err != nil {
		t.Errorf("the default (no TCP) must validate: %v", err)
	}
	for _, args := range [][]string{
		{"127.0.0.1:7331", ""},
		{"", "0123456789abcdef"},
	} {
		if err := ValidateTCPConfig(args[0], args[1]); err == nil {
			t.Errorf("ValidateTCPConfig(%q, %q) accepted a half-configured surface", args[0], args[1])
		}
	}
	if err := ValidateTCPConfig("127.0.0.1:7331", "0123456789abcdef"); err != nil {
		t.Errorf("a full configuration refused: %v", err)
	}
}
