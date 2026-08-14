package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestVersionFlag: `wtd --version` prints the version and the commit on
// one line and exits 0 — before any configuration validation, any store
// open and any listener, so it works on a machine with no store and no
// supervisor.
func TestVersionFlag(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := run([]string{"--version"})
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	line := strings.TrimSpace(string(out))
	if line != "wtd "+version+" ("+commit+")" {
		t.Errorf("version line = %q, want %q", line, "wtd "+version+" ("+commit+")")
	}
}
