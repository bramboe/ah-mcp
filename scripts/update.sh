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

# ── Fetch release metadata once ───────────────────────────────────────────────
echo "Fetching release metadata..."
RELEASE_JSON=$(curl -fsSL "${AUTH[@]}" \
  "https://api.github.com/repos/$REPO/releases/tags/$LATEST")

# Helper: extract asset ID by name using Python (always available)
asset_id() {
  echo "$RELEASE_JSON" | python3 -c "
import sys, json
assets = json.load(sys.stdin).get('assets', [])
name = '$1'
for a in assets:
    if a['name'] == name:
        print(a['id'])
        break
"
}

# ── Download binary ────────────────────────────────────────────────────────────
BINARY_ASSET="ah-mcp-$ARCH"
echo "Fetching release asset ID for $BINARY_ASSET..."
ASSET_ID=$(asset_id "$BINARY_ASSET")

if [[ -z "$ASSET_ID" ]]; then
  echo "ERROR: Asset $BINARY_ASSET not found in release $LATEST"
  echo "Available assets:"
  echo "$RELEASE_JSON" | python3 -c "import sys,json; [print(' -', a['name']) for a in json.load(sys.stdin).get('assets',[])]"
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

# ── Download service file ──────────────────────────────────────────────────────
echo "Downloading service file..."
SERVICE_ASSET_ID=$(asset_id "ah-mcp.service")

if [[ -n "$SERVICE_ASSET_ID" ]]; then
  curl -fsSL "${AUTH[@]}" -H "Accept: application/octet-stream" \
    -L -o /tmp/ah-mcp.service \
    "https://api.github.com/repos/$REPO/releases/assets/$SERVICE_ASSET_ID"
else
  # Fall back to raw content API (base64 decode)
  curl -fsSL "${AUTH[@]}" \
    "https://api.github.com/repos/$REPO/contents/ah-mcp.service?ref=$LATEST" \
    | python3 -c "import sys,json,base64; print(base64.b64decode(json.load(sys.stdin)['content']).decode(),end='')" \
    > /tmp/ah-mcp.service
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
