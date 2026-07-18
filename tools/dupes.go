package tools

import (
	"context"
	"fmt"
	"strings"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Near-duplicate detection. Adding "De Cecco Lasagne all'uovo" when
// "Grand' Italia Lasagne all'uovo" is already in the order is not catchable by
// product id — they are different products. Comparing significant title tokens
// is, so these helpers flag functionally-equal items across the cart, an order
// and the shopping list.

// dupeMinSharedTokens is how many significant tokens two titles must share to
// count as near-duplicates. Two keeps real pairs (lasagne all'uovo: LASAGNE,
// ALL, UOVO) while rejecting brand-only overlap (Mutti pelati vs Mutti passata).
const dupeMinSharedTokens = 2

// namedItem is any cart/order/list entry that can be compared by title.
type namedItem struct {
	ID       int
	Title    string
	Quantity int
}

// dupePair is two entries considered near-duplicates.
type dupePair struct {
	A, B   namedItem
	Shared []string
}

// findDuplicatePairs returns every pair of distinct items whose titles share at
// least dupeMinSharedTokens significant tokens.
func findDuplicatePairs(items []namedItem) []dupePair {
	toks := make([]map[string]bool, len(items))
	for i, it := range items {
		toks[i] = titleTokens(it.Title)
	}
	var pairs []dupePair
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].ID != 0 && items[i].ID == items[j].ID {
				continue // same product: quantity, not a duplicate
			}
			if shared := sharedTokens(toks[i], toks[j]); len(shared) >= dupeMinSharedTokens {
				pairs = append(pairs, dupePair{A: items[i], B: items[j], Shared: shared})
			}
		}
	}
	return pairs
}

// renderDupeWarning renders pairs as a short human-readable warning, or "" when
// there are none.
func renderDupeWarning(pairs []dupePair) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("⚠️ %d mogelijk dubbele artikel(en) gevonden:\n", len(pairs)))
	for _, p := range pairs {
		b.WriteString(fmt.Sprintf("- %q (id %d, %dx) lijkt op %q (id %d, %dx) — beide: %s\n",
			p.A.Title, p.A.ID, p.A.Quantity, p.B.Title, p.B.ID, p.B.Quantity,
			strings.ToLower(strings.Join(p.Shared, ", "))))
	}
	b.WriteString("Controleer of dit bedoeld is; zo niet, verwijder er één met ah_remove_from_cart of ah_update_order_items.")
	return b.String()
}

// cartItems returns the current cart/basket contents as comparable items.
func cartItems(ctx context.Context, c *appie.Client) ([]namedItem, error) {
	order, err := c.GetOrder(ctx)
	if isNoActiveOrder(err) {
		basket, bErr := fetchBasketQL(ctx, c)
		if bErr != nil {
			return nil, bErr
		}
		items := make([]namedItem, 0, len(basket.Basket.Items))
		for _, it := range basket.Basket.Items {
			n := namedItem{ID: it.ID, Quantity: it.Quantity}
			if it.Product != nil {
				n.Title = it.Product.Title
			}
			items = append(items, n)
		}
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]namedItem, 0, len(order.Items))
	for _, it := range order.Items {
		n := namedItem{ID: it.ProductID, Quantity: it.Quantity}
		if it.Product != nil {
			n.Title = it.Product.Title
		}
		items = append(items, n)
	}
	return items, nil
}

// withDupeWarning appends a near-duplicate warning to a mutation's result
// message when the resulting cart contains functionally-equal items, so the
// caller notices straight away instead of shipping the order twice over.
// Best-effort: on any lookup failure the message is returned unchanged.
func withDupeWarning(ctx context.Context, c *appie.Client, msg string) string {
	items, err := cartItems(ctx, c)
	if err != nil {
		return msg
	}
	if w := renderDupeWarning(findDuplicatePairs(items)); w != "" {
		return msg + "\n\n" + w
	}
	return msg
}

// RegisterDupeTools registers the duplicate-detection MCP tool.
func RegisterDupeTools(s *server.MCPServer, deps Deps) {
	registerFindDuplicateItems(s, deps)
}

// --- ah_find_duplicate_items ---

func registerFindDuplicateItems(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_find_duplicate_items",
		mcp.WithTitleAnnotation("Albert Heijn: Find Duplicate Items"),
		mcp.WithDescription(
			"Find near-duplicate products in the shopping cart/basket, a delivery order, or the shopping list — "+
				"different products that are functionally the same, e.g. 'Grand' Italia Lasagne all'uovo' next to "+
				"'De Cecco Lasagne all'uovo'. These are different product ids, so they cannot be spotted by id. "+
				"ALWAYS call this after adding several products, and before submitting an order, to avoid buying the "+
				"same thing twice. Pass order_id to check a specific delivery order (from ah_get_order_history); "+
				"otherwise the active cart/basket is checked. Set include_shopping_list='true' to also check the list.",
		),
		mcp.WithString("order_id",
			mcp.Description("Numeric order ID to check (from ah_get_order_history). Omit to check the active cart/basket."),
		),
		mcp.WithString("include_shopping_list",
			mcp.Description("Set to 'true' to also scan the shopping list for duplicates"),
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

		var items []namedItem
		var source string
		if oid := req.GetInt("order_id", 0); oid != 0 {
			order, err := c.GetOrderDetails(ctx, oid)
			if err != nil {
				return errResult(fmt.Sprintf("Failed to get order %d: %v", oid, err)), nil
			}
			for _, it := range order.Items {
				n := namedItem{ID: it.ProductID, Quantity: it.Quantity}
				if it.Product != nil {
					n.Title = it.Product.Title
				}
				items = append(items, n)
			}
			source = fmt.Sprintf("bestelling %d", oid)
		} else {
			items, err = cartItems(ctx, c)
			if err != nil {
				return errResult(fmt.Sprintf("Failed to get cart: %v", err)), nil
			}
			source = "winkelmand"
		}

		if strings.EqualFold(req.GetString("include_shopping_list", ""), "true") {
			if list, err := c.GetShoppingListItems(ctx, ""); err == nil {
				for _, it := range list {
					title := it.Name
					if title == "" && it.Product != nil {
						title = it.Product.Title
					}
					items = append(items, namedItem{ID: it.ProductID, Title: title, Quantity: it.Quantity})
				}
				source += " + boodschappenlijst"
			}
		}

		pairs := findDuplicatePairs(items)
		LogInfo("ah_find_duplicate_items", "source=%s items=%d dupes=%d", source, len(items), len(pairs))
		if len(pairs) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("Geen dubbele artikelen gevonden in %s (%d artikelen gecontroleerd).", source, len(items))), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("In %s (%d artikelen):\n\n%s", source, len(items), renderDupeWarning(pairs))), nil
	})
}
