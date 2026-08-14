package cli

import (
	"strings"
	"testing"
)

// TestVersionFlag: `wt --version` prints exactly "wt <version> (<commit>)"
// on one line and exits 0 — the line the installers parse to report what
// is being replaced and with what. It must work with no store, no
// endpoint and no coordinator: it is the identity of the binary alone.
func TestVersionFlag(t *testing.T) {
	code, stdout, stderr := runCLI(t, "--version")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	line := strings.TrimSpace(stdout)
	if line != "wt "+version+" ("+commit+")" {
		t.Errorf("version line = %q, want %q", line, "wt "+version+" ("+commit+")")
	}
	// The single-dash form works too, like every flag the client takes.
	code, stdout, _ = runCLI(t, "-version")
	if code != ExitOK || strings.TrimSpace(stdout) != "wt "+version+" ("+commit+")" {
		t.Errorf("-version = %d %q", code, stdout)
	}
}
