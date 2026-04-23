#!/bin/sh
# Poof! installer — downloads the binary and optionally sets up a server.
#
# CLI only:  curl -fsSL https://poof.rac.so/install | sh
# Server:    curl -fsSL https://poof.rac.so/install | sh -s server
set -e

REPO="racso/poof"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in x86_64) ARCH="amd64" ;; aarch64|arm64) ARCH="arm64" ;; *) echo "Unsupported: $ARCH"; exit 1 ;; esac

# Reuse a recent download (less than 1 hour old) to avoid re-downloading on retry.
if [ -x /tmp/poof ] && [ "$(find /tmp/poof -mmin -60 2>/dev/null)" ]; then
  echo "Using cached binary at /tmp/poof"
else
  TAG=${POOF_VERSION:-$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed 's/.*"\(v[^"]*\)".*/\1/')}
  [ -z "$TAG" ] && echo "Could not determine latest release." && exit 1
  echo "Downloading Poof! ${TAG} (${OS}/${ARCH})..."
  curl -fsSL "https://github.com/${REPO}/releases/download/${TAG}/poof-${OS}-${ARCH}" -o /tmp/poof
  chmod +x /tmp/poof
fi

if [ "${1:-}" = "server" ]; then
  /tmp/poof install --yes && rm -f /tmp/poof || { echo ""; echo "Installation NOT completed. Fix any reported issues, then re-run this script."; exit 1; }
else
  DIR="${INSTALL_DIR:-/usr/local/bin}"
  if [ -w "$DIR" ]; then mv /tmp/poof "$DIR/poof"; else sudo mv /tmp/poof "$DIR/poof"; fi
  echo "Poof! ${TAG} installed to ${DIR}/poof"
  echo "Run 'poof --help' to get started."
fi
