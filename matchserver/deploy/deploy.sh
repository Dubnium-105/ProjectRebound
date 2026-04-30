#!/usr/bin/env bash
set -euo pipefail

# ProjectRebound MatchServer deploy script
# Run on the VPS as root (via sudo). Expects:
#   /tmp/matchserver-new   — the new Go binary
#   Working directory layout matches matchserver.service:
#     Binary:   /opt/projectrebound/matchserver/matchserver
#     Config:   /etc/projectrebound/matchserver.yaml
#     Database: /var/lib/projectrebound/matchserver.db

BINARY_SRC="/tmp/matchserver-new"
BINARY_DST="/opt/projectrebound/matchserver"
RELEASES_DIR="/opt/projectrebound/releases"
PREVIOUS_DIR="/opt/projectrebound/previous"
HEALTH_URL="http://localhost:5000/health"
SERVICE_NAME="matchserver"

if [[ ! -f "$BINARY_SRC" ]]; then
    echo "ERROR: $BINARY_SRC not found"
    exit 1
fi

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
RELEASE_DIR="$RELEASES_DIR/$TIMESTAMP"

echo "=== Deploying release $TIMESTAMP ==="

# 1. Create release archive
mkdir -p "$RELEASES_DIR" "$(dirname "$BINARY_DST")"
mkdir -p "$RELEASE_DIR"
cp "$BINARY_SRC" "$RELEASE_DIR/matchserver"
echo "$TIMESTAMP" > "$RELEASE_DIR/VERSION"

# 2. Back up current running version
if [[ -f "$BINARY_DST/matchserver" ]]; then
    echo "Backing up current binary..."
    mkdir -p "$PREVIOUS_DIR"
    rm -rf "$PREVIOUS_DIR"/*
    cp -r "$BINARY_DST"/* "$PREVIOUS_DIR/" 2>/dev/null || true
fi

# 3. Replace working binary
echo "Replacing binary..."
rm -rf "$BINARY_DST"/*
cp "$RELEASE_DIR/matchserver" "$BINARY_DST/"
echo "$TIMESTAMP" > "$BINARY_DST/VERSION"

# 4. Restart service
echo "Restarting $SERVICE_NAME..."
systemctl restart "$SERVICE_NAME"

# 5. Health check with rollback
echo "Waiting for service to start..."
sleep 2

if curl -sf --max-time 5 "$HEALTH_URL" > /dev/null; then
    echo "=== Health check passed ==="
else
    echo "=== Health check FAILED — rolling back ==="
    if [[ -f "$PREVIOUS_DIR/matchserver" ]]; then
        rm -rf "$BINARY_DST"/*
        cp -r "$PREVIOUS_DIR"/* "$BINARY_DST/"
        systemctl restart "$SERVICE_NAME"
        sleep 2
        if curl -sf --max-time 5 "$HEALTH_URL" > /dev/null; then
            echo "Rollback successful."
        else
            echo "CRITICAL: Rollback also failed. Manual intervention required."
            exit 1
        fi
    else
        echo "CRITICAL: No previous binary to roll back to."
        exit 1
    fi
fi

# 6. Cleanup old releases (keep last 5)
echo "Cleaning old releases..."
ls -1dt "$RELEASES_DIR"/*/ 2>/dev/null | tail -n +6 | xargs rm -rf 2>/dev/null || true

echo "=== Deploy $TIMESTAMP complete ==="
