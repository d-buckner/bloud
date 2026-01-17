#!/usr/bin/env bash
# Start the bloud development environment inside the Lima VM
#
# Uses tmux to manage two processes:
# 1. Go hot reload via rsync + watch loop
# 2. vite - Svelte dev server with HMR
#
# Source files are synced from 9p mount to local /tmp for writing.
#

set -e

PROJECT_DIR="/home/bloud.linux/bloud"
LOCAL_SRC="/tmp/bloud-src"
DATA_DIR="/home/bloud/.local/share/bloud"  # Match NixOS module path
LOCAL_NODE_MODULES="/tmp/bloud-node-modules"
TMUX_SESSION="bloud-dev"

echo "=== Bloud Development Environment ==="
echo ""

# Check if project is mounted
if [ ! -d "$PROJECT_DIR/nixos" ]; then
    echo "Error: Project directory not mounted at $PROJECT_DIR"
    echo "Run: ./bloud vm-start"
    exit 1
fi

# Create data directories (in user's home, no sudo needed)
mkdir -p "$DATA_DIR/nix"
mkdir -p "$DATA_DIR/traefik/dynamic"

# Ensure podman network exists
echo "Ensuring podman network..."
podman network create apps-net 2>/dev/null || true

# Initial sync of source (full copy)
echo "Syncing source files..."
rm -rf "$LOCAL_SRC"
mkdir -p "$LOCAL_SRC"
# Use cp instead of rsync (rsync may not be installed)
cp -r "$PROJECT_DIR/services/host-agent" "$LOCAL_SRC/"
cp -r "$PROJECT_DIR/apps" "$LOCAL_SRC/"

# Set up node_modules for Linux
WEB_DIR="$LOCAL_SRC/host-agent/web"
if [ ! -d "$LOCAL_NODE_MODULES/node_modules" ] || [ "$PROJECT_DIR/services/host-agent/web/package.json" -nt "$LOCAL_NODE_MODULES/.installed" ]; then
    echo "Installing npm dependencies for Linux..."
    mkdir -p "$LOCAL_NODE_MODULES"
    cd "$LOCAL_NODE_MODULES"
    cp "$WEB_DIR/package.json" .
    cp "$WEB_DIR/package-lock.json" . 2>/dev/null || true
    npm install --prefer-offline 2>&1 | tail -5
    touch "$LOCAL_NODE_MODULES/.installed"
fi

# Symlink node_modules into local src (remove existing first)
rm -rf "$WEB_DIR/node_modules"
ln -s "$LOCAL_NODE_MODULES/node_modules" "$WEB_DIR/node_modules"

# Fix go.mod replace paths for VM directory structure
sed -i 's|=> ../../apps|=> ../apps|g' "$LOCAL_SRC/host-agent/go.mod" 2>/dev/null || true
sed -i 's|=> ../services/host-agent|=> ../host-agent|g' "$LOCAL_SRC/apps/go.mod" 2>/dev/null || true

# Pre-build host-agent binary (required for service prestart/poststart hooks)
echo "Building host-agent binary..."
cd "$LOCAL_SRC/host-agent"
if go build -o /tmp/host-agent ./cmd/host-agent 2>&1; then
    echo "Host-agent binary built at /tmp/host-agent"
else
    echo "Warning: Failed to build host-agent, services may fail"
fi

# Restart any failed services (in case services failed before binary existed)
echo "Checking for failed services..."
systemctl --user reset-failed 2>/dev/null || true
for svc in podman-apps-postgres podman-apps-redis bloud-db-init authentik-db-init podman-apps-authentik-server podman-apps-authentik-worker podman-apps-authentik-proxy; do
    if systemctl --user is-failed "$svc.service" 2>/dev/null; then
        echo "Restarting failed service: $svc"
        systemctl --user restart "$svc.service" 2>/dev/null || true
    elif ! systemctl --user is-active "$svc.service" 2>/dev/null; then
        echo "Starting inactive service: $svc"
        systemctl --user start "$svc.service" 2>/dev/null || true
    fi
done

# Create Go watch script with rsync
GO_WATCH="/tmp/go-watch.sh"
cat > "$GO_WATCH" << 'GO_EOF'
#!/usr/bin/env bash
# Go hot reload with file sync
SRC_DIR="$1"
LOCAL_DIR="$2"
DATA_DIR="$3"
NIX_CONFIG_DIR="$4"

cd "$LOCAL_DIR/host-agent"
export BLOUD_PORT=3000
export BLOUD_DATA_DIR="$DATA_DIR"
export BLOUD_APPS_DIR="$LOCAL_DIR/apps"
export BLOUD_NIX_CONFIG_DIR="$NIX_CONFIG_DIR"
export BLOUD_FLAKE_PATH="$SRC_DIR"
export BLOUD_FLAKE_TARGET="vm-dev"
export BLOUD_NIXOS_PATH="$SRC_DIR/nixos"

BIN="/tmp/host-agent"
LAST_HASH=""
PID=""

cleanup() {
    [ -n "$PID" ] && kill "$PID" 2>/dev/null
    exit 0
}
trap cleanup SIGINT SIGTERM

sync_files() {
    # Sync Go files from 9p mount to local copy
    cp -r "$SRC_DIR/services/host-agent/cmd" "$LOCAL_DIR/host-agent/" 2>/dev/null
    cp -r "$SRC_DIR/services/host-agent/internal" "$LOCAL_DIR/host-agent/" 2>/dev/null
    cp -r "$SRC_DIR/services/host-agent/pkg" "$LOCAL_DIR/host-agent/" 2>/dev/null
    cp "$SRC_DIR/services/host-agent/go.mod" "$LOCAL_DIR/host-agent/" 2>/dev/null
    cp "$SRC_DIR/services/host-agent/go.sum" "$LOCAL_DIR/host-agent/" 2>/dev/null
    # Fix go.mod replace path for VM directory structure (../../apps -> ../apps)
    sed -i 's|=> ../../apps|=> ../apps|g' "$LOCAL_DIR/host-agent/go.mod" 2>/dev/null
    # Sync apps (Go configurators + metadata)
    cp -r "$SRC_DIR/apps/"* "$LOCAL_DIR/apps/" 2>/dev/null
    # Fix apps/go.mod replace path for VM directory structure
    sed -i 's|=> ../services/host-agent|=> ../host-agent|g' "$LOCAL_DIR/apps/go.mod" 2>/dev/null
}

build_and_run() {
    echo "[$(date +%H:%M:%S)] Syncing files..."
    sync_files
    echo "[$(date +%H:%M:%S)] Building..."
    if go build -o "$BIN" ./cmd/host-agent 2>&1; then
        echo "[$(date +%H:%M:%S)] Build successful, starting server..."
        [ -n "$PID" ] && kill "$PID" 2>/dev/null && sleep 1
        "$BIN" &
        PID=$!
        echo "[$(date +%H:%M:%S)] Server started (PID: $PID)"
    else
        echo "[$(date +%H:%M:%S)] Build failed!"
    fi
}

# Initial build
build_and_run

# Watch loop - check source directory for changes
echo "[$(date +%H:%M:%S)] Watching for changes (polling every 2s)..."
while true; do
    sleep 2
    # Get hash of all Go files from SOURCE dir
    HASH=$(find "$SRC_DIR/services/host-agent" -name '*.go' -not -path '*/web/*' -exec stat -c '%Y' {} \; 2>/dev/null | sort | md5sum)
    if [ "$HASH" != "$LAST_HASH" ]; then
        LAST_HASH="$HASH"
        echo ""
        echo "[$(date +%H:%M:%S)] Change detected!"
        build_and_run
    fi
done
GO_EOF
chmod +x "$GO_WATCH"

# Create vite wrapper
VITE_WRAPPER="/tmp/run-vite.sh"
cat > "$VITE_WRAPPER" << 'VITE_EOF'
#!/usr/bin/env bash
# Watch for web file changes and restart vite if needed
SRC_DIR="$1"
LOCAL_DIR="$2"
WEB_DIR="$LOCAL_DIR/host-agent/web"
NODE_MODULES="/tmp/bloud-node-modules/node_modules"

cd "$WEB_DIR"

sync_web_files() {
    cp -r "$SRC_DIR/services/host-agent/web/src" "$WEB_DIR/" 2>/dev/null
    cp -r "$SRC_DIR/services/host-agent/web/static" "$WEB_DIR/" 2>/dev/null
    cp "$SRC_DIR/services/host-agent/web/vite.config.ts" "$WEB_DIR/" 2>/dev/null
    cp "$SRC_DIR/services/host-agent/web/svelte.config.js" "$WEB_DIR/" 2>/dev/null
    cp "$SRC_DIR/services/host-agent/web/tsconfig.json" "$WEB_DIR/" 2>/dev/null
}

start_vite() {
    node "$NODE_MODULES/vite/bin/vite.js" dev --port 5173 &
    VITE_PID=$!
    echo "[$(date +%H:%M:%S)] Vite started (PID: $VITE_PID)"
}

LAST_HASH=""

# Initial sync
sync_web_files

# Start vite in background
start_vite

echo "[$(date +%H:%M:%S)] Watching for web file changes..."

cleanup() {
    kill "$VITE_PID" 2>/dev/null
    exit 0
}
trap cleanup SIGINT SIGTERM

# Watch for changes and sync (src + static directories)
while true; do
    sleep 2
    HASH=$(find "$SRC_DIR/services/host-agent/web/src" "$SRC_DIR/services/host-agent/web/static" -type f -exec stat -c '%Y' {} \; 2>/dev/null | sort | md5sum)
    if [ "$HASH" != "$LAST_HASH" ]; then
        LAST_HASH="$HASH"
        echo "[$(date +%H:%M:%S)] Web files changed, syncing..."
        sync_web_files
    fi
    # Check if vite is still running
    if ! kill -0 "$VITE_PID" 2>/dev/null; then
        echo "[$(date +%H:%M:%S)] Vite stopped, restarting..."
        start_vite
    fi
done
VITE_EOF
chmod +x "$VITE_WRAPPER"

# Check if tmux session already exists
if tmux has-session -t "$TMUX_SESSION" 2>/dev/null; then
    echo "Dev environment already running!"
    echo ""
    echo "To view: ./bloud attach"
    echo "To stop: ./bloud stop"
    exit 0
fi

echo "Starting dev servers..."
echo ""

# Create tmux session with two panes
tmux new-session -d -s "$TMUX_SESSION" -n dev

# First pane: Go hot reload
tmux send-keys -t "$TMUX_SESSION:dev" "$GO_WATCH '$PROJECT_DIR' '$LOCAL_SRC' '$DATA_DIR' '$DATA_DIR/nix'" Enter

# Split horizontally for second pane
tmux split-window -h -t "$TMUX_SESSION:dev"

# Second pane: Vite dev server with sync
tmux send-keys -t "$TMUX_SESSION:dev.1" "$VITE_WRAPPER '$PROJECT_DIR' '$LOCAL_SRC'" Enter

# Set equal pane sizes
tmux select-layout -t "$TMUX_SESSION:dev" even-horizontal

echo "=== Development Environment Started ==="
echo ""
echo "Services (with hot reload):"
echo "  Go API:  http://localhost:3000  (syncing + rebuilding on *.go changes)"
echo "  Web UI:  http://localhost:5173  (syncing + vite HMR)"
echo ""
echo "Commands:"
echo "  ./bloud attach   - View tmux session (Ctrl-B D to detach)"
echo "  ./bloud logs     - View server output"
echo "  ./bloud stop     - Stop dev servers"
echo "  ./bloud status   - Check service status"
echo ""
echo "Edit files on your Mac - changes sync and reload in the VM!"
echo ""
