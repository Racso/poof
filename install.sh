#!/bin/sh
# Poof! installer — downloads the latest binary for the current platform.
#
# Usage:
#   curl -fsSL https://poof.rac.so/install | sh           — install CLI only
#   curl -fsSL https://poof.rac.so/install | sh -s server — install + build Docker image + create config
#
# Environment variables (optional):
#   POOF_VERSION   — specific version to install (e.g. "v0.10.0"); defaults to latest
#   INSTALL_DIR    — where to put the binary (default: /usr/local/bin)

set -e

REPO="racso/poof"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY="poof"
MODE="${1:-cli}"

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

# Resolve version
if [ -n "$POOF_VERSION" ]; then
  TAG="$POOF_VERSION"
  # Ensure v prefix
  case "$TAG" in v*) ;; *) TAG="v$TAG" ;; esac
  echo "Using Poof! ${TAG}..."
else
  echo "Fetching latest Poof! release..."
  TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"tag_name": *"\(.*\)".*/\1/')
  if [ -z "$TAG" ]; then
    echo "Could not determine latest release."
    exit 1
  fi
fi

# Download the binary
URL="https://github.com/${REPO}/releases/download/${TAG}/poof-${OS}-${ARCH}"
echo "Downloading Poof! ${TAG} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o "/tmp/poof"
chmod +x /tmp/poof

# Install binary
if [ -w "$INSTALL_DIR" ]; then
  mv /tmp/poof "${INSTALL_DIR}/${BINARY}"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv /tmp/poof "${INSTALL_DIR}/${BINARY}"
fi

echo "Poof! ${TAG} installed to ${INSTALL_DIR}/${BINARY}"

# --- CLI-only mode stops here ---
if [ "$MODE" = "cli" ]; then
  echo "Run 'poof --help' to get started."
  exit 0
fi

# --- Server mode: build Docker image and configure ---
if [ "$MODE" != "server" ]; then
  echo "Unknown mode: $MODE (expected 'cli' or 'server')"
  exit 1
fi

echo ""
echo "==> Setting up Poof! server..."

# Check Docker is available
if ! command -v docker >/dev/null 2>&1; then
  echo "Error: Docker is required but not installed."
  exit 1
fi

# Build a minimal Docker image from the downloaded binary
echo "Building Docker image poof:${TAG}..."
TMPDIR=$(mktemp -d)
cp "${INSTALL_DIR}/${BINARY}" "${TMPDIR}/poof"
cat > "${TMPDIR}/Dockerfile" <<'DOCKERFILE'
FROM docker:27-cli
COPY poof /usr/local/bin/poof
ENTRYPOINT ["poof"]
CMD ["server"]
DOCKERFILE
docker build -t "poof:${TAG}" -t poof:latest "${TMPDIR}" -q
rm -rf "${TMPDIR}"
echo "Docker image built: poof:latest (${TAG})"

# Create config directory and default config if missing
if [ ! -f /etc/poof/poof.toml ]; then
  mkdir -p /etc/poof
  TOKEN=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
  cat > /etc/poof/poof.toml <<EOF
token = "${TOKEN}"
EOF
  echo ""
  echo "Created /etc/poof/poof.toml with a random token."
  echo "Your API token is: ${TOKEN}"
  echo "(Save this — you'll need it to configure the CLI.)"
else
  echo "Config already exists at /etc/poof/poof.toml — skipping."
fi

# Create data directory
mkdir -p /var/lib/poof

# Create Docker network if it doesn't exist
if ! docker network inspect poof-net >/dev/null 2>&1; then
  docker network create poof-net
  echo "Created Docker network: poof-net"
fi

echo ""
echo "==> Done! To start the server:"
echo ""
echo "  docker run -d \\"
echo "    --name poof \\"
echo "    --restart always \\"
echo "    --network poof-net \\"
echo "    -v /etc/poof/poof.toml:/etc/poof/poof.toml:ro \\"
echo "    -v /var/lib/poof:/var/lib/poof \\"
echo "    -v /var/run/docker.sock:/var/run/docker.sock \\"
echo "    poof:latest"
echo ""
echo "Make sure Caddy is on the same poof-net network with:"
echo "  - admin API enabled (admin 0.0.0.0:2019)"
echo "  - static volume:    -v /var/lib/poof/static:/var/lib/poof/static:ro"
