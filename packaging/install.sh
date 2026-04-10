#!/bin/sh
# vibe-palace installer — detects OS/arch, downloads release, verifies checksum.
set -e

REPO="suykerbuyk/vibe-palace"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS and architecture.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
    linux|darwin) ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Get latest version.
if command -v curl >/dev/null 2>&1; then
    FETCH="curl -fsSL"
elif command -v wget >/dev/null 2>&1; then
    FETCH="wget -qO-"
else
    echo "curl or wget required"; exit 1
fi

VERSION=$($FETCH "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed 's/.*"v\(.*\)".*/\1/')
if [ -z "$VERSION" ]; then
    echo "Failed to determine latest version"; exit 1
fi

ARCHIVE="vp_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/v${VERSION}/${ARCHIVE}"
CHECKSUM_URL="https://github.com/$REPO/releases/download/v${VERSION}/checksums.txt"

echo "Installing vp v${VERSION} (${OS}/${ARCH})..."

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

$FETCH "$URL" > "$TMPDIR/$ARCHIVE"
$FETCH "$CHECKSUM_URL" > "$TMPDIR/checksums.txt"

# Verify checksum.
EXPECTED=$(grep "$ARCHIVE" "$TMPDIR/checksums.txt" | awk '{print $1}')
if [ -n "$EXPECTED" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
        ACTUAL=$(sha256sum "$TMPDIR/$ARCHIVE" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        ACTUAL=$(shasum -a 256 "$TMPDIR/$ARCHIVE" | awk '{print $1}')
    else
        echo "Warning: no sha256 tool found, skipping checksum verification"
        ACTUAL="$EXPECTED"
    fi
    if [ "$EXPECTED" != "$ACTUAL" ]; then
        echo "Checksum mismatch! Expected: $EXPECTED Got: $ACTUAL"
        exit 1
    fi
fi

# Extract and install.
tar xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR"
mkdir -p "$INSTALL_DIR"
mv "$TMPDIR/vp" "$INSTALL_DIR/vp"
chmod +x "$INSTALL_DIR/vp"

echo "Installed vp v${VERSION} to $INSTALL_DIR/vp"

# Check if INSTALL_DIR is in PATH.
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "Note: add $INSTALL_DIR to your PATH" ;;
esac
