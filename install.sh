#!/bin/sh
# Poof! installer — downloads the latest binary for the current platform.
# Usage: curl -fsSL https://raw.githubusercontent.com/racso/poof/main/install.sh | sh

set -e

REPO="racso/poof"
INSTALL_DIR="/usr/local/bin"
BINARY="poof"

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

# Fetch latest release tag
echo "Fetching latest Poof! release..."
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | sed 's/.*"tag_name": *"\(.*\)".*/\1/')

if [ -z "$LATEST" ]; then
  echo "Could not determine latest release."
  exit 1
fi

URL="https://github.com/${REPO}/releases/download/${LATEST}/poof-${OS}-${ARCH}"
echo "Downloading Poof! ${LATEST} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o "/tmp/poof"
chmod +x /tmp/poof

# Install
if [ -w "$INSTALL_DIR" ]; then
  mv /tmp/poof "${INSTALL_DIR}/${BINARY}"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv /tmp/poof "${INSTALL_DIR}/${BINARY}"
fi

echo "Poof! ${LATEST} installed to ${INSTALL_DIR}/${BINARY}"
echo "Run 'poof --help' to get started."
