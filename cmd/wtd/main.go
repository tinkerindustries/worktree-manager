// Command wtd is the worktree-manager coordinator: a resident per-user
// process owning the store and every privileged operation. It runs in the
// foreground against a loopback TCP address given by --addr (default
// 127.0.0.1:7833) — the mode the tests and the CI gates use, and the
// reason the process lifecycle is split from supervisor registration: a
// hosted Linux runner has no launchd. Under a supervisor (a launchd
// LaunchAgent on macOS, the systemd user unit on Linux, a scheduled task
// on Windows — written by `wt daemon install`) it is the same binary
// started by someone else. On Linux the systemd socket unit hands the
// listener over through LISTEN_FDS, and --activate makes wtd consume
// that descriptor instead of opening its own listener (systemd_linux.go).
//
// At startup wtd writes <store>/endpoint.json — the base URL and the host
// token, 0600 on unix — so the client can resolve and authenticate. The
// token is generated on first start and reused thereafter.
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
	"net"
	"os"
	"os/signal"
	"path/filepath"
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
	addr := fs.String("addr", "", "loopback TCP address to listen on (default: 127.0.0.1:7833)")
	allowRemote := fs.Bool("allow-remote", false, "allow a non-loopback bind address (--addr off loopback is refused without this)")
	activate := fs.Bool("activate", false, "consume the listening socket systemd passed via LISTEN_FDS (the systemd unit passes this; mutually exclusive with --addr)")
	containerToken := fs.String("container-token", "", "the token that admits container clients (WT_CLIENT_TOKEN); 16+ characters; absent, only host clients are accepted")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "wtd: unexpected arguments: %v\n", fs.Args())
		return 1
	}

	// Containers are opt-in exactly as the old TCP surface was: absent
	// --container-token the coordinator accepts host clients only, and a
	// token short enough to brute-force is refused (plan.md §5, phase R1).
	if *containerToken != "" && len(*containerToken) < 16 {
		fmt.Fprintln(os.Stderr, "wtd: --container-token must be at least 16 characters (a short token would be brute-forceable over the wire)")
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

	// The clean break: the four JSON files of phases 0–9 are never read,
	// and a store that still carries registry.json gets exactly one warning
	// naming it and saying it is no longer read — a log line, not a refusal,
	// because silence would strand containers, ports and VMs that nothing
	// will ever tear down (PLAN-SCOPE.md non-goal 1).
	if _, serr := os.Stat(filepath.Join(root, store.RegistryFileName)); serr == nil {
		log.Warn("registry.json is present but is no longer read: the store is now the SQLite database (wt.db). " +
			"Anything recorded only in registry.json — containers, ports, VMs — will never be torn down; " +
			"inspect it and release anything still running, then remove the file")
	}

	h, err := coord.NewHandler(st, log)
	if err != nil {
		log.Error("building the coordinator", "err", err)
		return 1
	}
	h.ContainerToken = *containerToken
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
	// (docs/ARCHITECTURE.md §14.1 R1). It runs before the first request
	// is served, so no client can observe a half-recovered entry.
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
		// .socket unit, never from --addr (the two cannot both decide
		// where the coordinator listens).
		if *addr != "" {
			log.Error("--activate and --addr are mutually exclusive", "addr", *addr)
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
		log.Info("wtd starting", "version", version, "store", root, "addr", ln.Addr().String(), "activated", true)
		if err := srv.ServeListener(ctx, ln); err != nil {
			log.Error("coordinator stopped with an error", "err", err)
			return 1
		}
		log.Info("wtd stopped")
		return 0
	}

	listenAddr := *addr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:7833"
	}
	if !*allowRemote {
		// A non-loopback bind address requires --allow-remote: the
		// coordinator is a per-user process holding the user's tokens, and
		// opening it to the network needs an explicit decision. A wildcard
		// bind (":7833", "0.0.0.0:7833") is off loopback too.
		tcpAddr, rerr := net.ResolveTCPAddr("tcp", listenAddr)
		if rerr != nil {
			log.Error("refusing to listen on an unresolvable address", "addr", listenAddr, "err", rerr)
			return 1
		}
		if tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
			log.Error("refusing to listen off loopback", "addr", listenAddr,
				"err", "a non-loopback bind address requires --allow-remote; use 127.0.0.1:7833, or pass --allow-remote deliberately")
			return 1
		}
	}
	log.Info("wtd starting", "version", version, "store", root, "addr", listenAddr, "container_token", *containerToken != "")
	if err := srv.Serve(ctx, listenAddr); err != nil {
		log.Error("coordinator stopped with an error", "err", err)
		return 1
	}
	log.Info("wtd stopped")
	return 0
}
