package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterOrderTools registers order-related MCP tools.
func RegisterOrderTools(s *server.MCPServer, deps Deps) {
	registerGetOrderHistory(s, deps)
	registerGetFrequentItems(s, deps)
}

// --- ah_get_order_history ---

func registerGetOrderHistory(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_order_history",
		mcp.WithDescription(
			"Get recent Albert Heijn online order history. "+
				"Returns upcoming and recent orders including id, date, total_price, item_count, status. "+
				"Note: uses the orderFulfillments API which returns open (scheduled) orders.",
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of orders to return (default 10)"),
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

		limit := req.GetInt("limit", 10)

		fulfillments, err := c.GetFulfillments(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get order history: %v", err)), nil
		}

		type orderEntry struct {
			ID          int     `json:"id"`
			Date        string  `json:"date,omitempty"`
			TimeWindow  string  `json:"time_window,omitempty"`
			TotalPrice  float64 `json:"total_price"`
			ItemCount   int     `json:"item_count,omitempty"`
			Status      string  `json:"status"`
			ShoppingType string `json:"shopping_type,omitempty"`
			Modifiable  bool    `json:"modifiable"`
		}
		results := make([]orderEntry, 0, len(fulfillments))
		for i, f := range fulfillments {
			if i >= limit {
				break
			}
			date := f.Delivery.Slot.DateDisplay
			if date == "" {
				date = f.Delivery.Slot.Date
			}
			results = append(results, orderEntry{
				ID:          f.OrderID,
				Date:        date,
				TimeWindow:  f.Delivery.Slot.TimeDisplay,
				TotalPrice:  f.TotalPrice,
				Status:      f.StatusDescription,
				ShoppingType: f.ShoppingType,
				Modifiable:  f.Modifiable,
			})
		}
		return jsonResult(results)
	})
}

// --- ah_get_frequent_items ---

func registerGetFrequentItems(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_frequent_items",
		mcp.WithDescription(
			"Get frequently ordered products by analysing order history. "+
				"Fetches all fulfillments, expands each order's items, counts per product, "+
				"and returns products ordered at least min_order_count times. "+
				"Returns product_name, product_id, order_count, last_ordered_date.",
		),
		mcp.WithNumber("min_order_count",
			mcp.Description("Minimum number of orders a product must appear in (default 3)"),
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

		minCount := req.GetInt("min_order_count", 3)

		fulfillments, err := c.GetFulfillments(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get fulfillments: %v", err)), nil
		}

		type productStats struct {
			Name          string
			Count         int
			LastOrderDate string
		}
		stats := map[int]*productStats{}

		for _, f := range fulfillments {
			orderDate := f.Delivery.Slot.Date

			order, oErr := c.GetOrderDetails(ctx, f.OrderID)
			if oErr != nil {
				fmt.Fprintf(os.Stderr, "[ah-mcp] Warning: could not fetch order %d: %v\n", f.OrderID, oErr)
				continue
			}
			seen := map[int]bool{}
			for _, item := range order.Items {
				pid := item.ProductID
				if seen[pid] {
					continue
				}
				seen[pid] = true

				name := ""
				if item.Product != nil {
					name = item.Product.Title
				}
				if name == "" {
					name = strconv.Itoa(pid)
				}

				if stats[pid] == nil {
					stats[pid] = &productStats{Name: name}
				}
				stats[pid].Count++
				// Keep latest date
				if orderDate > stats[pid].LastOrderDate {
					stats[pid].LastOrderDate = orderDate
					stats[pid].Name = name // update name in case it was empty before
				}
			}
		}

		type item struct {
			ProductName     string `json:"product_name"`
			ProductID       int    `json:"product_id"`
			OrderCount      int    `json:"order_count"`
			LastOrderedDate string `json:"last_ordered_date,omitempty"`
		}
		results := []item{}
		for pid, s := range stats {
			if s.Count >= minCount {
				lastDate := s.LastOrderDate
				if t, err := time.Parse("2006-01-02", lastDate); err == nil {
					lastDate = t.Format("2006-01-02")
				}
				results = append(results, item{
					ProductName:     s.Name,
					ProductID:       pid,
					OrderCount:      s.Count,
					LastOrderedDate: lastDate,
				})
			}
		}
		sort.Slice(results, func(i, j int) bool {
			return results[i].OrderCount > results[j].OrderCount
		})
		return jsonResult(results)
	})
}
