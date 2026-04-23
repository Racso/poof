#!/bin/sh
# Poof! installer — downloads the binary and sets up either the CLI or a server.
#
# Interactive:  curl -fsSL https://poof.rac.so/install | sh
# CLI only:     curl -fsSL https://poof.rac.so/install | sh -s client
# Server:       curl -fsSL https://poof.rac.so/install | sh -s server
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

# Determine mode: from argument, or ask interactively.
MODE="${1:-}"
if [ -z "$MODE" ]; then
  echo ""
  echo "  What would you like to install?"
  echo ""
  echo "    c) CLI client — for deploying to an existing Poof! server"
  echo "    s) Server     — sets up Caddy, Docker image, and starts Poof! on this machine"
  echo ""
  printf "  Choose [c/s]: "
  read -r CHOICE </dev/tty
  case "$CHOICE" in
    c|C|client)  MODE="client" ;;
    s|S|server)  MODE="server" ;;
    *) echo "Invalid choice. Run again and pick c or s."; exit 1 ;;
  esac
fi

case "$MODE" in
  client)
    DIR="${INSTALL_DIR:-/usr/local/bin}"
    if [ -w "$DIR" ]; then mv /tmp/poof "$DIR/poof"; else sudo mv /tmp/poof "$DIR/poof"; fi
    echo "Poof! installed to ${DIR}/poof"
    echo "Run 'poof --help' to get started."
    ;;
  server)
    /tmp/poof install --yes && rm -f /tmp/poof || { echo ""; echo "Installation NOT completed. Fix any reported issues, then re-run this script."; exit 1; }
    ;;
  *)
    echo "Unknown mode: $MODE (expected 'client' or 'server')"
    exit 1
    ;;
esac
