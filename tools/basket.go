package tools

import (
	"context"
	"fmt"
	"strings"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterBasketTools registers shopping-list MCP tools.
func RegisterBasketTools(s *server.MCPServer, deps Deps) {
	registerGetShoppingList(s, deps)
	registerAddToShoppingList(s, deps)
}

// --- ah_get_shopping_list ---

func registerGetShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_shopping_list",
		mcp.WithDescription("Get the contents of your Albert Heijn shopping list. Returns item names, quantities, and product IDs."),
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

		lists, err := c.GetShoppingLists(ctx, 0)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get shopping lists: %v", err)), nil
		}
		if len(lists) == 0 {
			return mcp.NewToolResultText("No shopping lists found."), nil
		}

		items, err := c.GetShoppingListItems(ctx, lists[0].ID)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get shopping list items: %v", err)), nil
		}

		// Enrich with product names if available
		var productIDs []int
		for _, item := range items {
			if item.ProductID > 0 {
				productIDs = append(productIDs, item.ProductID)
			}
		}
		productNames := map[int]string{}
		if len(productIDs) > 0 {
			products, pErr := c.GetProductsByIDs(ctx, productIDs)
			if pErr == nil {
				for _, p := range products {
					productNames[p.ID] = p.Title
				}
			}
		}

		type entry struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			ProductID int    `json:"product_id,omitempty"`
			Quantity  int    `json:"quantity"`
			Checked   bool   `json:"checked,omitempty"`
		}
		entries := make([]entry, 0, len(items))
		for _, item := range items {
			name := item.Name
			if name == "" {
				if n, ok := productNames[item.ProductID]; ok {
					name = n
				} else if item.ProductID > 0 {
					name = fmt.Sprintf("Product %d", item.ProductID)
				}
			}
			entries = append(entries, entry{
				ID:        item.ID,
				Name:      name,
				ProductID: item.ProductID,
				Quantity:  item.Quantity,
				Checked:   item.Checked,
			})
		}
		return jsonResult(entries)
	})
}

// --- ah_add_to_shopping_list ---

func registerAddToShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_add_to_shopping_list",
		mcp.WithDescription(
			"Add one or more products to your Albert Heijn shopping list. "+
				"Pass an array of items, each with product_id (int) and quantity (int). "+
				"Returns confirmation listing the names of successfully added products.",
		),
		mcp.WithArray("items",
			mcp.Required(),
			mcp.Description(`Array of items to add. Each item: {"product_id": 123456, "quantity": 2}`),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"product_id": map[string]any{"type": "integer"},
					"quantity":   map[string]any{"type": "integer"},
				},
				"required": []string{"product_id", "quantity"},
			}),
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

		rawItems, ok := req.GetArguments()["items"]
		if !ok {
			return errResult("items parameter is required"), nil
		}

		type inputItem struct {
			ProductID int `json:"product_id"`
			Quantity  int `json:"quantity"`
		}
		var inputItems []inputItem
		switch v := rawItems.(type) {
		case []any:
			for _, raw := range v {
				m, mOK := raw.(map[string]any)
				if !mOK {
					continue
				}
				pid := toInt(m["product_id"])
				qty := toInt(m["quantity"])
				if pid > 0 && qty > 0 {
					inputItems = append(inputItems, inputItem{ProductID: pid, Quantity: qty})
				}
			}
		default:
			return errResult("items must be an array"), nil
		}

		if len(inputItems) == 0 {
			return errResult("no valid items provided (each item needs product_id > 0 and quantity > 0)"), nil
		}

		listItems := make([]appie.ListItem, 0, len(inputItems))
		for _, it := range inputItems {
			listItems = append(listItems, appie.ListItem{
				ProductID: it.ProductID,
				Quantity:  it.Quantity,
			})
		}

		if err := c.AddToShoppingList(ctx, listItems); err != nil {
			return errResult(fmt.Sprintf("Failed to add items: %v", err)), nil
		}

		// Fetch product names for the confirmation message.
		var pids []int
		for _, it := range inputItems {
			pids = append(pids, it.ProductID)
		}
		products, pErr := c.GetProductsByIDs(ctx, pids)
		nameMap := map[int]string{}
		if pErr == nil {
			for _, p := range products {
				nameMap[p.ID] = p.Title
			}
		}

		names := make([]string, 0, len(inputItems))
		for _, it := range inputItems {
			if n, ok := nameMap[it.ProductID]; ok {
				names = append(names, fmt.Sprintf("%s (x%d)", n, it.Quantity))
			} else {
				names = append(names, fmt.Sprintf("Product %d (x%d)", it.ProductID, it.Quantity))
			}
		}
		return mcp.NewToolResultText(fmt.Sprintf("Added to shopping list:\n- %s", strings.Join(names, "\n- "))), nil
	})
}

// toInt converts interface{} to int, supporting float64 (JSON numbers) and int.
func toInt(v any) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	default:
		return 0
	}
}
