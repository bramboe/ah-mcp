package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// CacheTTLReceipt caches individual receipt details. Receipts are immutable,
// so a long TTL is safe; it only bounds memory usage.
const CacheTTLReceipt = 24 * time.Hour

// RegisterSavingsTools registers savings/discount-history MCP tools.
func RegisterSavingsTools(s *server.MCPServer, deps Deps) {
	registerGetSavingsSummary(s, deps)
}

// discountCategory classifies a receipt discount line by its name.
// AH names bonus discounts after the product/segment ("ZAANLANDER"),
// premium discounts "bio premium", miles redemptions "MIJN AH MILES",
// and koopzegel redemptions contain "KOOPZEGEL".
func discountCategory(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "KOOPZEGEL"):
		return "koopzegels"
	case strings.Contains(upper, "MILES"):
		return "miles"
	case strings.Contains(upper, "PREMIUM"):
		return "premium"
	default:
		return "bonus"
	}
}

// receiptStats is the per-receipt aggregate the summary is built from.
type receiptStats struct {
	ID                 string             `json:"id"`
	Date               string             `json:"date"`
	TotalPaid          float64            `json:"total_paid"`
	TotalDiscount      float64            `json:"total_discount"`
	DiscountByCategory map[string]float64 `json:"discount_by_category,omitempty"`
	DiscountLines      []discountLine     `json:"discount_lines,omitempty"`
	KoopzegelsBought   float64            `json:"koopzegels_bought,omitempty"`
}

type discountLine struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
}

// fetchReceiptStats loads one receipt's details (cached) and reduces it to stats.
func fetchReceiptStats(ctx context.Context, c *appie.Client, id, date string, totalPaid float64) (*receiptStats, error) {
	cacheKey := "receipt_stats:" + id
	if cached, ok := GlobalCache.Get(cacheKey); ok {
		var st receiptStats
		if err := unmarshalCached(cached, &st); err == nil {
			return &st, nil
		}
	}

	receipt, err := c.GetReceipt(ctx, id)
	if err != nil {
		return nil, err
	}

	st := receiptStats{
		ID:                 id,
		Date:               date,
		TotalPaid:          totalPaid,
		DiscountByCategory: map[string]float64{},
	}
	for _, d := range receipt.Discounts {
		amount := d.Amount
		if amount < 0 {
			amount = -amount
		}
		st.TotalDiscount += amount
		st.DiscountByCategory[discountCategory(d.Name)] += amount
		st.DiscountLines = append(st.DiscountLines, discountLine{Name: d.Name, Amount: amount})
	}
	for _, it := range receipt.Items {
		if strings.Contains(strings.ToUpper(it.Description), "KOOPZEGEL") {
			st.KoopzegelsBought += it.Amount
		}
	}

	if data, err := json.Marshal(st); err == nil {
		GlobalCache.Set(cacheKey, data, CacheTTLReceipt)
	}
	return &st, nil
}

// --- ah_get_savings_summary ---

func registerGetSavingsSummary(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_savings_summary",
		mcp.WithTitleAnnotation("Albert Heijn: Savings Summary"),
		mcp.WithDescription(
			"Summarise how much the member saved on Albert Heijn in-store receipts (kassabonnen) in a period: "+
				"total paid, total discount, split by category (bonus / premium / miles / koopzegels), "+
				"top discounted products, koopzegels bought, and per-receipt breakdown. "+
				"Use when the user asks e.g. how much bonus discount they got this month. "+
				"Includes both in-store receipts (kassabonnen) and online delivery orders. "+
				"Defaults to the current calendar month. Note: there is no live koopzegels balance in the AH API.",
		),
		mcp.WithString("month",
			mcp.Description("Calendar month YYYY-MM, e.g. '2026-06' (alternative to from_date/to_date)"),
		),
		mcp.WithString("from_date",
			mcp.Description("Start date YYYY-MM-DD (inclusive)"),
		),
		mcp.WithString("to_date",
			mcp.Description("End date YYYY-MM-DD (inclusive, defaults to today)"),
		),
		mcp.WithString("include_online",
			mcp.Description("Include online delivery orders (default 'true'); set 'false' for in-store receipts only"),
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

		from, to, err := resolvePeriod(
			req.GetString("month", ""),
			req.GetString("from_date", ""),
			req.GetString("to_date", ""),
		)
		if err != nil {
			return errResult(err.Error()), nil
		}
		start := time.Now()

		receipts, err := c.GetReceipts(ctx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get receipts: %v", err)), nil
		}

		// Receipt dates are "YYYY-MM-DD HH:MM"; prefix compare against the range.
		type listed struct {
			id    string
			date  string
			total float64
		}
		var inRange []listed
		for _, r := range receipts {
			day := r.Date
			if len(day) > 10 {
				day = day[:10]
			}
			if day >= from && day <= to {
				inRange = append(inRange, listed{id: r.TransactionID, date: r.Date, total: r.TotalAmount})
			}
		}

		// Fetch details with a small worker pool; tolerate individual failures.
		stats := make([]*receiptStats, len(inRange))
		errs := make([]error, len(inRange))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for i, r := range inRange {
			wg.Add(1)
			go func(i int, r listed) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				stats[i], errs[i] = fetchReceiptStats(ctx, c, r.id, r.date, r.total)
			}(i, r)
		}
		wg.Wait()

		type summary struct {
			From               string             `json:"from"`
			To                 string             `json:"to"`
			ReceiptCount       int                `json:"receipt_count"`
			OnlineOrderCount   int                `json:"online_order_count"`
			FailedReceipts     int                `json:"failed_receipts,omitempty"`
			TotalPaid          float64            `json:"total_paid"`
			TotalDiscount      float64            `json:"total_discount"`
			DiscountPercentage float64            `json:"discount_percentage,omitempty"`
			DiscountByCategory map[string]float64 `json:"discount_by_category"`
			KoopzegelsBought   float64            `json:"koopzegels_bought,omitempty"`
			TopDiscounts       []discountLine     `json:"top_discounts,omitempty"`
			Receipts           []*receiptStats    `json:"receipts"`
			OnlineOrders       []onlineOrderStat  `json:"online_orders,omitempty"`
		}
		out := summary{
			From:               from,
			To:                 to,
			DiscountByCategory: map[string]float64{},
		}

		byName := map[string]float64{}
		for i, st := range stats {
			if errs[i] != nil {
				out.FailedReceipts++
				LogError("ah_get_savings_summary", "receipt=%s err=%v", inRange[i].id, errs[i])
				continue
			}
			out.ReceiptCount++
			out.TotalPaid += st.TotalPaid
			out.TotalDiscount += st.TotalDiscount
			out.KoopzegelsBought += st.KoopzegelsBought
			for cat, amt := range st.DiscountByCategory {
				out.DiscountByCategory[cat] += amt
			}
			for _, dl := range st.DiscountLines {
				byName[dl.Name] += dl.Amount
			}
			out.Receipts = append(out.Receipts, st)
		}

		// Online delivery orders carry an order-level discount (bonus applied
		// at checkout) that never appears on in-store receipts.
		if !strings.EqualFold(req.GetString("include_online", "true"), "false") {
			orders, oErr := fetchOnlineOrderSavings(ctx, c, from, to)
			if oErr != nil {
				LogError("ah_get_savings_summary", "online orders err=%v", oErr)
			}
			for _, o := range orders {
				out.OnlineOrderCount++
				out.TotalPaid += o.TotalPaid
				out.TotalDiscount += o.Discount
				out.DiscountByCategory["online"] += o.Discount
				out.OnlineOrders = append(out.OnlineOrders, o)
			}
		}

		if gross := out.TotalPaid + out.TotalDiscount; gross > 0 {
			out.DiscountPercentage = out.TotalDiscount / gross * 100
		}
		for name, amt := range byName {
			out.TopDiscounts = append(out.TopDiscounts, discountLine{Name: name, Amount: amt})
		}
		sort.Slice(out.TopDiscounts, func(i, j int) bool { return out.TopDiscounts[i].Amount > out.TopDiscounts[j].Amount })
		if len(out.TopDiscounts) > 10 {
			out.TopDiscounts = out.TopDiscounts[:10]
		}
		sort.Slice(out.Receipts, func(i, j int) bool { return out.Receipts[i].Date > out.Receipts[j].Date })

		LogInfo("ah_get_savings_summary", "from=%s to=%s receipts=%d orders=%d discount=%.2f duration=%v",
			from, to, out.ReceiptCount, out.OnlineOrderCount, out.TotalDiscount, time.Since(start))
		return jsonResult(out)
	})
}

// onlineOrderStat is one online delivery order's savings contribution.
type onlineOrderStat struct {
	OrderID   int     `json:"order_id"`
	Date      string  `json:"date,omitempty"`
	TotalPaid float64 `json:"total_paid"`
	Discount  float64 `json:"discount"`
}

// fetchOnlineOrderSavings returns closed online delivery orders whose delivery
// date falls in [from, to], with their order-level discount and paid total.
func fetchOnlineOrderSavings(ctx context.Context, c *appie.Client, from, to string) ([]onlineOrderStat, error) {
	const query = `query OrderFulfillmentsClosed {
  orderFulfillments(status: CLOSED) {
    result {
      orderId
      totalPrice {
        discount { amount }
        totalPrice { amount }
      }
      delivery { slot { date } }
    }
  }
}`
	var resp struct {
		OrderFulfillments struct {
			Result []struct {
				OrderID    int `json:"orderId"`
				TotalPrice struct {
					Discount struct {
						Amount float64 `json:"amount"`
					} `json:"discount"`
					TotalPrice struct {
						Amount float64 `json:"amount"`
					} `json:"totalPrice"`
				} `json:"totalPrice"`
				Delivery struct {
					Slot struct {
						Date string `json:"date"`
					} `json:"slot"`
				} `json:"delivery"`
			} `json:"result"`
		} `json:"orderFulfillments"`
	}
	if err := c.DoGraphQL(ctx, query, nil, &resp); err != nil {
		return nil, err
	}

	var orders []onlineOrderStat
	for _, r := range resp.OrderFulfillments.Result {
		day := r.Delivery.Slot.Date
		if len(day) > 10 {
			day = day[:10]
		}
		if day < from || day > to {
			continue
		}
		discount := r.TotalPrice.Discount.Amount
		if discount < 0 {
			discount = -discount
		}
		orders = append(orders, onlineOrderStat{
			OrderID:   r.OrderID,
			Date:      day,
			TotalPaid: r.TotalPrice.TotalPrice.Amount,
			Discount:  discount,
		})
	}
	return orders, nil
}

// resolvePeriod turns month / from_date / to_date parameters into an
// inclusive [from, to] date range, defaulting to the current calendar month.
func resolvePeriod(month, fromDate, toDate string) (string, string, error) {
	const day = "2006-01-02"
	now := time.Now()

	if month != "" {
		if fromDate != "" || toDate != "" {
			return "", "", fmt.Errorf("use either month or from_date/to_date, not both")
		}
		t, err := time.Parse("2006-01", month)
		if err != nil {
			return "", "", fmt.Errorf("invalid month %q, expected YYYY-MM", month)
		}
		from := t.Format(day)
		to := t.AddDate(0, 1, -1).Format(day)
		return from, to, nil
	}

	to := now.Format(day)
	if toDate != "" {
		t, err := time.Parse(day, toDate)
		if err != nil {
			return "", "", fmt.Errorf("invalid to_date %q, expected YYYY-MM-DD", toDate)
		}
		to = t.Format(day)
	}
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format(day)
	if fromDate != "" {
		t, err := time.Parse(day, fromDate)
		if err != nil {
			return "", "", fmt.Errorf("invalid from_date %q, expected YYYY-MM-DD", fromDate)
		}
		from = t.Format(day)
	}
	if from > to {
		return "", "", fmt.Errorf("from_date %s is after to_date %s", from, to)
	}
	return from, to, nil
}
