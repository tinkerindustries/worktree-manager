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
# The opt-in loopback TCP surface is configured with --tcp <addr> and
# --tcp-token <token> (both together, loopback address, 16+ characters) —
# for hosts where a socket cannot be shared into a container.
set -eu

usage() {
	cat <<'EOF'
usage: install.sh [--prefix <dir>] [--tcp <addr> --tcp-token <token>]

  --prefix <dir>     install the binaries into <dir>/bin and write the
                     supervisor registration under <dir> (self-contained;
                     nothing is loaded). Default: $HOME/.local/bin with a
                     real supervisor registration.
  --tcp <addr>       also start the coordinator's opt-in loopback TCP
                     listener at this address (requires --tcp-token).
  --tcp-token <tok>  the token every TCP connection must present (at
                     least 16 characters; WT_TCP_TOKEN also works).
  -h, --help         this help.
EOF
}

PREFIX="${WT_PREFIX:-$HOME/.local}"
REG_PREFIX=""
TCP=""
TCP_TOKEN="${WT_TCP_TOKEN:-}"

while [ $# -gt 0 ]; do
	case "$1" in
	--prefix)
		[ $# -ge 2 ] || { echo "install.sh: --prefix needs an argument" >&2; exit 2; }
		PREFIX="$2"
		REG_PREFIX="$2"
		shift 2
		;;
	--tcp)
		[ $# -ge 2 ] || { echo "install.sh: --tcp needs an argument" >&2; exit 2; }
		TCP="$2"
		shift 2
		;;
	--tcp-token)
		[ $# -ge 2 ] || { echo "install.sh: --tcp-token needs an argument" >&2; exit 2; }
		TCP_TOKEN="$2"
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

if [ -n "$TCP" ] && [ -z "$TCP_TOKEN" ]; then
	echo "install.sh: --tcp requires --tcp-token: peer credentials do not exist on a TCP connection, so the listener is unauthenticated without one" >&2
	exit 2
fi

BINDIR="$PREFIX/bin"
mkdir -p "$BINDIR"
cp "$SCRIPT_DIR/wt" "$SCRIPT_DIR/wtd" "$BINDIR/"
chmod 755 "$BINDIR/wt" "$BINDIR/wtd"
echo "installed wt and wtd into $BINDIR"

# Register the coordinator with the platform's supervisor and start it:
# the installer drives `wt daemon install`, which owns the per-platform
# registration (launchd on macOS, the systemd user unit on Linux, the
# logon scheduled task on Windows). An explicit prefix directs the
# registration at the same prefix, where it is inert data.
REG_ARGS=""
if [ -n "$REG_PREFIX" ]; then
	REG_ARGS="--prefix $REG_PREFIX"
fi
if [ -n "$TCP" ]; then
	"$BINDIR/wt" daemon install $REG_ARGS --wtd "$BINDIR/wtd" --tcp "$TCP" --tcp-token "$TCP_TOKEN"
	if [ -z "$REG_PREFIX" ]; then
		echo "loopback TCP enabled on $TCP; a container client dials tcp://<host>:<port> with WT_CLIENT_TOKEN set"
	fi
else
	"$BINDIR/wt" daemon install $REG_ARGS --wtd "$BINDIR/wtd"
fi

echo "verify with: wt daemon status"
case ":$PATH:" in
*":$BINDIR:"*) ;;
*) echo "note: $BINDIR is not on your PATH; add it (e.g. export PATH=\"$BINDIR:\$PATH\")" ;;
esac
