#!/bin/sh
set -e

REPO="voocel/codebot"
BINARY="codebot"
INSTALL_DIR="/usr/local/bin"

# Detect OS and architecture.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$OS" in
    linux)  OS="Linux" ;;
    darwin) OS="Darwin" ;;
    *)      echo "Unsupported OS: $OS"; exit 1 ;;
esac

case "$ARCH" in
    x86_64|amd64) ARCH="x86_64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)             echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Determine version: use argument, env var, or fetch latest.
VERSION="${1:-${CODEBOT_VERSION:-}}"
if [ -z "$VERSION" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
    if [ -z "$VERSION" ]; then
        echo "Failed to fetch latest version"; exit 1
    fi
fi

FILENAME="${BINARY}_${VERSION#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

echo "Downloading ${BINARY} ${VERSION} (${OS}/${ARCH})..."
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

curl -fsSL "$URL" -o "${TMPDIR}/${FILENAME}"
tar -xzf "${TMPDIR}/${FILENAME}" -C "$TMPDIR"

# Install binary.
if [ -w "$INSTALL_DIR" ]; then
    mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
    echo "Need sudo to install to ${INSTALL_DIR}"
    sudo mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

# Create global config directory and default AGENTS.md if not present.
CONFIG_DIR="${HOME}/.codebot"
AGENTS_FILE="${CONFIG_DIR}/AGENTS.md"
mkdir -p "$CONFIG_DIR"
if [ ! -f "$AGENTS_FILE" ]; then
    cat > "$AGENTS_FILE" << 'AGENTS_EOF'
# Codebot

You are Codebot, an AI coding assistant that runs in the terminal.
You help developers read, write, and refactor code through direct filesystem and shell access.

# This file is loaded for every project as the lowest-priority context.
# Add your personal preferences and conventions here.
# Project-level AGENTS.md (in the project root) takes higher priority.

## Code Style
- Prefer simple, correct solutions over clever ones
- Follow existing conventions in each project

## Communication
- Be concise and direct
- Explain the "why" before making changes
AGENTS_EOF
    echo "Created default ${AGENTS_FILE}"
fi

echo "${BINARY} ${VERSION} installed to ${INSTALL_DIR}/${BINARY}"
