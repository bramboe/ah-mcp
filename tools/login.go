package tools

import (
	"context"
	"fmt"
	"os"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Deps holds the dependencies injected into every tool handler.
type Deps struct {
	// TokensPath is the path to the tokens.json file.
	TokensPath string
	// CallbackHost is the base URL for the OAuth callback server (e.g. http://localhost:9876).
	CallbackHost string
	// CallbackPort is the port the temporary OAuth proxy listens on.
	CallbackPort int
	// GetClient returns the authenticated appie client.
	GetClient func() (*appie.Client, error)
	// ReloadClient recreates the client from the tokens file.
	ReloadClient func() (*appie.Client, error)
	// IsAuthenticated checks whether valid tokens are on disk.
	IsAuthenticated func() bool
	// StartOAuthFlow starts the proxy and returns (loginURL, doneChan, err).
	StartOAuthFlow func(ctx context.Context) (string, <-chan error, error)
	// RefreshIfNeeded refreshes the access token if it is close to expiry.
	RefreshIfNeeded func(ctx context.Context) error
	// Server is needed to send log notifications while the tool blocks.
	Server *server.MCPServer
}

func notAuthResult() *mcp.CallToolResult {
	return mcp.NewToolResultError(`{"error":"not_authenticated","message":"Not logged in. Call ah_login first."}`)
}

func errResult(msg string) *mcp.CallToolResult {
	return mcp.NewToolResultError(msg)
}

// RegisterLoginTool registers the ah_login MCP tool.
func RegisterLoginTool(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_login",
		mcp.WithDescription("Log in to Albert Heijn. Returns a URL to open in your browser, then waits for authentication to complete (up to 5 minutes). Already authenticated? Returns your name immediately."),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleLogin(ctx, req, deps)
	})
}

func handleLogin(ctx context.Context, _ mcp.CallToolRequest, deps Deps) (*mcp.CallToolResult, error) {
	// If already authenticated, return immediately.
	if deps.IsAuthenticated() {
		c, err := deps.GetClient()
		if err != nil {
			return mcp.NewToolResultText("Already logged in (could not fetch member name)."), nil
		}
		member, err := c.GetMember(ctx)
		if err != nil {
			return mcp.NewToolResultText("Already logged in (could not fetch member name)."), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Already connected as %s %s.", member.FirstName, member.LastName)), nil
	}

	// Start the OAuth flow.
	loginURL, done, err := deps.StartOAuthFlow(ctx)
	if err != nil {
		return errResult(fmt.Sprintf("Failed to start OAuth flow: %v", err)), nil
	}

	// Send the login URL as a log notification so clients that support streaming
	// can show it to the user immediately, before the tool finishes blocking.
	_ = deps.Server.SendLogMessageToClient(ctx, mcp.LoggingMessageNotification{
		Params: mcp.LoggingMessageNotificationParams{
			Level:  mcp.LoggingLevelInfo,
			Logger: "ah-mcp",
			Data:   fmt.Sprintf("Please open this URL in your browser: %s", loginURL),
		},
	})

	fmt.Fprintf(os.Stderr, "[ah-mcp] OAuth login URL: %s\n", loginURL)

	// Block waiting for the OAuth callback (max 5 minutes).
	select {
	case loginErr := <-done:
		if loginErr != nil {
			return errResult(fmt.Sprintf("Login failed: %v", loginErr)), nil
		}
	case <-ctx.Done():
		return errResult("Login cancelled."), nil
	}

	// Reload the client so it picks up the new tokens.
	c, err := deps.ReloadClient()
	if err != nil {
		return errResult(fmt.Sprintf("Login succeeded but could not reload client: %v", err)), nil
	}

	member, err := c.GetMember(ctx)
	if err != nil {
		return mcp.NewToolResultText("Login successful! (Could not fetch member name.)"), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Please open this URL in your browser to log in to Albert Heijn:\n%s\n\nWaiting for you to complete login (timeout: 5 minutes)...\n\nLogin successful! Connected as %s %s.",
		loginURL, member.FirstName, member.LastName,
	)), nil
}
