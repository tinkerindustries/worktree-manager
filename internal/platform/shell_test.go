package platform

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestShellCommandRunsPosixShell: the hook shell is a POSIX shell on every
// platform, so a redirection and a background job — what the fixture's own
// start hook uses — run as written.
func TestShellCommandRunsPosixShell(t *testing.T) {
	dir := t.TempDir()
	c, err := ShellCommand(context.Background(), "echo one two >out.txt; cat out.txt")
	if err != nil {
		t.Skipf("no POSIX shell on this host: %v", err)
	}
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("running the hook command: %v", err)
	}
	if strings.TrimSpace(string(out)) != "one two" {
		t.Errorf("output = %q, want %q", out, "one two")
	}
}

// TestShellCommandHonoursTheContext: the context carries the hook's declared
// timeout, and the command dies with it.
func TestShellCommandHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c, err := ShellCommand(ctx, "sleep 30")
	if err != nil {
		t.Skipf("no POSIX shell on this host: %v", err)
	}
	start := time.Now()
	if err := c.Run(); err == nil {
		t.Fatal("the command outlived its context")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the command took %s to die", elapsed)
	}
}
