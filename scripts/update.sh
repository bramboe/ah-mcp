#!/usr/bin/env bash
# update.sh — Download and install the latest ah-mcp release from GitHub.
#
# Usage:
#   sudo ./update.sh
#
# Requirements:
#   - Store your GitHub PAT in /home/ah-mcp/.github_token (chmod 600)
#     Token needs: repo (classic) OR contents:read (fine-grained)
#   - Run as root (needed to write to /usr/local/bin and /etc/systemd)

set -euo pipefail

REPO="mrserzhan/ah-mcp"
ARCH="linux-amd64"          # change to linux-arm64 if needed
INSTALL_PATH="/usr/local/bin/ah-mcp"
SERVICE_NAME="ah-mcp"
SERVICE_PATH="/etc/systemd/system/ah-mcp.service"
TOKEN_FILE="/home/ah-mcp/.github_token"

# ── Auth ──────────────────────────────────────────────────────────────────────
if [[ ! -f "$TOKEN_FILE" ]]; then
  echo "ERROR: GitHub token not found at $TOKEN_FILE"
  echo "  Create it with: echo 'ghp_...' > $TOKEN_FILE && chmod 600 $TOKEN_FILE"
  exit 1
fi
GITHUB_TOKEN=$(cat "$TOKEN_FILE")
AUTH=(-H "Authorization: token $GITHUB_TOKEN")

# ── Latest release tag ────────────────────────────────────────────────────────
echo "Fetching latest release tag..."
LATEST=$(curl -fsSL "${AUTH[@]}" \
  "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' | head -1 | cut -d'"' -f4)

if [[ -z "$LATEST" ]]; then
  echo "ERROR: Could not fetch latest release from GitHub API"
  exit 1
fi
echo "Latest: $LATEST"

# ── Current version check ─────────────────────────────────────────────────────
if [[ -x "$INSTALL_PATH" ]]; then
  CURRENT=$("$INSTALL_PATH" --version 2>/dev/null | awk '{print $2}' || echo "unknown")
  if [[ "$CURRENT" == "$LATEST" ]]; then
    echo "Already on $LATEST — nothing to do."
    exit 0
  fi
  echo "Current: $CURRENT → updating to $LATEST"
fi

# ── Download binary via GitHub API (works with private repos) ─────────────────
BINARY_ASSET="ah-mcp-$ARCH"
echo "Fetching release asset ID for $BINARY_ASSET..."
ASSET_ID=$(curl -fsSL "${AUTH[@]}" \
  "https://api.github.com/repos/$REPO/releases/tags/$LATEST" \
  | grep -A1 "\"name\": \"$BINARY_ASSET\"" | grep '"id"' | head -1 | grep -o '[0-9]*')

if [[ -z "$ASSET_ID" ]]; then
  echo "ERROR: Could not find asset $BINARY_ASSET in release $LATEST"
  exit 1
fi

echo "Downloading binary (asset $ASSET_ID)..."
curl -fsSL "${AUTH[@]}" -H "Accept: application/octet-stream" \
  -L -o /tmp/ah-mcp-new \
  "https://api.github.com/repos/$REPO/releases/assets/$ASSET_ID"
chmod 755 /tmp/ah-mcp-new

# Quick sanity check
if ! file /tmp/ah-mcp-new | grep -q "ELF"; then
  echo "ERROR: Downloaded file is not an ELF binary — check token permissions"
  rm -f /tmp/ah-mcp-new
  exit 1
fi

# ── Download service file via GitHub API ──────────────────────────────────────
echo "Downloading service file..."
SERVICE_ASSET_ID=$(curl -fsSL "${AUTH[@]}" \
  "https://api.github.com/repos/$REPO/releases/tags/$LATEST" \
  | grep -A1 '"name": "ah-mcp.service"' | grep '"id"' | head -1 | grep -o '[0-9]*')

if [[ -n "$SERVICE_ASSET_ID" ]]; then
  curl -fsSL "${AUTH[@]}" -H "Accept: application/octet-stream" \
    -L -o /tmp/ah-mcp.service \
    "https://api.github.com/repos/$REPO/releases/assets/$SERVICE_ASSET_ID"
else
  # Fall back to raw content API
  curl -fsSL "${AUTH[@]}" \
    "https://api.github.com/repos/$REPO/contents/ah-mcp.service?ref=$LATEST" \
    | grep '"content"' | cut -d'"' -f4 | base64 -d > /tmp/ah-mcp.service
fi

# ── Deploy ────────────────────────────────────────────────────────────────────
echo "Stopping $SERVICE_NAME..."
systemctl stop "$SERVICE_NAME" || true

echo "Installing binary to $INSTALL_PATH..."
cp /tmp/ah-mcp-new "$INSTALL_PATH"
chown root:root "$INSTALL_PATH"
chmod 755 "$INSTALL_PATH"

echo "Installing service file to $SERVICE_PATH..."
cp /tmp/ah-mcp.service "$SERVICE_PATH"
chown root:root "$SERVICE_PATH"
chmod 644 "$SERVICE_PATH"
systemctl daemon-reload

echo "Starting $SERVICE_NAME..."
systemctl start "$SERVICE_NAME"

echo ""
systemctl status "$SERVICE_NAME" --no-pager -l
echo ""
echo "Done — updated to $LATEST"
