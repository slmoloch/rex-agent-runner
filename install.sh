#!/usr/bin/env bash
# Install rex: build the Go binary and symlink it into ~/.local/bin (or $1).
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$PROJECT_DIR/bin"
INSTALL_DIR="${1:-$HOME/.local/bin}"

if ! command -v go >/dev/null 2>&1; then
    echo "error: 'go' not found on PATH. Install Go 1.22+ from https://go.dev/dl/" >&2
    exit 1
fi

echo "==> Building rex..."
mkdir -p "$BIN_DIR"
(cd "$PROJECT_DIR/go" && go build -o "$BIN_DIR/rex" ./cmd/rex)

echo "==> Symlinking rex to $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
target="$INSTALL_DIR/rex"
if [ -L "$target" ] || [ -e "$target" ]; then
    echo "    Updating existing rex..."
    rm -f "$target"
fi
ln -s "$BIN_DIR/rex" "$target"

# Clean up old command names from the Python era.
for old_cmd in claude-bot claude-job agent-tool rex-go; do
    old_target="$INSTALL_DIR/$old_cmd"
    if [ -L "$old_target" ] || [ -e "$old_target" ]; then
        rm -f "$old_target"
        echo "    Removed old $old_cmd"
    fi
done

echo ""
echo "Done! rex installed to $target"
echo ""
echo "Next steps:"
echo "  cd <your-project-folder>"
echo "  rex init"
