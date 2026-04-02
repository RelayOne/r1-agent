#!/usr/bin/env bash
set -euo pipefail

# Stoke installer
# Usage: curl -fsSL https://stoke.dev/install | bash

VERSION="${STOKE_VERSION:-latest}"
INSTALL_DIR="${STOKE_INSTALL_DIR:-/usr/local/bin}"

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
    linux|darwin) ;;
    *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

echo "⚡ Installing Stoke ($OS/$ARCH)"
echo ""

# For now, build from source (no release binaries yet)
if command -v go &>/dev/null; then
    echo "  Go found: $(go version)"
    
    TMPDIR=$(mktemp -d)
    trap "rm -rf $TMPDIR" EXIT
    
    echo "  Building from source..."
    cd "$TMPDIR"
    
    if [ "$VERSION" = "latest" ]; then
        git clone --depth 1 https://github.com/good-ventures/stoke.git 2>/dev/null || {
            echo "  Cannot clone repo. Install manually:"
            echo "    git clone https://github.com/good-ventures/stoke.git"
            echo "    cd stoke && go build -o stoke ./cmd/stoke"
            echo "    sudo mv stoke $INSTALL_DIR/"
            exit 1
        }
    else
        git clone --depth 1 --branch "$VERSION" https://github.com/good-ventures/stoke.git 2>/dev/null || exit 1
    fi
    
    cd stoke
    go build -o stoke ./cmd/stoke
    
    if [ -w "$INSTALL_DIR" ]; then
        mv stoke "$INSTALL_DIR/stoke"
    else
        echo "  Need sudo to install to $INSTALL_DIR"
        sudo mv stoke "$INSTALL_DIR/stoke"
    fi
    
    echo ""
    echo "  ✓ Installed: $INSTALL_DIR/stoke"
    echo "  Version: $($INSTALL_DIR/stoke version)"
else
    echo "  Go is required to build Stoke from source."
    echo "  Install Go: https://go.dev/dl/"
    echo ""
    echo "  Or install Go first:"
    echo "    curl -fsSL https://go.dev/dl/go1.23.4.linux-${ARCH}.tar.gz | sudo tar -C /usr/local -xz"
    echo "    export PATH=\$PATH:/usr/local/go/bin"
    echo "    Then re-run this script."
    exit 1
fi

echo ""
echo "  Quick start:"
echo "    stoke doctor"
echo "    stoke run --task \"Add auth middleware\" --dry-run"
echo "    stoke build --plan stoke-plan.json --dry-run"
echo ""
echo "  Docs: https://github.com/good-ventures/stoke/blob/main/docs/operator-guide.md"
