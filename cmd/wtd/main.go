// Command wtd is the worktree-manager coordinator: a resident per-user
// process owning the store and every privileged operation. It runs in the
// foreground against a socket path given by --socket or WT_SOCKET — the
// mode the tests and the CI gates use, and the reason the process lifecycle
// is split from supervisor registration: a hosted Linux runner has no
// launchd. Under a supervisor (a launchd LaunchAgent on macOS, written by
// `wt daemon install`) it is the same binary started by someone else.
//
// Lifecycle: SIGINT and SIGTERM cancel the serving context, in-flight
// requests finish, and the process exits 0. Structured logging with
// log/slog goes to stderr — wtd is the only binary that logs; the client
// prints and does not log.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mrgeoffrich/worktree-manager/internal/coord"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// version is the coordinator's release version (RELEASE.md owns the
// versioning policy; this is the phase-2 value).
const version = "0.2.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("wtd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socket := fs.String("socket", "", "socket path (default: WT_SOCKET, then the platform default)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "wtd: unexpected arguments: %v\n", fs.Args())
		return 1
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	root, err := store.Root()
	if err != nil {
		log.Error("resolving the store root", "err", err)
		return 1
	}
	st, err := store.Open(root)
	if err != nil {
		log.Error("opening the store", "err", err)
		return 1
	}

	socketPath := *socket
	if socketPath == "" {
		socketPath, err = platform.SocketPath()
		if err != nil {
			log.Error("resolving the socket path", "err", err)
			return 1
		}
	}

	h, err := coord.NewHandler(st, log)
	if err != nil {
		log.Error("building the coordinator", "err", err)
		return 1
	}
	srv := coord.NewServer(h, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("wtd starting", "version", version, "store", root, "socket", socketPath)
	if err := srv.Serve(ctx, socketPath); err != nil {
		log.Error("coordinator stopped with an error", "err", err)
		return 1
	}
	log.Info("wtd stopped")
	return 0
}
