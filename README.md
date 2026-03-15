# ah-mcp — Albert Heijn MCP Server

A [Model Context Protocol](https://modelcontextprotocol.io) server for the Albert Heijn (🇳🇱) supermarket API. Works with any MCP-compatible client.

## What this is

**ah-mcp** exposes the Albert Heijn mobile API as MCP tools so your AI assistant can:

- Search products and check bonus offers
- Browse last-chance / vandaag-af clearance items (store-specific)
- View your order history and frequently bought items
- Read and update your shopping list
- Fetch your member profile

Authentication is handled entirely through a reverse-proxy OAuth flow — no tokens are ever stored in the project directory.

## Compatibility

Tested with:

| Client | Transport |
|---|---|
| Claude Desktop | stdio |
| Claude.ai (web) | SSE |
| Windsurf | stdio |
| Cursor | stdio |
| ChatGPT Desktop | SSE |

## Quick start (pre-built binary)

1. Download the latest binary from Releases.
2. Run it:
   ```bash
   ./ah-mcp --transport stdio   # for local clients
   ./ah-mcp --transport sse     # for web / remote clients (default)
   ```
3. Ask your AI assistant to call **`ah_login`** and follow the URL it returns.

## Build from source

```bash
git clone https://github.com/mrserzhan/ah-mcp
cd ah-mcp
go build -o ah-mcp .
```

Requires Go 1.23+.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `AH_CALLBACK_HOST` | `http://localhost:9876` | Base URL for the OAuth proxy. Users open this URL in their browser during login. Override to your server's public URL for remote deployments. |
| `AH_CALLBACK_PORT` | `9876` | Port the temporary OAuth reverse-proxy server listens on. |
| `AH_MCP_PORT` | `3000` | Port for the MCP HTTP/SSE server (`--transport sse`). |
| `AH_TOKENS_PATH` | `~/.config/ah-mcp/tokens.json` | Override the XDG token storage path. Directory is created automatically (mode `0700`). File is written with mode `0600`. |

Copy `.env.example` to `.env` and uncomment lines you want to change.

## Token storage

Tokens are stored in the XDG-compliant config directory:

| Platform | Default path |
|---|---|
| Linux | `~/.config/ah-mcp/tokens.json` |
| macOS | `~/Library/Application Support/ah-mcp/tokens.json` |
| Windows | `%AppData%\ah-mcp\tokens.json` |

The directory is created with mode `0700` and the file with mode `0600` (owner read/write only). Tokens are refreshed automatically before every API call — you only need to run `ah_login` once.

Override with `AH_TOKENS_PATH` if needed.

## First login

Just call the **`ah_login`** tool from your AI assistant:

```
User: log in to ah
Assistant: calls ah_login
→ "Please open this URL in your browser to log in to Albert Heijn:
   http://localhost:9876/login?...
   Waiting for you to complete login (timeout: 5 minutes)..."
→ (after you log in)
→ "Login successful! Connected as Jan Jansen."
```

No configuration needed for local use.

## Client setup guides

### Claude.ai (web) — SSE

1. Start the server: `./ah-mcp --transport sse` (exposes port 3000).
2. Open Claude.ai → Settings → Connections → Add MCP server.
3. Paste the SSE URL: `http://your-server:3000/sse`

### Claude Desktop — SSE or stdio

**SSE** (remote server):

```json
{
  "mcpServers": {
    "ah": {
      "url": "http://your-server:3000/sse"
    }
  }
}
```

**stdio** (local binary):

```json
{
  "mcpServers": {
    "ah": {
      "command": "/path/to/ah-mcp",
      "args": ["--transport", "stdio"]
    }
  }
}
```

### Windsurf / Cursor — stdio

Add to your MCP config (usually `~/.codeium/windsurf/mcp_config.json` or `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "ah": {
      "command": "/path/to/ah-mcp",
      "args": ["--transport", "stdio"]
    }
  }
}
```

## Remote server deployment

### systemd setup

1. Create a dedicated user:
   ```bash
   sudo useradd -r -m -d /home/ah-mcp -s /sbin/nologin ah-mcp
   ```
2. Copy the binary:
   ```bash
   sudo cp ah-mcp /usr/local/bin/ah-mcp
   sudo chmod 755 /usr/local/bin/ah-mcp
   ```
3. Create `/home/ah-mcp/.env`:
   ```env
   AH_CALLBACK_HOST=https://ah-mcp.example.com
   AH_MCP_PORT=3000
   ```
4. Install and start the service:
   ```bash
   sudo cp ah-mcp.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now ah-mcp
   ```
5. Put a reverse proxy (nginx, Caddy) in front to handle TLS.

Tokens are stored automatically at `/home/ah-mcp/.config/ah-mcp/tokens.json` — no `AH_TOKENS_PATH` override needed.

## Available tools

| Tool | Description |
|---|---|
| `ah_login` | Authenticate with Albert Heijn via browser OAuth |
| `ah_search_products` | Search products by keyword |
| `ah_get_bonus_offers` | List current bonus/promotional offers |
| `ah_get_last_chance_items` | Vandaag-af / clearance items from a store |
| `ah_get_order_history` | Recent online orders |
| `ah_get_frequent_items` | Products you order most often |
| `ah_get_shopping_list` | View your shopping list |
| `ah_add_to_shopping_list` | Add items to your shopping list |
| `ah_get_member_profile` | Your AH member profile |

## Troubleshooting

**Port conflict on 9876 or 3000**
Change via `AH_CALLBACK_PORT` or `AH_MCP_PORT` in your `.env`.

**Login timeout after 5 minutes**
The OAuth flow timed out. Call `ah_login` again and complete the browser flow faster.

**Token issues / expired session**
Delete `tokens.json` and call `ah_login` again.
On Linux: `rm ~/.config/ah-mcp/tokens.json`
On macOS: `rm ~/Library/Application\ Support/ah-mcp/tokens.json`

**Last-chance items require a store**
`ah_get_last_chance_items` needs a store ID or postal code — bargain items are store-specific. Provide `store_id` or `postal_code` as a parameter.

**"Not logged in" error**
Run `ah_login` first. The server does not auto-open a browser; you must open the URL manually.

## Acknowledgements

**ah-mcp** is built on top of [**appie-go**](https://github.com/gwillem/appie-go) — a Go client library for the Albert Heijn mobile API by [@gwillem](https://github.com/gwillem). It provides the authenticated HTTP client, all API call implementations (product search, bonus offers, orders, shopping lists, member profile, bargain items), and the OAuth token format. This project uses it as a library dependency without modification.

The OAuth reverse-proxy login flow in `auth.go` is inspired by the approach in appie-go's `login.go`, adapted to return a URL string rather than open a browser — making it safe for server-side MCP use.
