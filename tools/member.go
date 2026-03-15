package tools

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterMemberTools registers member profile MCP tools.
func RegisterMemberTools(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_member_profile",
		mcp.WithDescription(
			"Get your Albert Heijn member profile. "+
				"Returns name, email, member_since, and bonus_card_number (last 4 digits only).",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		member, err := c.GetMember(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get member profile: %v", err)), nil
		}

		// Mask bonus card: show only last 4 digits.
		bonusCard := member.BonusCardNumber
		if len(bonusCard) > 4 {
			bonusCard = "****" + bonusCard[len(bonusCard)-4:]
		}

		type profile struct {
			Name            string `json:"name"`
			Email           string `json:"email"`
			BonusCardNumber string `json:"bonus_card_number,omitempty"`
			DateOfBirth     string `json:"date_of_birth,omitempty"`
		}
		return jsonResult(profile{
			Name:            member.FirstName + " " + member.LastName,
			Email:           member.Email,
			BonusCardNumber: bonusCard,
			DateOfBirth:     member.DateOfBirth,
		})
	})
}
