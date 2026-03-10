#!/bin/sh
set -e

REPO="Mojashi/claude-conversation-editor"
BIN_DIR="${SURGERY_BIN_DIR:-$HOME/bin}"
APP_DIR="${SURGERY_APP_DIR:-/Applications}"

# Detect arch
ARCH=$(uname -m)
case $ARCH in
  arm64) ASSET="surgery-darwin-arm64.zip" ;;
  x86_64) ASSET="surgery-darwin-amd64.zip" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

echo "Installing Surgery..."

# Get latest release URL
URL=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep "browser_download_url" \
  | grep "$ASSET" \
  | sed 's/.*"browser_download_url": "\(.*\)"/\1/')

if [ -z "$URL" ]; then
  echo "Failed to find release asset: $ASSET" >&2
  exit 1
fi

# Download and extract
TMP=$(mktemp -d)
trap "rm -rf $TMP" EXIT

echo "Downloading $ASSET..."
curl -fsSL "$URL" -o "$TMP/surgery.zip"
unzip -q "$TMP/surgery.zip" -d "$TMP"

# Install .app
APP=$(find "$TMP" -name "*.app" -maxdepth 1 | head -1)
if [ -z "$APP" ]; then
  echo "No .app found in zip" >&2
  exit 1
fi

rm -rf "$APP_DIR/surgery.app"
cp -r "$APP" "$APP_DIR/surgery.app"
echo "Installed surgery.app to $APP_DIR"

# Symlink binary
mkdir -p "$BIN_DIR"
ln -sf "$APP_DIR/surgery.app/Contents/MacOS/surgery" "$BIN_DIR/surgery"
echo "Linked surgery command to $BIN_DIR/surgery"

echo ""
echo "Done! Run: surgery"
