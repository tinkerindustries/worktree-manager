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
#
# Every archive nests its contents under one directory named for the
# archive — wt-0.3.0-darwin-arm64/ — so unpacking one puts five files in a
# directory of their own rather than scattering them across whatever
# directory the caller is standing in. The installers resolve everything
# relative to their own location, so the nesting is invisible to them.
#
# Each archive carries wt and wtd side by side (the shape
# CoordinatorBinaryPath expects — `wt daemon install` finds wtd next to
# wt) and the installer that drives `wt daemon install`. Nothing else is
# shipped: no package manager channels, no signing, no self-update
# (PLAN-SCOPE.md non-goals).
set -eu

cd "$(dirname "$0")/.." # the repository root
ROOT="$(pwd)"

# The version is a semantic version (semver.org 2.0.0) without the tag's
# leading "v": 0.2.0, 1.0.0-rc.1, 0.3.0-dev.7+gabc1234. The release tag is
# that string with a v in front, which is what the release workflow passes
# here. Anything else is refused rather than stamped into a binary —
# `wt --version` is the only thing a user has to tell two builds apart,
# and a bare commit hash does not order against anything.
SEMVER='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'

# derive_version answers "what is this checkout, in semver terms" when the
# caller named no version. An exact tag is that release. A commit past the
# nearest tag is a development build named after the tag it descends from,
# so it sorts *below* the next release and above the last one. No tags at
# all is 0.0.0-dev, which sorts below everything.
derive_version() {
	if desc="$(git describe --tags --match 'v[0-9]*' --exact-match 2>/dev/null)"; then
		printf '%s' "${desc#v}"
		return
	fi
	if desc="$(git describe --tags --match 'v[0-9]*' --long 2>/dev/null)"; then
		base="${desc%-*-g*}"    # v0.2.0-7-gabc1234 -> v0.2.0
		rest="${desc#"$base"-}" # 7-gabc1234
		printf '%s-dev.%s+g%s' "${base#v}" "${rest%-g*}" "${rest#*-g}"
		return
	fi
	printf '0.0.0-dev+g%s' "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
}

VERSION="${1:-}"
VERSION="${VERSION#v}" # a tag may be passed whole; the version is its tail
if [ -z "$VERSION" ]; then
	VERSION="$(derive_version)"
fi
if ! printf '%s' "$VERSION" | grep -Eq "$SEMVER"; then
	echo "build.sh: \"$VERSION\" is not a semantic version (MAJOR.MINOR.PATCH, optionally -prerelease and +build; see semver.org)" >&2
	echo "build.sh: pass one, e.g. dist/build.sh 0.2.0, or pass nothing to derive it from the nearest v* tag" >&2
	exit 2
fi
# The commit the binaries were built from, for `wt --version` / `wtd
# --version`. Both binaries carry the same version and commit — they ship
# together. The client's variables live in internal/cli (addressed by
# their import path); the coordinator's live in its own main package,
# whose symbols the linker knows as main.version/main.commit. Each keeps
# its default as the fallback when the flag is not set.
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
LDFLAGS="-X github.com/mrgeoffrich/worktree-manager/internal/cli.version=$VERSION \
-X github.com/mrgeoffrich/worktree-manager/internal/cli.commit=$COMMIT \
-X main.version=$VERSION -X main.commit=$COMMIT"

# WT_CELLS restricts which platform cells this run assembles, as a
# space-separated list of <os>/<arch>. The darwin archives are assembled
# on a macOS runner because the menu bar app is a Swift build and cannot
# be cross-compiled, while the linux and windows archives are assembled on
# Linux; the two runs publish one release between them, each writing a
# SHA256SUMS over the archives it produced. Unset, every cell is built,
# which is what a local dist/build.sh does.
CELLS="${WT_CELLS:-darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64}"

# WT_MACAPP_BUNDLE names a built WorktreeMenu.app to ship inside the
# darwin archives, so one download installs the client, the coordinator
# and the menu bar app. The bundle is built and signed before this script
# runs — macapp/Scripts/bundle.sh, then macapp/Scripts/sign.sh where a
# Developer ID is configured — and this script copies it without signing
# it. Unset, the darwin archives carry the two binaries alone, as every
# release before this one did.
MACAPP_BUNDLE="${WT_MACAPP_BUNDLE:-}"
if [ -n "$MACAPP_BUNDLE" ] && [ ! -d "$MACAPP_BUNDLE" ]; then
	echo "build.sh: WT_MACAPP_BUNDLE names $MACAPP_BUNDLE, which is not a directory; point it at a built WorktreeMenu.app" >&2
	exit 2
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
  SHA256SUMS                 the sha256 of each binary; install.sh verifies
                             the binaries against it before copying
                             anything (--skip-verify overrides)
  install.sh                 the unix installer (install.ps1 on Windows)
  WorktreeMenu.app           the macOS menu bar app (macOS archives only);
                             install.sh puts it in /Applications and
                             starts it, and --no-menubar skips it

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
  ./install.sh --addr 127.0.0.1:7833
                                # pin the coordinator's listen address
                                # (default: a free port chosen at install)
  ./install.sh --container-token <token>
                                # admit container clients with this token;
                                # a container sets WT_ENDPOINT and
                                # WT_CLIENT_TOKEN to reach the coordinator

  ./install.sh --client-only    # install the wt client alone: no wtd, no
                                # supervisor registration. This is the
                                # container install — the client reaches
                                # the host's coordinator over WT_ENDPOINT
                                # and WT_CLIENT_TOKEN.

  ./install.sh --dry-run        # print every action, change nothing
  ./install.sh --uninstall      # stop the coordinator, remove the
                                # registration and the two binaries; the
                                # store is never removed

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
	# The trap is the abnormal-exit path; the normal path removes the stage
	# at the end of this function, because a single EXIT trap can only name
	# the last stage and the earlier cells' directories would otherwise
	# survive the build.
	trap 'rm -rf "$STAGE"' EXIT

	# NAME is both the archive's basename and the one directory every path
	# inside it sits under, so `tar xzf wt-<version>-<os>-<arch>.tar.gz`
	# is followed by `cd wt-<version>-<os>-<arch>`.
	NAME="wt-$VERSION-$GOOS-$GOARCH"
	DIR="$STAGE/$NAME"
	mkdir -p "$DIR"

	CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "$LDFLAGS" -o "$DIR/wt$BINEXT" ./cmd/wt
	CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "$LDFLAGS" -o "$DIR/wtd$BINEXT" ./cmd/wtd
	cp "$OUT/README.txt" "$DIR/README.txt"

	# The menu bar app rides in the darwin archives when one was built and
	# handed to this script. It is a bundle directory rather than a file, so
	# it is copied whole and named as a directory in the archive file list
	# below. Its integrity is its own code signature, which install.sh
	# checks with codesign, rather than a line in SHA256SUMS: a signature
	# covers the whole bundle and says who signed it, which a digest of one
	# file inside it does not.
	if [ "$GOOS" = "darwin" ] && [ -n "$MACAPP_BUNDLE" ]; then
		cp -R "$MACAPP_BUNDLE" "$DIR/WorktreeMenu.app"
	fi

	# Optional: sign the two darwin binaries with a Developer ID
	# Application identity, the same kind macapp/Scripts/sign.sh signs the
	# menu bar app with. Off by default and inert unless
	# WT_DARWIN_SIGNING_IDENTITY is set — with no identity configured this
	# whole block does not run, so the archives it produces are unchanged
	# (build.sh's output is byte-identical with and without this addition
	# when the variable is unset). A browser-downloaded release archive is
	# quarantined by Gatekeeper, and wtd — a resident process that
	# registers itself with launchd — is exactly the shape Gatekeeper is
	# suspicious of, which is the motivation for offering this at all.
	if [ "$GOOS" = "darwin" ] && [ -n "${WT_DARWIN_SIGNING_IDENTITY:-}" ]; then
		if ! command -v codesign >/dev/null 2>&1; then
			echo "build.sh: WT_DARWIN_SIGNING_IDENTITY is set but codesign is not on this machine" >&2
			exit 1
		fi
		echo "signing wt$BINEXT and wtd$BINEXT for $GOOS/$GOARCH..."
		codesign --options runtime --timestamp --sign "$WT_DARWIN_SIGNING_IDENTITY" "$DIR/wt$BINEXT"
		codesign --options runtime --timestamp --sign "$WT_DARWIN_SIGNING_IDENTITY" "$DIR/wtd$BINEXT"
	fi

	# The archive's own manifest: a SHA256SUMS covering the two binaries,
	# which the installer verifies against before copying anything. It sits
	# inside the archive — the outside manifest covers the archives
	# themselves — because an archive without one is not a distribution
	# this installer will install (install.sh refuses, naming --skip-verify
	# as the deliberate override). sha256sum on Linux, shasum -a 256 on
	# macOS.
	# The entries name the binaries alone, never the enclosing directory:
	# the installer verifies files beside itself, and it is inside that
	# directory when it runs.
	if command -v sha256sum >/dev/null 2>&1; then
		(cd "$DIR" && sha256sum "wt$BINEXT" "wtd$BINEXT") >"$DIR/SHA256SUMS"
	else
		(cd "$DIR" && for f in "wt$BINEXT" "wtd$BINEXT"; do
			printf '%s  %s\n' "$(shasum -a 256 "$f" | awk '{print $1}')" "$f"
		done) >"$DIR/SHA256SUMS"
	fi

	case "$EXT" in
	tar.gz)
		cp "$ROOT/dist/install.sh" "$DIR/install.sh"
		chmod 755 "$DIR/install.sh"
		# The file list stays explicit rather than archiving the directory
		# whole: the archive holds what this script put there and nothing
		# a stray file in the stage could add.
		# The file list stays explicit; the menu bar app joins it only when
		# this cell staged one.
		set -- "$NAME/wt$BINEXT" "$NAME/wtd$BINEXT" \
			"$NAME/install.sh" "$NAME/README.txt" "$NAME/SHA256SUMS"
		if [ -d "$DIR/WorktreeMenu.app" ]; then
			set -- "$@" "$NAME/WorktreeMenu.app"
		fi
		tar -C "$STAGE" -czf "$OUT/$NAME.tar.gz" "$@"
		;;
	zip)
		cp "$ROOT/dist/install.ps1" "$DIR/install.ps1"
		go run "$ROOT/dist/ziphelper.go" "$OUT/$NAME.zip" \
			"$DIR" "$NAME" "wt$BINEXT" "wtd$BINEXT" install.ps1 README.txt SHA256SUMS
		;;
	esac

	rm -rf "$STAGE"
}

# linux/arm64 is not only a Linux desktop cell: it is the architecture a
# container built on an Apple Silicon machine runs, so the client that goes
# into a local Docker image comes from here (RELEASE.md, "Container
# clients"). Both linux cells therefore ship on every release.
# The cells this run assembles come from CELLS, so a release can split
# them across two runners and still produce one distribution.
for cell in $CELLS; do
	case "$cell" in
	windows/*) stage_for "${cell%/*}" "${cell#*/}" zip .exe ;;
	*/*) stage_for "${cell%/*}" "${cell#*/}" tar.gz "" ;;
	*)
		echo "build.sh: WT_CELLS entry \"$cell\" is not <os>/<arch>" >&2
		exit 2
		;;
	esac
done

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
