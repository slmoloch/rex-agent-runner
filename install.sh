#!/usr/bin/env bash
# Install rex: set up venv, deps, and symlink to ~/.local/bin
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$PROJECT_DIR/bin"
INSTALL_DIR="${1:-$HOME/.local/bin}"

echo "==> Setting up virtual environment..."
if [ ! -d "$PROJECT_DIR/venv" ]; then
    python3 -m venv "$PROJECT_DIR/venv"
fi

echo "==> Installing dependencies..."
"$PROJECT_DIR/venv/bin/pip" install -q -r "$PROJECT_DIR/requirements.txt"

# Build the Go port alongside the Python install. The shell wrapper still
# dispatches to Python for every subcommand; the Go binary only exposes what
# has been ported so far (see PORT_CLEANUP.md for the transition plan).
if command -v go >/dev/null 2>&1; then
    echo "==> Building Go binary..."
    (cd "$PROJECT_DIR/go" && go build -o "$BIN_DIR/rex-go" ./cmd/rex)
else
    echo "==> Skipping Go build (go not found on PATH)."
fi

echo "==> Making rex executable..."
chmod +x "$BIN_DIR/rex"

echo "==> Symlinking rex to $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR"
target="$INSTALL_DIR/rex"
if [ -L "$target" ] || [ -e "$target" ]; then
    echo "    Updating existing rex..."
    rm -f "$target"
fi
ln -s "$BIN_DIR/rex" "$target"

# Clean up old command names
for old_cmd in claude-bot claude-job agent-tool; do
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
