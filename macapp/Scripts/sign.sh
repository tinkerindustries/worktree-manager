#!/bin/sh
# sign.sh — signs WorktreeMenu.app with a Developer ID Application
# identity, submits it to Apple's notary service, staples the ticket, and
# zips the stapled app as WorktreeMenu-<version>.zip.
#
# Usage: macapp/Scripts/sign.sh --version <version> [--app <path>] [--out <dir>] [--dry-run]
#
# This is the distributable half of Scripts/bundle.sh's ad-hoc signed
# local build (macapp/PLAN.md, phase 4): bundle.sh gets a build to launch
# on this machine; sign.sh gets it past Gatekeeper on someone else's.
#
# Every credential is read from the environment and never printed:
#
#   MACAPP_SIGNING_IDENTITY    the Developer ID Application identity
#                             codesign signs with, e.g. "Developer ID
#                             Application: Jane Doe (TEAMID1234)".
#   MACAPP_NOTARY_KEY_ID       the App Store Connect API key's key ID.
#   MACAPP_NOTARY_KEY_ISSUER   the API key's issuer ID.
#   MACAPP_NOTARY_KEY_PATH     path to the API key's .p8 private key file
#                             — the file, never its contents in an
#                             environment variable.
#
# All four are required for a real run; a missing one is a refusal naming
# it, not a silent skip. --dry-run prints every action this script would
# take without touching disk or network and without requiring any of them
# to be set, so this script's shape can be checked in an environment with
# no Developer ID certificate and no App Store Connect key — which is
# exactly the environment this was written in. The end-to-end signing and
# notarization run is unverified here.
set -eu

cd "$(dirname "$0")/.." # macapp/
ROOT="$(pwd)"

usage() {
	cat <<'EOF'
usage: sign.sh --version <version> [--app <path>] [--out <dir>] [--dry-run]

  --version <version>   the version to put in the zip's name
                        (WorktreeMenu-<version>.zip). Required.
  --app <path>          the .app bundle to sign (default:
                        .build/WorktreeMenu.app, what
                        Scripts/bundle.sh produces).
  --out <dir>           where to write WorktreeMenu-<version>.zip
                        (default: .build).
  --dry-run             print every action and change nothing. Does not
                        require any credential to be set, so it runs in
                        an environment with none configured.
  -h, --help            this help.

Credentials, read from the environment and never printed:

  MACAPP_SIGNING_IDENTITY    the Developer ID Application identity
                             codesign should sign with, e.g.
                             "Developer ID Application: Jane Doe (TEAMID1234)".
  MACAPP_NOTARY_KEY_ID       the App Store Connect API key's key ID.
  MACAPP_NOTARY_KEY_ISSUER   the API key's issuer ID.
  MACAPP_NOTARY_KEY_PATH     path to the API key's .p8 private key file
                             (the file, never its contents in an
                             environment variable).

All four are required for a real run. A missing one is a refusal naming
it, not a silent skip.
EOF
}

APP="$ROOT/.build/WorktreeMenu.app"
OUT="$ROOT/.build"
VERSION=""
DRY_RUN=""

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || { echo "sign.sh: --version needs an argument" >&2; exit 2; }
		VERSION="$2"
		shift 2
		;;
	--app)
		[ $# -ge 2 ] || { echo "sign.sh: --app needs an argument" >&2; exit 2; }
		APP="$2"
		shift 2
		;;
	--out)
		[ $# -ge 2 ] || { echo "sign.sh: --out needs an argument" >&2; exit 2; }
		OUT="$2"
		shift 2
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "sign.sh: unknown option: $1" >&2
		usage
		exit 2
		;;
	esac
done

if [ -z "$VERSION" ]; then
	echo "sign.sh: --version is required, e.g. --version 0.2.0" >&2
	exit 2
fi

ZIP="$OUT/WorktreeMenu-$VERSION.zip"

# The four *_status helpers report only whether a credential is set, never
# its value — dry-run's whole point is showing what this script needs
# without requiring it, so this is what it checks each of the four names
# against. Written as one function per name, rather than a loop over
# indirect variable names, because POSIX sh has no arrays and reading a
# variable by a name held in another variable needs eval.
identity_status() {
	if [ -n "${MACAPP_SIGNING_IDENTITY:-}" ]; then echo "configured"; else echo "not set"; fi
}
key_id_status() {
	if [ -n "${MACAPP_NOTARY_KEY_ID:-}" ]; then echo "configured"; else echo "not set"; fi
}
key_issuer_status() {
	if [ -n "${MACAPP_NOTARY_KEY_ISSUER:-}" ]; then echo "configured"; else echo "not set"; fi
}
key_path_status() {
	if [ -n "${MACAPP_NOTARY_KEY_PATH:-}" ]; then echo "configured"; else echo "not set"; fi
}

if [ -n "$DRY_RUN" ]; then
	echo "would sign: $APP"
	echo "  codesign --force --options runtime --timestamp --sign <MACAPP_SIGNING_IDENTITY> \"$APP\""
	echo "  MACAPP_SIGNING_IDENTITY: $(identity_status)"
	echo "would zip the signed app for submission and wait for Apple's notary service"
	echo "  xcrun notarytool submit <zipped app> --key <MACAPP_NOTARY_KEY_PATH> --key-id <MACAPP_NOTARY_KEY_ID> --issuer <MACAPP_NOTARY_KEY_ISSUER> --wait"
	echo "  MACAPP_NOTARY_KEY_ID: $(key_id_status)"
	echo "  MACAPP_NOTARY_KEY_ISSUER: $(key_issuer_status)"
	echo "  MACAPP_NOTARY_KEY_PATH: $(key_path_status)"
	echo "would staple the notarization ticket: xcrun stapler staple \"$APP\""
	echo "would zip the stapled app: $ZIP"
	echo "dry run: nothing was changed"
	exit 0
fi

# Real run: every credential must be present before anything is touched.
# Named individually rather than in a loop, so the refusal below can list
# exactly the names that are missing.
missing=""
[ -n "${MACAPP_SIGNING_IDENTITY:-}" ] || missing="$missing MACAPP_SIGNING_IDENTITY"
[ -n "${MACAPP_NOTARY_KEY_ID:-}" ] || missing="$missing MACAPP_NOTARY_KEY_ID"
[ -n "${MACAPP_NOTARY_KEY_ISSUER:-}" ] || missing="$missing MACAPP_NOTARY_KEY_ISSUER"
[ -n "${MACAPP_NOTARY_KEY_PATH:-}" ] || missing="$missing MACAPP_NOTARY_KEY_PATH"
if [ -n "$missing" ]; then
	echo "sign.sh: missing required credential(s):$missing" >&2
	echo "sign.sh: run with --dry-run to see what this script does without them" >&2
	exit 1
fi
if [ ! -f "$MACAPP_NOTARY_KEY_PATH" ]; then
	echo "sign.sh: MACAPP_NOTARY_KEY_PATH names a file that does not exist: $MACAPP_NOTARY_KEY_PATH" >&2
	exit 1
fi

if [ ! -d "$APP" ]; then
	echo "sign.sh: no app bundle at $APP (build one with Scripts/bundle.sh --configuration release, or pass --app)" >&2
	exit 1
fi

mkdir -p "$OUT"

echo "signing $APP..."
# --force replaces bundle.sh's ad-hoc signature; codesign otherwise
# refuses to resign an already-signed bundle. --options runtime enables
# the hardened runtime and --timestamp requests a secure timestamp — both
# required for Apple to notarize the result.
codesign --force --options runtime --timestamp --sign "$MACAPP_SIGNING_IDENTITY" "$APP"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
NOTARY_ZIP="$STAGE/WorktreeMenu-notarize.zip"
# ditto, not zip: it preserves the bundle's resource forks and extended
# attributes, which a plain zip can drop, and is what Apple's own
# notarization guide uses.
ditto -c -k --keepParent "$APP" "$NOTARY_ZIP"

echo "submitting to Apple's notary service and waiting (this can take several minutes)..."
xcrun notarytool submit "$NOTARY_ZIP" \
	--key "$MACAPP_NOTARY_KEY_PATH" \
	--key-id "$MACAPP_NOTARY_KEY_ID" \
	--issuer "$MACAPP_NOTARY_KEY_ISSUER" \
	--wait

echo "stapling the notarization ticket..."
xcrun stapler staple "$APP"

echo "zipping the stapled app..."
rm -f "$ZIP"
ditto -c -k --keepParent "$APP" "$ZIP"

echo "signed and notarized: $ZIP"
if command -v shasum >/dev/null 2>&1; then
	shasum -a 256 "$ZIP"
fi
