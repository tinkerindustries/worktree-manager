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
# no custom port. --allow-host (repeatable) names a Host header value the
# coordinator accepts beyond loopback and its own address, which is what a
# container reaching the host by name — host.docker.internal on Docker
# Desktop — needs to get past the DNS-rebinding guard.
#
# Before anything is copied the two binaries are verified against the
# SHA256SUMS manifest this archive carries (build.sh writes it) — an
# archive without one is refused, not skipped; --skip-verify is the
# deliberate override. A verification happens before an upgrade too, and
# the version line says what is being replaced and with what.
#
# --dry-run prints every action — the verification, the paths that would
# be written, the address and container-token decision, the supervisor
# command — and changes nothing on disk; the last line is "dry run:
# nothing was changed".
#
# --client-only installs the wt client alone: wtd is not copied and no
# supervisor registration is made. That is the container install — a
# container runs the client and reaches a coordinator that lives on the
# host, so a wtd inside the image would be a second coordinator with its
# own store, which is precisely what must not happen. It composes with
# --prefix and --dry-run, and it refuses alongside --addr and
# --container-token, which configure a coordinator this install does not
# have.
#
# --uninstall reverses the install: it drives `wt daemon uninstall`
# (which stops the coordinator, deregisters it from the supervisor and
# removes the registration) and then removes the two binaries. The store
# is never removed — it is the only record of what is allocated on the
# machine — and a registry that still holds entries makes the verb (and
# so this script) refuse with exit 3, naming `wt list` and `wt rm`;
# `wt daemon uninstall --force` is the documented way past that refusal.
set -eu

usage() {
	cat <<'EOF'
usage: install.sh [--prefix <dir>] [--addr <addr>] [--container-token <tok>]
                  [--allow-host <host>] [--client-only] [--skip-verify]
                  [--dry-run] [--uninstall]

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
  --allow-host <host>     a Host header value the coordinator accepts
                          beyond loopback and its own address, e.g.
                          host.docker.internal (repeatable). A container
                          reaching the host by name needs its name here,
                          or the coordinator's DNS-rebinding guard refuses
                          the request.
  --client-only           install the wt client alone — no wtd, no
                          supervisor registration. The container install:
                          the client reaches a coordinator on the host
                          through WT_ENDPOINT and WT_CLIENT_TOKEN. Refuses
                          alongside --addr and --container-token, which
                          configure a coordinator this install has not got.
  --skip-verify           install without checking the binaries against the
                          archive's SHA256SUMS (deliberate override; the
                          check runs by default and refuses on a mismatch
                          or a missing manifest).
  --dry-run               print every action — verification, paths, the
                          address and container-token decision, the
                          supervisor command — and change nothing on disk.
  --uninstall             the reverse: drive `wt daemon uninstall` (stop
                          the coordinator, remove the registration), then
                          remove the two binaries. The store is never
                          removed.
  -h, --help              this help.
EOF
}

PREFIX="${WT_PREFIX:-$HOME/.local}"
REG_PREFIX=""
ADDR=""
CONTAINER_TOKEN="${WT_CONTAINER_TOKEN:-}"
UNINSTALL=""
DRY_RUN=""
SKIP_VERIFY=""
CLIENT_ONLY=""
ALLOW_HOSTS=""

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
	--allow-host)
		[ $# -ge 2 ] || { echo "install.sh: --allow-host needs an argument" >&2; exit 2; }
		# Repeatable: each occurrence appends, so naming two container
		# runtimes' names means both. Each value is one word by the
		# validation `wt daemon install` runs, so the unquoted expansion
		# below is safe under set -u.
		ALLOW_HOSTS="$ALLOW_HOSTS --allow-host $2"
		shift 2
		;;
	--client-only)
		CLIENT_ONLY=1
		shift
		;;
	--skip-verify)
		SKIP_VERIFY=1
		shift
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--uninstall)
		UNINSTALL=1
		shift
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

# --client-only installs no coordinator, so the two flags that configure
# one have nothing to configure. Refusing is the house rule — a flag that
# is silently dropped is worse than one that is rejected, because the
# operator believes the container was given a token it never got.
if [ -n "$CLIENT_ONLY" ]; then
	if [ -n "$ADDR" ]; then
		echo "install.sh: --addr configures the coordinator, which --client-only does not install; set WT_ENDPOINT in the container instead" >&2
		exit 2
	fi
	if [ -n "$CONTAINER_TOKEN" ]; then
		echo "install.sh: --container-token configures the coordinator, which --client-only does not install; set WT_CLIENT_TOKEN in the container instead" >&2
		exit 2
	fi
	if [ -n "$ALLOW_HOSTS" ]; then
		echo "install.sh: --allow-host configures the coordinator, which --client-only does not install; pass it where the coordinator is installed" >&2
		exit 2
	fi
fi

# BINS is what this install handles: both binaries normally, the client
# alone under --client-only. Every step below — the presence check, the
# verification, the copy, the removal — reads it, so the two shapes cannot
# drift apart.
BINS="wt wtd"
if [ -n "$CLIENT_ONLY" ]; then
	BINS="wt"
fi

# The archive's own directory: the binaries must be right next to this
# script, which is how the distribution is assembled. An uninstall does not
# need them — it drives the already-installed wt — so the presence check is
# the install path's.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -z "$UNINSTALL" ]; then
	for bin in $BINS; do
		if [ ! -x "$SCRIPT_DIR/$bin" ]; then
			echo "install.sh: $bin is missing from this distribution (install from the archive, not from a bare copy)" >&2
			exit 1
		fi
	done
fi

BINDIR="$PREFIX/bin"

# The checksum tool: sha256sum where present, shasum -a 256 on macOS.
# Neither present is a refusal naming both, not a silent skip.
SUM=""
if command -v sha256sum >/dev/null 2>&1; then
	SUM="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	SUM="shasum -a 256"
fi

# verify_binaries checks the two binaries against the archive's own
# SHA256SUMS (the manifest build.sh writes inside every archive, covering
# exactly wt and wtd). A missing manifest is a refusal too — an archive
# without one is not a distribution this installer built — and every
# refusal names --skip-verify as the deliberate override.
verify_binaries() {
	if [ ! -f "$SCRIPT_DIR/SHA256SUMS" ]; then
		echo "install.sh: SHA256SUMS is missing from this distribution; refusing to install an archive this installer did not build (pass --skip-verify to install anyway)" >&2
		exit 1
	fi
	if [ -z "$SUM" ]; then
		echo "install.sh: neither sha256sum nor shasum is installed, so the binaries cannot be verified (pass --skip-verify to install anyway)" >&2
		exit 1
	fi
	for bin in $BINS; do
		want=$(awk -v b="$bin" '$2 == b { print $1; exit }' "$SCRIPT_DIR/SHA256SUMS")
		if [ -z "$want" ]; then
			echo "install.sh: SHA256SUMS has no entry for $bin; refusing to install an unverifiable binary (pass --skip-verify to install anyway)" >&2
			exit 1
		fi
		got=$($SUM "$SCRIPT_DIR/$bin" | awk '{print $1}')
		if [ "$got" != "$want" ]; then
			echo "install.sh: checksum mismatch for $bin: the archive's SHA256SUMS says $want, the file hashes to $got (pass --skip-verify to install anyway)" >&2
			exit 1
		fi
	done
	echo "verified $(echo $BINS | sed 's/ / and /') against SHA256SUMS"
}

# wt_ident prints a wt binary's identity as "VERSION (COMMIT)" — the
# parenthesised tail of its `wt --version` line — or nothing when the
# binary is missing or its version line is unreadable. That is what lets
# the installer say what is being replaced and with what.
wt_ident() {
	[ -x "$1" ] || return 1
	line="$("$1" --version 2>/dev/null)" || return 1
	set -- $line # wt VERSION (COMMIT)
	[ $# -ge 3 ] || return 1
	printf '%s %s' "$2" "$3"
}

# version_line reports the replacement in the distribution's terms: what
# the machine already has (if anything) and what this archive brings.
version_line() {
	new="$(wt_ident "$SCRIPT_DIR/wt")" || new=""
	if [ -z "$new" ]; then
		new="version unknown"
	fi
	if [ -x "$BINDIR/wt" ]; then
		old="$(wt_ident "$BINDIR/wt")" || old=""
		if [ -n "$old" ]; then
			echo "replacing wt $old with $new"
		else
			echo "replacing wt (installed version unreadable) with $new"
		fi
	else
		echo "installing wt $new"
	fi
}

# The supervisor command the real run issues, as one line for a dry run.
# The container token is a secret and is never echoed — the placeholder
# says a token is configured without repeating it.
supervisor_command() {
	cmd="$BINDIR/wt daemon install --wtd $BINDIR/wtd"
	if [ -n "$REG_PREFIX" ]; then
		cmd="$cmd --prefix $REG_PREFIX"
	fi
	if [ -n "$ADDR" ]; then
		cmd="$cmd --addr $ADDR"
	fi
	if [ -n "$CONTAINER_TOKEN" ]; then
		cmd="$cmd --container-token <token>"
	fi
	if [ -n "$ALLOW_HOSTS" ]; then
		cmd="$cmd$ALLOW_HOSTS"
	fi
	echo "$cmd"
}

if [ -n "$UNINSTALL" ]; then
	# A --client-only uninstall removes the client and nothing else. It must
	# not drive `wt daemon uninstall`: this install registered no
	# coordinator, and inside a container the verb would refuse over the
	# host's registry entries — entries a container has no business
	# deciding about.
	if [ -n "$CLIENT_ONLY" ]; then
		if [ -n "$DRY_RUN" ]; then
			echo "would remove: $BINDIR/wt"
			echo "no coordinator was registered by a --client-only install; none is deregistered"
			echo "dry run: nothing was changed"
			exit 0
		fi
		rm -f "$BINDIR/wt"
		echo "removed wt from $BINDIR (no coordinator was registered by a --client-only install)"
		exit 0
	fi
	if [ -n "$DRY_RUN" ]; then
		# Print every action, change nothing.
		if [ -x "$BINDIR/wt" ]; then
			if [ -n "$REG_PREFIX" ]; then
				echo "would run: $BINDIR/wt daemon uninstall --prefix $REG_PREFIX"
			else
				echo "would run: $BINDIR/wt daemon uninstall"
			fi
		else
			echo "wt is not installed in $BINDIR; nothing to uninstall"
		fi
		echo "would remove: $BINDIR/wt $BINDIR/wtd"
		echo "the store at ${WT_HOME:-$HOME/.wt} is never removed"
		echo "dry run: nothing was changed"
		exit 0
	fi
	# Drive the verb first: it stops the coordinator, deregisters it and
	# removes the registration, and it refuses (exit 3, nothing changed)
	# while the registry still holds entries. Its exit code propagates.
	if [ -x "$BINDIR/wt" ]; then
		if [ -n "$REG_PREFIX" ]; then
			"$BINDIR/wt" daemon uninstall --prefix "$REG_PREFIX"
		else
			"$BINDIR/wt" daemon uninstall
		fi
	else
		echo "wt is not installed in $BINDIR; nothing to uninstall"
		echo "store: ${WT_HOME:-$HOME/.wt} — left alone (it is the only record of what is allocated on this machine)"
	fi
	rm -f "$BINDIR/wt" "$BINDIR/wtd"
	echo "removed wt and wtd from $BINDIR"
	exit 0
fi

# Verification first, before anything is copied. The dry run runs the same
# checks — they only read — and then prints every action without taking
# it. --skip-verify skips the check in both.
if [ -z "$SKIP_VERIFY" ]; then
	verify_binaries
else
	echo "skipped verification (--skip-verify)"
fi
version_line

if [ -n "$DRY_RUN" ]; then
	echo "would install $(echo $BINS | sed 's/ / and /') into $BINDIR"
	if [ -n "$CLIENT_ONLY" ]; then
		echo "client only: no coordinator is installed and nothing is registered"
		echo "the client reaches a coordinator through WT_ENDPOINT and WT_CLIENT_TOKEN"
		echo "dry run: nothing was changed"
		exit 0
	fi
	if [ -n "$ADDR" ]; then
		echo "address: $ADDR (as given)"
	else
		echo "address: a free port, chosen at install time"
	fi
	if [ -n "$CONTAINER_TOKEN" ]; then
		echo "container clients: admitted (a token is configured; it is never echoed)"
	else
		echo "container clients: not admitted (no --container-token)"
	fi
	echo "would run: $(supervisor_command)"
	if [ -z "$REG_PREFIX" ]; then
		echo "would run: $BINDIR/wt claude install --refresh-only"
	fi
	echo "dry run: nothing was changed"
	exit 0
fi

mkdir -p "$BINDIR"
# Copy to a temporary name in the same directory, then rename into place.
# A straight `cp` over a running coordinator rewrites the image underneath
# the live process — it happens to succeed on macOS, which is worse rather
# than better, because the daemon is left executing a file that changed
# under it. rename(2) over a running executable is safe and atomic: the
# running process keeps the old inode until it exits.
for bin in $BINS; do
	tmp="$BINDIR/.$bin.tmp.$$"
	cp "$SCRIPT_DIR/$bin" "$tmp"
	chmod 755 "$tmp"
	mv -f "$tmp" "$BINDIR/$bin"
done
echo "installed $(echo $BINS | sed 's/ / and /') into $BINDIR"

# A client-only install stops here: there is no coordinator to register,
# and the endpoint the client talks to is given at run time by the two
# environment variables rather than pinned at install time. Saying so is
# the whole of the guidance a container image needs.
if [ -n "$CLIENT_ONLY" ]; then
	echo "client only: no coordinator was installed and nothing was registered"
	echo "point the client at one with WT_ENDPOINT=http://<host>:<port> and WT_CLIENT_TOKEN=<token>"
	echo "verify with: wt daemon status"
	case ":$PATH:" in
	*":$BINDIR:"*) ;;
	*) echo "note: $BINDIR is not on your PATH; add it (e.g. export PATH=\"$BINDIR:\$PATH\")" ;;
	esac
	exit 0
fi

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
if [ -n "$ALLOW_HOSTS" ]; then
	COORD_ARGS="$COORD_ARGS$ALLOW_HOSTS"
fi
if [ -n "$REG_PREFIX" ]; then
	"$BINDIR/wt" daemon install --prefix "$REG_PREFIX" --wtd "$BINDIR/wtd" $COORD_ARGS
else
	"$BINDIR/wt" daemon install --wtd "$BINDIR/wtd" $COORD_ARGS
fi
if [ -n "$CONTAINER_TOKEN" ] && [ -z "$REG_PREFIX" ]; then
	echo "container clients admitted; a container sets WT_ENDPOINT to the address above and WT_CLIENT_TOKEN to the token"
fi

# Bring an existing Claude Code integration up to date. The hook scripts
# and the onboarding skill are embedded in the binary, so a new wt carries
# new copies of both while the ones on disk stay at whatever version wrote
# them; without this an upgrade never reaches them. --refresh-only creates
# no installation — registering the hooks is the user's opt-in, made by
# `wt claude install`, and an upgrade does not make it for them — and it
# leaves a file the user has edited alone. A --prefix install is
# self-contained and loads nothing, so it does not touch ~/.claude. A
# failure here is reported and does not fail the install: the binaries are
# already in place and the coordinator is already registered.
if [ -z "$REG_PREFIX" ]; then
	"$BINDIR/wt" claude install --refresh-only ||
		echo "note: could not refresh the Claude Code hooks; run '$BINDIR/wt claude install' by hand"
fi

echo "verify with: wt daemon status"
case ":$PATH:" in
*":$BINDIR:"*) ;;
*) echo "note: $BINDIR is not on your PATH; add it (e.g. export PATH=\"$BINDIR:\$PATH\")" ;;
esac
