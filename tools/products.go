package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterProductTools registers product-related MCP tools.
func RegisterProductTools(s *server.MCPServer, deps Deps) {
	registerSearchProducts(s, deps)
	registerSearchProductsFiltered(s, deps)
	registerGetProduct(s, deps)
	registerGetBonusOffers(s, deps)
	registerGetBonusGroupProducts(s, deps)
	registerGetLastChanceItems(s, deps)
	registerSearchStores(s, deps)
}

// --- ah_search_products ---

func registerSearchProducts(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_search_products",
		mcp.WithTitleAnnotation("Albert Heijn: Search Products"),
		mcp.WithDescription(
			"Search for Albert Heijn (Dutch supermarket) products by keyword. "+
				"AH is a Dutch supermarket so prefer Dutch search terms for best results: "+
				"e.g. 'melk' (milk), 'kaas' (cheese), 'brood' (bread), 'kip' (chicken), 'appel' (apple). "+
				"English terms also work but may return fewer results. "+
				"Returns id, title, price, bonus_price, unit, is_bonus, image_url.",
		),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query in Dutch or English, e.g. 'melk', 'cola', 'pindakaas', 'chicken'"),
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of results to return (default 10, max 30)"),
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

// --- ah_get_bonus_group_products ---

func registerGetBonusGroupProducts(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_bonus_group_products",
		mcp.WithTitleAnnotation("Albert Heijn: Bonus Group Products"),
		mcp.WithDescription(
			"Get all individual products belonging to a specific Albert Heijn bonus promotion group. "+
				"Use this to drill into a deal like '2+1 gratis kaas' or 'Alle yoghurt 25% korting'. "+
				"Get segment_id from the bonus_segment_id field in ah_get_bonus_offers results. "+
				"Returns the same fields as ah_search_products.",
		),
		mcp.WithString("segment_id",
			mcp.Required(),
			mcp.Description("Bonus segment ID from the bonus_segment_id field in ah_get_bonus_offers"),
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

		segmentID := req.GetString("segment_id", "")
		if segmentID == "" {
			return errResult("segment_id is required"), nil
		}

		products, err := c.GetBonusGroupProducts(ctx, segmentID)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get bonus group products for %s: %v", segmentID, err)), nil
		}

		type item struct {
			ID             int     `json:"id"`
			Title          string  `json:"title"`
			Price          float64 `json:"price"`
			BonusPrice     float64 `json:"bonus_price,omitempty"`
			Unit           string  `json:"unit,omitempty"`
			IsBonus        bool    `json:"is_bonus"`
			BonusMechanism string  `json:"bonus_mechanism,omitempty"`
			ImageURL       string  `json:"image_url,omitempty"`
		}
		results := make([]item, 0, len(products))
		for _, p := range products {
			it := item{
				ID:             p.ID,
				Title:          p.Title,
				IsBonus:        p.IsBonus,
				Unit:           p.UnitSize,
				BonusMechanism: p.BonusMechanism,
			}
			if p.IsBonus {
				it.BonusPrice = p.Price.Now
				it.Price = p.Price.Was
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
		mcp.WithTitleAnnotation("Albert Heijn: Bonus Offers"),
		mcp.WithDescription(
			"Get current Albert Heijn bonus/promotional offers. "+
				"Use this (not ah_search_products) when the user asks what is on bonus/sale/discount. "+
				"Supports optional keyword filter to find e.g. cheese on bonus: set query='kaas'. "+
				"Group deals (e.g. '2+1 gratis', 'Alle yoghurt 25% korting') have id=0 and a non-empty bonus_segment_id — "+
				"pass that to ah_get_bonus_group_products to see the individual products in the group. "+
				"Returns id, bonus_segment_id, title, original_price, bonus_price, discount_percentage, bonus_mechanism.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of results to return (default 20)"),
		),
		mcp.WithString("query",
			mcp.Description("Optional keyword filter (Dutch or English) applied client-side, e.g. 'kaas', 'vlees', 'bier'"),
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
		query := strings.ToLower(req.GetString("query", ""))

		// GetBonusProducts fetches all categories and fails if any one errors.
		// Fall back to spotlight (featured deals) on error so the tool always
		// returns something useful.
		products, err := c.GetBonusProducts(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] GetBonusProducts failed (%v), falling back to spotlight\n", err)
			products, err = c.GetSpotlightBonusProducts(ctx)
			if err != nil {
				return errResult(fmt.Sprintf("Failed to get bonus products: %v", err)), nil
			}
		}

		type item struct {
			ID                 int     `json:"id,omitempty"`
			BonusSegmentID     string  `json:"bonus_segment_id,omitempty"`
			Title              string  `json:"title"`
			OriginalPrice      float64 `json:"original_price,omitempty"`
			BonusPrice         float64 `json:"bonus_price"`
			DiscountPercentage float64 `json:"discount_percentage,omitempty"`
			BonusMechanism     string  `json:"bonus_mechanism,omitempty"`
		}
		results := make([]item, 0)
		for _, p := range products {
			if len(results) >= limit {
				break
			}
			// Client-side keyword filter when query is set.
			if query != "" && !strings.Contains(strings.ToLower(p.Title), query) {
				continue
			}
			it := item{
				ID:             p.ID,
				BonusSegmentID: p.BonusSegmentID,
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
		mcp.WithTitleAnnotation("Albert Heijn: Last-Chance Items"),
		mcp.WithDescription(
			"Get last-chance / vandaag-af / clearance items from an Albert Heijn store. "+
				"Requires a store_id (use ah_search_products to find stores, or provide postal_code to find the nearest store). "+
				"Uses the dedicated bargainItems GraphQL endpoint which returns today-only markdown deals.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of results to return (default 20)"),
		),
		mcp.WithString("store_id",
			mcp.Description("AH store ID (integer). Required to retrieve bargain items."),
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

// --- ah_search_stores ---

func registerSearchStores(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_search_stores",
		mcp.WithTitleAnnotation("Albert Heijn: Search Stores"),
		mcp.WithDescription(
			"Find Albert Heijn stores near a Dutch postal code. "+
				"If no postal_code is given, automatically uses the address from the member profile. "+
				"Returns store id (use this for ah_get_last_chance_items), name, type, and address.",
		),
		mcp.WithString("postal_code",
			mcp.Description("Dutch postal code, e.g. '1234AB'. Optional — falls back to member address."),
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

		postalCode := req.GetString("postal_code", "")

		// Auto-resolve from member profile when not provided.
		if postalCode == "" {
			member, mErr := c.GetMember(ctx)
			if mErr != nil {
				return errResult("No postal_code provided and could not fetch member address: " + mErr.Error()), nil
			}
			postalCode = member.Address.PostalCode
			if postalCode == "" {
				return errResult("No postal_code provided and member profile has no address on file."), nil
			}
		}

		stores, err := c.SearchStores(ctx, postalCode)
		if err != nil {
			return errResult(fmt.Sprintf("Store search failed for %s: %v", postalCode, err)), nil
		}
		if len(stores) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("No AH stores found near %s.", postalCode)), nil
		}

		type storeEntry struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			Type      string `json:"type,omitempty"`
			Street    string `json:"street,omitempty"`
			City      string `json:"city,omitempty"`
			PostalCode string `json:"postal_code,omitempty"`
		}
		results := make([]storeEntry, 0, len(stores))
		for _, st := range stores {
			results = append(results, storeEntry{
				ID:        st.ID,
				Name:      st.Name,
				Type:      st.StoreType,
				Street:    fmt.Sprintf("%s %s", st.Address.Street, st.Address.HouseNumber),
				City:      st.Address.City,
				PostalCode: st.Address.PostalCode,
			})
		}
		return jsonResult(results)
	})
}


// --- ah_get_product ---

func registerGetProduct(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_product",
		mcp.WithTitleAnnotation("Albert Heijn: Product Details"),
		mcp.WithDescription(
			"Get detailed information about a single Albert Heijn product by ID. "+
				"Returns title, brand, category, description, price, unit size, bonus info, NutriScore, property icons. "+
				"Set include_nutritional_info=true to also return calories, fat, protein, etc. "+
				"Get product_id from ah_search_products.",
		),
		mcp.WithString("product_id",
			mcp.Required(),
			mcp.Description("Numeric product ID from ah_search_products"),
		),
		mcp.WithString("include_nutritional_info",
			mcp.Description("Set to 'true' to include nutritional values (default false)"),
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

		productID := req.GetInt("product_id", 0)
		if productID == 0 {
			return errResult("product_id is required"), nil
		}
		includeNutri := req.GetString("include_nutritional_info", "") == "true"

		var p *appie.Product
		var pErr error
		if includeNutri {
			p, pErr = c.GetProductFull(ctx, productID)
		} else {
			p, pErr = c.GetProduct(ctx, productID)
		}
		if pErr != nil {
			return errResult(fmt.Sprintf("Failed to get product %d: %v", productID, pErr)), nil
		}

		type result struct {
			ID                   int         `json:"id"`
			Title                string      `json:"title"`
			Brand                string      `json:"brand,omitempty"`
			Category             string      `json:"category,omitempty"`
			ShortDescription     string      `json:"short_description,omitempty"`
			Price                float64     `json:"price"`
			BonusPrice           float64     `json:"bonus_price,omitempty"`
			UnitSize             string      `json:"unit_size,omitempty"`
			UnitPriceDescription string      `json:"unit_price_description,omitempty"`
			IsBonus              bool        `json:"is_bonus"`
			BonusMechanism       string      `json:"bonus_mechanism,omitempty"`
			NutriScore           string      `json:"nutri_score,omitempty"`
			IsAvailable          bool        `json:"is_available"`
			PropertyIcons        []string    `json:"property_icons,omitempty"`
			NutritionalInfo      interface{} `json:"nutritional_info,omitempty"`
			ImageURL             string      `json:"image_url,omitempty"`
		}
		r := result{
			ID:                   p.ID,
			Title:                p.Title,
			Brand:                p.Brand,
			Category:             p.Category,
			ShortDescription:     p.ShortDescription,
			IsBonus:              p.IsBonus,
			BonusMechanism:       p.BonusMechanism,
			NutriScore:           p.NutriScore,
			IsAvailable:          p.IsAvailable,
			UnitSize:             p.UnitSize,
			UnitPriceDescription: p.UnitPriceDescription,
			PropertyIcons:        p.PropertyIcons,
		}
		if p.IsBonus {
			r.BonusPrice = p.Price.Now
			r.Price = p.Price.Was
			if r.Price == 0 {
				r.Price = p.Price.Now
			}
		} else {
			r.Price = p.Price.Now
		}
		if len(p.Images) > 0 {
			r.ImageURL = p.Images[0].URL
		}
		if includeNutri && len(p.NutritionalInfo) > 0 {
			r.NutritionalInfo = p.NutritionalInfo
		}
		return jsonResult(r)
	})
}

// --- ah_search_products_filtered ---

func registerSearchProductsFiltered(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_search_products_filtered",
		mcp.WithTitleAnnotation("Albert Heijn: Search Products (Filtered)"),
		mcp.WithDescription(
			"Search Albert Heijn products with optional bonus filter. "+
				"Set bonus=true to return only products currently on promotion/sale. "+
				"Dutch search terms give best results: 'melk', 'kaas', 'vlees', etc.",
		),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query in Dutch or English"),
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of results (default 10, max 30)"),
		),
		mcp.WithString("bonus",
			mcp.Description("Set to 'true' to return only products currently on bonus/promotion"),
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
		bonus := req.GetString("bonus", "") == "true"

		products, err := c.SearchProductsFiltered(ctx, appie.SearchOptions{
			Query: query,
			Limit: limit,
			Bonus: bonus,
		})
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
			it := item{ID: p.ID, Title: p.Title, IsBonus: p.IsBonus, Unit: p.UnitSize}
			if p.IsBonus {
				it.BonusPrice = p.Price.Now
				it.Price = p.Price.Was
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
