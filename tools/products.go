package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterProductTools registers product-related MCP tools.
func RegisterProductTools(s *server.MCPServer, deps Deps) {
	registerSearchProducts(s, deps)
	registerGetBonusOffers(s, deps)
	registerGetLastChanceItems(s, deps)
}

// --- ah_search_products ---

func registerSearchProducts(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_search_products",
		mcp.WithDescription("Search for Albert Heijn products by keyword. Returns id, title, price, bonus_price, unit, is_bonus, image_url."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query, e.g. 'melk' or 'pindakaas'"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default 10)"),
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

		query := req.GetString("query", "")
		if query == "" {
			return errResult("query is required"), nil
		}
		limit := req.GetInt("limit", 10)

		products, err := c.SearchProducts(ctx, query, limit)
		if err != nil {
			return errResult(fmt.Sprintf("Search failed: %v", err)), nil
		}

		type item struct {
			ID         int     `json:"id"`
			Title      string  `json:"title"`
			Price      float64 `json:"price"`
			BonusPrice float64 `json:"bonus_price,omitempty"`
			Unit       string  `json:"unit,omitempty"`
			IsBonus    bool    `json:"is_bonus"`
			ImageURL   string  `json:"image_url,omitempty"`
		}
		results := make([]item, 0, len(products))
		for _, p := range products {
			it := item{
				ID:      p.ID,
				Title:   p.Title,
				Price:   p.Price.Was,
				IsBonus: p.IsBonus,
				Unit:    p.UnitSize,
			}
			if p.IsBonus {
				it.BonusPrice = p.Price.Now
				if it.Price == 0 {
					it.Price = p.Price.Now
				}
			} else {
				it.Price = p.Price.Now
			}
			if len(p.Images) > 0 {
				it.ImageURL = p.Images[0].URL
			}
			results = append(results, it)
		}
		return jsonResult(results)
	})
}

// --- ah_get_bonus_offers ---

func registerGetBonusOffers(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_bonus_offers",
		mcp.WithDescription("Get current Albert Heijn bonus/promotional offers. Returns id, title, original_price, bonus_price, discount_percentage, valid_until."),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default 20)"),
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

		limit := req.GetInt("limit", 20)

		products, err := c.GetBonusProducts(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get bonus products: %v", err)), nil
		}

		type item struct {
			ID                 int     `json:"id"`
			Title              string  `json:"title"`
			OriginalPrice      float64 `json:"original_price,omitempty"`
			BonusPrice         float64 `json:"bonus_price"`
			DiscountPercentage float64 `json:"discount_percentage,omitempty"`
			ValidUntil         string  `json:"valid_until,omitempty"`
			BonusMechanism     string  `json:"bonus_mechanism,omitempty"`
		}
		results := make([]item, 0, len(products))
		for i, p := range products {
			if i >= limit {
				break
			}
			it := item{
				ID:             p.ID,
				Title:          p.Title,
				OriginalPrice:  p.Price.Was,
				BonusPrice:     p.Price.Now,
				BonusMechanism: p.BonusMechanism,
			}
			if p.Price.Was > 0 && p.Price.Now > 0 {
				it.DiscountPercentage = (1 - p.Price.Now/p.Price.Was) * 100
			}
			results = append(results, it)
		}
		return jsonResult(results)
	})
}

// --- ah_get_last_chance_items ---

func registerGetLastChanceItems(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_last_chance_items",
		mcp.WithDescription(
			"Get last-chance / vandaag-af / clearance items from an Albert Heijn store. "+
				"Requires a store_id (use ah_search_products to find stores, or provide postal_code to find the nearest store). "+
				"Uses the dedicated bargainItems GraphQL endpoint which returns today-only markdown deals.",
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default 20)"),
		),
		mcp.WithNumber("store_id",
			mcp.Description("AH store ID. Required to retrieve bargain items."),
		),
		mcp.WithString("postal_code",
			mcp.Description("Dutch postal code (e.g. '1234AB') to find the nearest store when store_id is not known."),
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

		limit := req.GetInt("limit", 20)
		storeID := req.GetInt("store_id", 0)

		// If store_id not provided, try to resolve from postal_code.
		if storeID == 0 {
			postalCode := req.GetString("postal_code", "")
			if postalCode == "" {
				// Try member's address as last resort.
				member, mErr := c.GetMember(ctx)
				if mErr == nil && member.Address.PostalCode != "" {
					postalCode = member.Address.PostalCode
				}
			}
			if postalCode != "" {
				stores, sErr := c.SearchStores(ctx, postalCode)
				if sErr == nil && len(stores) > 0 {
					storeID = stores[0].ID
				}
			}
		}

		if storeID == 0 {
			// NOTE: The bargainItems GraphQL endpoint is store-specific and
			// requires a store ID. Without one we cannot retrieve last-chance
			// items. Ask the user to supply store_id or postal_code.
			return errResult(
				"Cannot retrieve last-chance items without a store. " +
					"Please provide store_id or postal_code. " +
					"Example: {\"store_id\": 1234} or {\"postal_code\": \"1011AB\"}",
			), nil
		}

		bargains, err := c.GetBargains(ctx, storeID)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get bargains for store %d: %v", storeID, err)), nil
		}

		type item struct {
			ID                 int     `json:"id"`
			Title              string  `json:"title"`
			Brand              string  `json:"brand,omitempty"`
			Category           string  `json:"category,omitempty"`
			MarkdownType       string  `json:"markdown_type,omitempty"`
			DiscountPercentage float64 `json:"discount_percentage,omitempty"`
			ExpirationDate     string  `json:"expiration_date,omitempty"`
			Stock              int     `json:"stock,omitempty"`
			PriceWas           string  `json:"price_was,omitempty"`
			PriceNow           string  `json:"price_now"`
		}
		results := make([]item, 0, len(bargains))
		for i, b := range bargains {
			if i >= limit {
				break
			}
			// Parse expiration date for display
			expDate := b.ExpirationDate
			if t, err := time.Parse(time.RFC3339, expDate); err == nil {
				expDate = t.Format("2006-01-02")
			}
			results = append(results, item{
				ID:                 b.Product.ID,
				Title:              b.Product.Title,
				Brand:              b.Product.Brand,
				Category:           b.Category,
				MarkdownType:       b.MarkdownType,
				DiscountPercentage: b.MarkdownPercentage,
				ExpirationDate:     expDate,
				Stock:              b.Stock,
				PriceWas:           b.PriceWas,
				PriceNow:           b.PriceNow,
			})
		}
		return jsonResult(results)
	})
}

// jsonResult marshals v and wraps it in a CallToolResult.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(fmt.Sprintf("marshal result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// refreshTokens calls RefreshIfNeeded from the auth package via the Deps closure.
func refreshTokens(ctx context.Context, deps Deps) error {
	return deps.RefreshIfNeeded(ctx)
}

