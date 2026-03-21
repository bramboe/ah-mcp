package tools

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterInfoTool registers the ah_get_server_info MCP tool.
func RegisterInfoTool(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_server_info",
		mcp.WithTitleAnnotation("Albert Heijn MCP: Server Info"),
		mcp.WithDescription("Returns the version of the MCP server and the appie-go library it uses."),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(fmt.Sprintf(
			"ah-mcp version: %s\nappie-go version: %s",
			deps.ServerVersion, deps.AppieVersion,
		)), nil
	})
}
