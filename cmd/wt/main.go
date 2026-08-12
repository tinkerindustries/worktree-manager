// Command wt is the worktree-manager client. It runs once per operation and
// prints results to stdout and diagnostics to stderr. Phase 0 adds exactly
// two verbs — spec validate and spec explain — with the coordinator verbs
// landing in later phases.
package main

import (
	"os"

	"github.com/mrgeoffrich/worktree-manager/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
