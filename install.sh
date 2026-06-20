#!/bin/sh
# One-line install for cc-gateway:
#   curl -fsSL https://raw.githubusercontent.com/Kotrotsos/cc-gateway/main/install.sh | sh
#
# Downloads the prebuilt binary for your OS/arch from the latest GitHub release
# and installs it to BINDIR (default /usr/local/bin). Windows users: use Docker.
set -e

REPO="Kotrotsos/cc-gateway"
BINDIR="${BINDIR:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "cc-gateway: unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux | darwin) ;;
  *) echo "cc-gateway: unsupported OS: $os (use the Docker image instead)" >&2; exit 1 ;;
esac

asset="cc-gateway-${os}-${arch}"
url="https://github.com/${REPO}/releases/latest/download/${asset}"

echo "Downloading ${url}"
tmp=$(mktemp)
curl -fSL "$url" -o "$tmp"
chmod +x "$tmp"

if [ -w "$BINDIR" ]; then
  mv "$tmp" "$BINDIR/cc-gateway"
else
  echo "Installing to $BINDIR (needs sudo)"
  sudo mv "$tmp" "$BINDIR/cc-gateway"
fi

echo
echo "Installed: $BINDIR/cc-gateway"
echo "Start it:  cc-gateway"
echo "Then:      export ANTHROPIC_BASE_URL=http://localhost:8443 && claude"
echo "UI:        http://localhost:8088"
