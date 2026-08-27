#!/bin/sh
# bundle.sh — assembles WorktreeMenu.app from the built WorktreeMenu binary
# and Info.plist, ad-hoc signed so it launches on this machine.
#
# Usage: macapp/Scripts/bundle.sh [--configuration debug|release]
#
# This is the phase-1 bundling step (macapp/PLAN.md's acceptance: "Run
# Scripts/bundle.sh, open the app, see entries"). Ad-hoc signing
# (codesign -s -) is enough for a local build to run without a Gatekeeper
# quarantine prompt. A distributable, notarized build with a Developer ID
# identity is Phase 4's Scripts/sign.sh, which this script does not
# attempt.
set -eu

cd "$(dirname "$0")/.." # macapp/
ROOT="$(pwd)"

CONFIGURATION="debug"
while [ $# -gt 0 ]; do
	case "$1" in
	--configuration)
		CONFIGURATION="$2"
		shift 2
		;;
	--configuration=*)
		CONFIGURATION="${1#--configuration=}"
		shift
		;;
	*)
		echo "bundle.sh: unrecognised argument: $1" >&2
		exit 2
		;;
	esac
done

case "$CONFIGURATION" in
debug | release) ;;
*)
	echo "bundle.sh: --configuration must be debug or release, got: $CONFIGURATION" >&2
	exit 2
	;;
esac

echo "building WorktreeMenu ($CONFIGURATION)..."
swift build --configuration "$CONFIGURATION"

BIN_PATH="$(swift build --configuration "$CONFIGURATION" --show-bin-path)"
BINARY="$BIN_PATH/WorktreeMenu"
if [ ! -x "$BINARY" ]; then
	echo "bundle.sh: built binary not found at $BINARY" >&2
	exit 1
fi

APP="$ROOT/.build/WorktreeMenu.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cp "$BINARY" "$APP/Contents/MacOS/WorktreeMenu"
cp "$ROOT/Resources/Info.plist" "$APP/Contents/Info.plist"

echo "ad-hoc signing..."
codesign --force --sign - "$APP"

echo "assembled: $APP"
