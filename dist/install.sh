#!/bin/sh
# wt — the worktree manager installer (unix).
#
# Installs the two binaries from this archive into a prefix and registers
# the coordinator with the platform's supervisor, starting it — the same
# registration `wt daemon install` performs, driven by the installer rather
# than reimplemented (docs/ARCHITECTURE.md §13.1). Nothing else on the
# machine is touched.
#
# An explicit --prefix is a self-contained install: the binaries land in
# <prefix>/bin and the supervisor registration is written under the same
# prefix without loading anything (the `wt daemon install --prefix` rail).
# Without --prefix the binaries go to $HOME/.local/bin and the
# coordinator is registered with the real supervisor.
#
# The coordinator listens on a loopback HTTP port. --addr overrides the
# default; --container-token admits container clients (16+ characters).
# The two are independent: a custom port needs no token, and a token needs
# no custom port.
set -eu

usage() {
	cat <<'EOF'
usage: install.sh [--prefix <dir>] [--addr <addr>] [--container-token <tok>]

  --prefix <dir>          install the binaries into <dir>/bin and write the
                          supervisor registration under <dir>
                          (self-contained; nothing is loaded). Default:
                          $HOME/.local/bin with a real supervisor
                          registration.
  --addr <addr>           the loopback address the coordinator listens on.
                          Default: a free port, chosen at install time.
  --container-token <tok> admit container clients with this token (at
                          least 16 characters; WT_CONTAINER_TOKEN also
                          works, which keeps it out of shell history).
                          Absent, only host clients are admitted.
  -h, --help              this help.
EOF
}

PREFIX="${WT_PREFIX:-$HOME/.local}"
REG_PREFIX=""
ADDR=""
CONTAINER_TOKEN="${WT_CONTAINER_TOKEN:-}"

while [ $# -gt 0 ]; do
	case "$1" in
	--prefix)
		[ $# -ge 2 ] || { echo "install.sh: --prefix needs an argument" >&2; exit 2; }
		PREFIX="$2"
		REG_PREFIX="$2"
		shift 2
		;;
	--addr)
		[ $# -ge 2 ] || { echo "install.sh: --addr needs an argument" >&2; exit 2; }
		ADDR="$2"
		shift 2
		;;
	--container-token)
		[ $# -ge 2 ] || { echo "install.sh: --container-token needs an argument" >&2; exit 2; }
		CONTAINER_TOKEN="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "install.sh: unknown option: $1" >&2
		usage
		exit 2
		;;
	esac
done

# The archive's own directory: the two binaries must be right next to this
# script, which is how the distribution is assembled.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
for bin in wt wtd; do
	if [ ! -x "$SCRIPT_DIR/$bin" ]; then
		echo "install.sh: $bin is missing from this distribution (install from the archive, not from a bare copy)" >&2
		exit 1
	fi
done

BINDIR="$PREFIX/bin"
mkdir -p "$BINDIR"
cp "$SCRIPT_DIR/wt" "$SCRIPT_DIR/wtd" "$BINDIR/"
chmod 755 "$BINDIR/wt" "$BINDIR/wtd"
echo "installed wt and wtd into $BINDIR"

# Register the coordinator with the platform's supervisor and start it:
# the installer drives `wt daemon install`, which owns the per-platform
# registration (launchd on macOS, the systemd user unit on Linux, the
# logon scheduled task on Windows). An explicit prefix directs the
# registration at the same prefix, where it is inert data. The arguments
# are one word each by construction (a host:port address and a
# whitespace-free token — the validation `wt daemon install` runs), so the
# unquoted expansion is safe under `set -u`.
#
# --addr and --container-token are passed independently: the coordinator
# has a default address and admits containers only when a token is
# configured, so neither implies the other.
COORD_ARGS=""
if [ -n "$ADDR" ]; then
	COORD_ARGS="--addr $ADDR"
fi
if [ -n "$CONTAINER_TOKEN" ]; then
	COORD_ARGS="$COORD_ARGS --container-token $CONTAINER_TOKEN"
fi
if [ -n "$REG_PREFIX" ]; then
	"$BINDIR/wt" daemon install --prefix "$REG_PREFIX" --wtd "$BINDIR/wtd" $COORD_ARGS
else
	"$BINDIR/wt" daemon install --wtd "$BINDIR/wtd" $COORD_ARGS
fi
if [ -n "$CONTAINER_TOKEN" ] && [ -z "$REG_PREFIX" ]; then
	echo "container clients admitted; a container sets WT_ENDPOINT to the address above and WT_CLIENT_TOKEN to the token"
fi

echo "verify with: wt daemon status"
case ":$PATH:" in
*":$BINDIR:"*) ;;
*) echo "note: $BINDIR is not on your PATH; add it (e.g. export PATH=\"$BINDIR:\$PATH\")" ;;
esac
