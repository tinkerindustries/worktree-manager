#!/bin/sh
# build.sh — assembles the phase-9 distribution: one archive per platform
# containing both binaries plus the platform's installer, built from the
# one module with CGO_ENABLED=0, plus a SHA256SUMS manifest.
#
# Usage: dist/build.sh [version]
#
# The version defaults to the nearest git tag (or 0.0.0-dev outside a
# checkout). A release cuts by tagging v0.x.y and letting the release
# workflow run this script (RELEASE.md, "How a release is cut").
#
# The archive format per platform: tar.gz for macOS and Linux (bsdtar on
# macOS and Windows 10+ reads them), zip for Windows (built by the
# stdlib-only dist/ziphelper.go, so no `zip` binary is needed anywhere).
# Each archive carries wt and wtd side by side (the shape
# CoordinatorBinaryPath expects — `wt daemon install` finds wtd next to
# wt) and the installer that drives `wt daemon install`. Nothing else is
# shipped: no package manager channels, no signing, no self-update
# (PLAN-SCOPE.md non-goals).
set -eu

cd "$(dirname "$0")/.." # the repository root
ROOT="$(pwd)"

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
	VERSION="$(git describe --tags --always 2>/dev/null || echo 0.0.0-dev)"
fi

OUT="$ROOT/dist/out"
rm -rf "$OUT"
mkdir -p "$OUT"

cat >"$OUT/README.txt" <<EOF
Worktree Manager $VERSION

One distribution per platform containing both binaries — the wt client and
the wtd coordinator — built from the one Go module with CGO_ENABLED=0, so
nothing else needs installing. This archive contains:

  wt, wtd                    the two binaries (wt.exe, wtd.exe on Windows)
  install.sh                 the unix installer (install.ps1 on Windows)

Install:

  tar xzf wt-$VERSION-<os>-<arch>.tar.gz
  cd wt-$VERSION-<os>-<arch>
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
EOF

stage_for() { # goos goarch ext bin-ext
	GOOS="$1"
	GOARCH="$2"
	EXT="$3"
	BINEXT="$4"
	STAGE="$(mktemp -d)"
	trap 'rm -rf "$STAGE"' EXIT

	CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -o "$STAGE/wt$BINEXT" ./cmd/wt
	CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -o "$STAGE/wtd$BINEXT" ./cmd/wtd
	cp "$OUT/README.txt" "$STAGE/README.txt"

	case "$EXT" in
	tar.gz)
		cp "$ROOT/dist/install.sh" "$STAGE/install.sh"
		chmod 755 "$STAGE/install.sh"
		tar -C "$STAGE" -czf "$OUT/wt-$VERSION-$GOOS-$GOARCH.tar.gz" \
			"wt$BINEXT" "wtd$BINEXT" install.sh README.txt
		;;
	zip)
		cp "$ROOT/dist/install.ps1" "$STAGE/install.ps1"
		go run "$ROOT/dist/ziphelper.go" "$OUT/wt-$VERSION-$GOOS-$GOARCH.zip" \
			"$STAGE" "wt$BINEXT" "wtd$BINEXT" install.ps1 README.txt
		;;
	esac
}

build_cell() { # goos goarch
	stage_for "$1" "$2" tar.gz ""
}

build_cell darwin arm64
build_cell darwin amd64
build_cell linux amd64
stage_for windows amd64 zip .exe

# The manifest: enough for a person (or a script) to verify every archive
# byte-for-byte before installing it. sha256sum on Linux, shasum -a 256 on
# macOS.
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$OUT" && sha256sum wt-*) >"$OUT/SHA256SUMS"
else
	(cd "$OUT" && for f in wt-*; do
		printf '%s  %s\n' "$(shasum -a 256 "$f" | awk '{print $1}')" "$f"
	done) >"$OUT/SHA256SUMS"
fi

echo "distribution: $OUT"
ls -1 "$OUT"
