package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/mrserzhan/ah-mcp/tools"
	"github.com/mark3labs/mcp-go/server"
)

// version is set at build time via -ldflags="-X main.version=v1.2.3"
var version = "dev"

const (
	defaultCallbackHost = "http://localhost:9876"
	defaultCallbackPort = 9876
	defaultMCPPort      = 3000
)

func main() {
	transport := flag.String("transport", "sse", "Transport mode: 'sse' (HTTP/SSE) or 'stdio'")
	remote := flag.Bool("remote", false, "Remote mode: disable auto browser-open on login (overridden by AH_REMOTE=true)")
	flag.Parse()

	// AH_REMOTE env var also enables remote mode.
	if os.Getenv("AH_REMOTE") == "true" {
		*remote = true
	}

	// Resolve config from environment.
	callbackHost := envOr("AH_CALLBACK_HOST", defaultCallbackHost)
	callbackPort := envIntOr("AH_CALLBACK_PORT", defaultCallbackPort)
	mcpPort := envIntOr("AH_MCP_PORT", defaultMCPPort)
	tokensPath := TokensPath()

	// Ensure token directory exists with secure permissions.
	if err := ensureTokenDir(tokensPath); err != nil {
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Warning: could not create token directory: %v\n", err)
	}

	// Build MCP server.
	s := server.NewMCPServer(
		"Albert Heijn",
		version,
		server.WithLogging(),
	)

	// Build dependency bundle.
	deps := tools.Deps{
		TokensPath:   tokensPath,
		CallbackHost: callbackHost,
		CallbackPort: callbackPort,
		RemoteMode:   *remote,
		GetClient:    GetClient,
		ReloadClient: ReloadClient,
		IsAuthenticated: func() bool {
			return IsAuthenticated(tokensPath)
		},
		StartOAuthFlow: func(ctx context.Context) (string, <-chan error, error) {
			return StartOAuthFlow(ctx, callbackHost, callbackPort, tokensPath)
		},
		RefreshIfNeeded: func(ctx context.Context) error {
			return RefreshIfNeeded(ctx, tokensPath)
		},
		Server: s,
	}

	// Register all tools.
	tools.RegisterLoginTool(s, deps)
	tools.RegisterProductTools(s, deps)
	tools.RegisterOrderTools(s, deps)
	tools.RegisterBasketTools(s, deps)
	tools.RegisterMemberTools(s, deps)

	ctx := context.Background()

	switch *transport {
	case "stdio":
		fmt.Fprintln(os.Stderr, "[Albert Heijn MCP] Starting in stdio mode")
		stdioSrv := server.NewStdioServer(s)
		if err := stdioSrv.Listen(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] stdio server error: %v\n", err)
			os.Exit(1)
		}
	case "sse", "":
		addr := fmt.Sprintf(":%d", mcpPort)
		// Base URL advertised to clients. Defaults to localhost for local use;
		// override AH_MCP_BASE_URL for remote deployments behind a reverse proxy.
		baseURL := envOr("AH_MCP_BASE_URL", fmt.Sprintf("http://localhost:%d", mcpPort))
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Starting SSE server on %s (base URL: %s)\n", addr, baseURL)
		httpSrv := server.NewSSEServer(s, server.WithBaseURL(baseURL))
		if err := httpSrv.Start(addr); err != nil {
			fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] SSE server error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Unknown transport %q. Use 'sse' or 'stdio'.\n", *transport)
		os.Exit(1)
	}
}

// envOr returns the value of the named environment variable or the default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envIntOr returns the integer value of the named environment variable or the default.
func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ensureTokenDir creates the directory for the tokens file with mode 0700.
func ensureTokenDir(tokensPath string) error {
	return os.MkdirAll(parentDir(tokensPath), 0700)
}
