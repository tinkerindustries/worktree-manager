Worktree Manager 0.9.0-test

One distribution per platform containing both binaries — the wt client and
the wtd coordinator — built from the one Go module with CGO_ENABLED=0, so
nothing else needs installing. This archive contains:

  wt, wtd                    the two binaries (wt.exe, wtd.exe on Windows)
  install.sh                 the unix installer (install.ps1 on Windows)

Install:

  tar xzf wt-0.9.0-test-<os>-<arch>.tar.gz
  cd wt-0.9.0-test-<os>-<arch>
  ./install.sh               # installs into ~/.local/bin (or
                             # %LOCALAPPDATA%\wt\bin) and registers the
                             # coordinator with launchd, systemd or the
                             # Task Scheduler, then starts it

  ./install.sh --prefix <dir>   # self-contained install into <dir>; the
                                # registration is written there and nothing
                                # is loaded (a temp-prefix install)
  ./install.sh --tcp 127.0.0.1:7331 --tcp-token <token>
                                # also enable the opt-in loopback TCP
                                # listener for hosts where a socket cannot
                                # be shared into a container; every TCP
                                # connection must present the token

Verify with: wt daemon status

See RELEASE.md in the repository for versioning, the compatibility policy
and rollback.
