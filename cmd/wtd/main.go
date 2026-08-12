// Command wtd is the worktree-manager coordinator: a resident per-user
// process owning the store and every privileged operation. It runs in the
// foreground against a socket path given by --socket or WT_SOCKET — the
// mode the tests and the CI gates use, and the reason the process lifecycle
// is split from supervisor registration: a hosted Linux runner has no
// launchd. Under a supervisor (a launchd LaunchAgent on macOS, the systemd
// user unit on Linux, a scheduled task on Windows — written by
// `wt daemon install`) it is the same binary started by someone else. On
// Linux the systemd socket unit hands the listener over through
// LISTEN_FDS, and --activate makes wtd consume that descriptor instead of
// opening its own socket (systemd_linux.go).
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
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
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
	activate := fs.Bool("activate", false, "consume the listening socket systemd passed via LISTEN_FDS (the systemd unit passes this; mutually exclusive with --socket)")
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
	// The driver registry is the allocation probe and the teardown path.
	// Phase 8 adds cidr and machine; the registry skips a type with no
	// driver, which is how the earlier phases ran without them.
	h.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{},
		&driver.CIDR{}, &driver.Machine{}))
	// Phase 9, R1: a restart — an upgrade, a crash, a reboot — leaves any
	// reserving entry's materialisation outcome unknowable. The startup
	// pass tears those entries down by handle (or moves them to
	// tearing-down with the note when teardown cannot complete), so an
	// upgrade mid-operation is recoverable rather than wedged
	// (docs/ARCHITECTURE.md §14.1 R1). It runs before the first connection
	// is accepted, so no client can observe a half-recovered entry.
	recovered, rerr := h.RecoverInterrupted()
	if rerr != nil {
		log.Error("recovering interrupted allocations", "err", rerr)
		return 1
	}
	log.Info("wtd startup recovery", "result", coord.RecoveryReport(recovered))
	srv := coord.NewServer(h, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *activate {
		// The systemd socket-activation path: the listener comes from the
		// .socket unit, never from --socket (the two cannot both decide
		// where the coordinator listens).
		if *socket != "" {
			log.Error("--activate and --socket are mutually exclusive", "socket", *socket)
			return 1
		}
		ln, err := platform.ActivatedListener()
		if err != nil {
			log.Error("consuming the activated socket", "err", err)
			return 1
		}
		if ln == nil {
			log.Error("--activate given but no activated socket (LISTEN_FDS is unset); run wtd in the foreground, or start it through the systemd socket unit")
			return 1
		}
		log.Info("wtd starting", "version", version, "store", root, "socket", ln.Addr().String(), "activated", true)
		if err := srv.ServeListener(ctx, ln); err != nil {
			log.Error("coordinator stopped with an error", "err", err)
			return 1
		}
		log.Info("wtd stopped")
		return 0
	}

	log.Info("wtd starting", "version", version, "store", root, "socket", socketPath)
	if err := srv.Serve(ctx, socketPath); err != nil {
		log.Error("coordinator stopped with an error", "err", err)
		return 1
	}
	log.Info("wtd stopped")
	return 0
}
