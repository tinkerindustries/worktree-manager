//go:build !windows

package platform

import (
	"fmt"
	"os/exec"
)

// shellPath resolves the POSIX shell. A unix host without one is not a host
// this tool can run hooks on.
func shellPath() (string, error) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		return "", fmt.Errorf("no POSIX shell: sh is not on PATH; hook commands are shell commands and cannot run without one")
	}
	return sh, nil
}
