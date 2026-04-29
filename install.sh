#!/bin/bash
set -euo pipefail

VERSION="${1:-latest}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
esac

REPO="cdotlock/mob-sandbox"
BASE="https://github.com/$REPO/releases/download/$VERSION"

echo "Installing mob-sandbox ($OS/$ARCH)..."

for bin in mob mob-server; do
  URL="$BASE/${bin}-${OS}-${ARCH}"
  echo "  Downloading $bin..."
  curl -fsSL "$URL" -o "/usr/local/bin/$bin"
  chmod +x "/usr/local/bin/$bin"
done

echo "Done. Run 'mob-server init' on your server or 'mob init' on your laptop."
