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

// RegisterBonusMatchTools registers the bonus × purchase-history cross tool.
func RegisterBonusMatchTools(s *server.MCPServer, deps Deps) {
	registerGetBonusForFrequentItems(s, deps)
}

// matchStopwords are title tokens too generic to indicate a product match.
var matchStopwords = map[string]bool{
	"AH": true, "ALLE": true, "DE": true, "HET": true, "EEN": true,
	"OF": true, "EN": true, "MET": true, "VOOR": true, "PER": true,
	"GRAM": true, "GR": true, "KG": true, "ML": true, "LITER": true,
	"STUK": true, "STUKS": true, "PAK": true, "PAKKEN": true, "ZAK": true,
	"FLES": true, "VERPAKKING": true, "VOORDEELPAKKEN": true,
	// Generic descriptors that create false positives ("AH Biologisch
	// yoghurt" should not match "Alle AH Biologisch kaas" on BIOLOGISCH).
	"BIOLOGISCH": true, "BIOLOGISCHE": true, "BIO": true,
	"LEKKER": true, "LEKKERE": true, "LATER": true, "READY": true,
	"VERS": true, "VERSE": true, "EXTRA": true, "MINI": true,
	"KLEINVERPAKKING": true, "VOORDEELPAK": true, "VERSAFDELING": true,
	"ZELFBEDIENINGSAFDELING": true,
}

// titleTokens reduces a product/offer title to significant match tokens.
func titleTokens(s string) map[string]bool {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	tokens := map[string]bool{}
	for _, t := range strings.Fields(b.String()) {
		if len(t) < 3 || matchStopwords[t] {
			continue
		}
		if strings.IndexFunc(t, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
			continue // pure numbers (weights, counts) are not distinctive
		}
		tokens[t] = true
	}
	return tokens
}

// sharedTokens returns the tokens present in both sets.
func sharedTokens(a, b map[string]bool) []string {
	var shared []string
	for t := range a {
		if b[t] {
			shared = append(shared, t)
		}
	}
	sort.Strings(shared)
	return shared
}

// groupProductsQuery resolves the individual products of a bonus promotion
// group. Adapted from appie-go's group query, but with explicit period dates
// so it also works for next week's offers (the library hardcodes the current
// period).
const groupProductsQuery = `query FetchBonusPromotionWithProducts(
  $id: String, $periodStart: String, $periodEnd: String
) {
  bonusPromotions(input: {
    id: $id, periodStart: $periodStart, periodEnd: $periodEnd,
    filterUnavailableProducts: true, forcePromotionVisibility: true,
    showAllPromotionSegments: true
  }) {
    id
    products {
      id
      title
      priceV2(
        periodStart: $periodStart, periodEnd: $periodEnd,
        filterUnavailableProducts: true, forcePromotionVisibility: true
      ) {
        now { amount }
        was { amount }
        promotionLabel { tiers { description } }
      }
    }
  }
}`

type groupProduct struct {
	ID        int
	Title     string
	PriceNow  float64
	PriceWas  float64
	Mechanism string
}

// fetchGroupProducts returns the products of a bonus group for a period,
// cached for 30 minutes.
func fetchGroupProducts(ctx context.Context, c *appie.Client, segmentID, periodStart, periodEnd string) ([]groupProduct, error) {
	cacheKey := fmt.Sprintf("bonus:group:%s:%s", segmentID, periodStart)
	if cached, ok := GlobalCache.Get(cacheKey); ok {
		var products []groupProduct
		if err := unmarshalCached(cached, &products); err == nil {
			return products, nil
		}
	}

	var resp struct {
		BonusPromotions []struct {
			Products []struct {
				ID      int    `json:"id"`
				Title   string `json:"title"`
				PriceV2 struct {
					Now struct {
						Amount float64 `json:"amount"`
					} `json:"now"`
					Was struct {
						Amount float64 `json:"amount"`
					} `json:"was"`
					PromotionLabel *struct {
						Tiers []struct {
							Description string `json:"description"`
						} `json:"tiers"`
					} `json:"promotionLabel"`
				} `json:"priceV2"`
			} `json:"products"`
		} `json:"bonusPromotions"`
	}
	variables := map[string]any{"id": segmentID, "periodStart": periodStart, "periodEnd": periodEnd}
	if err := c.DoGraphQL(ctx, groupProductsQuery, variables, &resp); err != nil {
		return nil, err
	}

	var products []groupProduct
	if len(resp.BonusPromotions) > 0 {
		for _, p := range resp.BonusPromotions[0].Products {
			gp := groupProduct{ID: p.ID, Title: p.Title, PriceNow: p.PriceV2.Now.Amount, PriceWas: p.PriceV2.Was.Amount}
			if p.PriceV2.PromotionLabel != nil && len(p.PriceV2.PromotionLabel.Tiers) > 0 {
				gp.Mechanism = p.PriceV2.PromotionLabel.Tiers[0].Description
			}
			products = append(products, gp)
		}
	}
	if data, err := json.Marshal(products); err == nil {
		GlobalCache.Set(cacheKey, data, 30*time.Minute)
	}
	return products, nil
}

// resolveFromPrices enriches group offers (those with a bonus_segment_id) by
// resolving the group's cheapest product. It handles two cases:
//   - no single price (e.g. '1+1 gratis', '2e halve prijs'): sets a "vanaf"
//     (from) price by applying the offer's discount percentage to the base.
//   - a price but no discount % (e.g. '3 voor 6.00', 'voor 1.99'): derives the
//     discount % from the base price so every offer shows a percentage.
//
// Runs concurrently; individual failures are ignored.
func resolveFromPrices(ctx context.Context, c *appie.Client, offers []bonusOfferItem, periodStart, periodEnd string) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i := range offers {
		o := &offers[i]
		// Individual products already carry van/voor/%; only groups need this.
		if o.BonusSegmentID == "" {
			continue
		}
		if o.BonusPrice > 0 && o.DiscountPercentage > 0 {
			continue
		}
		wg.Add(1)
		go func(o *bonusOfferItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			products, err := fetchGroupProducts(ctx, c, o.BonusSegmentID, periodStart, periodEnd)
			if err != nil || len(products) == 0 {
				return
			}
			base := 0.0
			for _, p := range products {
				pr := p.PriceNow
				if pr <= 0 {
					pr = p.PriceWas
				}
				if pr > 0 && (base == 0 || pr < base) {
					base = pr
				}
			}
			if base == 0 {
				return
			}

			if o.BonusPrice == 0 {
				// Price-less deal: only claim a "vanaf" price when the discount
				// fraction is known (percentage/free deals), not for plain
				// amount-off deals where we cannot compute the exact price.
				if o.DiscountPercentage <= 0 {
					return
				}
				o.OriginalPrice = base
				o.BonusPrice = round2(base * (1 - o.DiscountPercentage/100))
				o.PriceFrom = true
			} else if o.DiscountPercentage == 0 && base > o.BonusPrice {
				// Priced deal (n voor X / voor X) without a %: derive it from
				// the cheapest normal price so a percentage is always shown.
				o.OriginalPrice = base
				o.DiscountPercentage = round1((1 - o.BonusPrice/base) * 100)
			}
			if o.BonusPrice > 0 {
				o.KoopzegelDiscount = round2(o.BonusPrice * koopzegelRate)
				o.PriceAfterKoopzegels = round2(o.BonusPrice - o.KoopzegelDiscount)
			}
		}(o)
	}
	wg.Wait()
}

// periodOffers returns the NATIONAL+SPOTLIGHT offers of a bonus period,
// cached for 30 minutes (a full fetch is ~30 section calls).
func periodOffers(ctx context.Context, c *appie.Client, period *bonusPeriodResp) ([]bonusOfferItem, error) {
	cacheKey := "bonus:period:" + period.BonusStartDate
	if cached, ok := GlobalCache.Get(cacheKey); ok {
		var offers []bonusOfferItem
		if err := unmarshalCached(cached, &offers); err == nil {
			return offers, nil
		}
	}
	offers, err := collectTabOffers(ctx, c, period, "NATIONAL", "SPOTLIGHT")
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(offers); err == nil {
		GlobalCache.Set(cacheKey, data, 30*time.Minute)
	}
	return offers, nil
}

// --- ah_get_bonus_for_frequent_items ---

func registerGetBonusForFrequentItems(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_bonus_for_frequent_items",
		mcp.WithTitleAnnotation("Albert Heijn: Bonus Deals on Your Frequent Products"),
		mcp.WithDescription(
			"Cross-reference the member's frequently ordered products with the bonus offers of a period: "+
				"answers 'which of my usual products are on sale this week (or next week)?' in one call. "+
				"Matches by product id where possible and by title keywords otherwise (match_type/matched_on show how). "+
				"Personal (Kies & Activeer) offers are included for the current period and marked personal=true — "+
				"those can be activated with ah_activate_personal_bonus. "+
				"period='next' works from the moment AH publishes next week's bonus (typically Friday).",
		),
		mcp.WithString("period",
			mcp.Description("'current' (default) or 'next' for next week's bonus"),
		),
		mcp.WithString("min_order_count",
			mcp.Description("Minimum times a product must have been ordered to be considered (default 2)"),
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of matches to return (default 30)"),
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

		period := req.GetString("period", "current")
		if period != "current" && period != "next" {
			return errResult("period must be 'current' or 'next'"), nil
		}
		minCount := req.GetInt("min_order_count", 2)
		limit := req.GetInt("limit", 30)
		start := time.Now()
		today := time.Now().Format("2006-01-02")

		// Select the requested bonus period.
		meta, err := fetchBonusMetadata(ctx, c)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get bonus metadata: %v", err)), nil
		}
		current, upcoming := selectPeriods(meta, today)
		selected := current
		if period == "next" {
			selected = upcoming
		}
		if selected == nil {
			msg := fmt.Sprintf("No %s bonus period available.", period)
			if period == "next" && current != nil && current.NextPeriodVisibleFrom != "" {
				msg = fmt.Sprintf("Next week's bonus offers are not published yet; they become visible from %s.",
					current.NextPeriodVisibleFrom)
			}
			return jsonResult(map[string]any{"available": false, "message": msg})
		}

		// Gather offers: national/spotlight for the period, plus personal
		// offers when looking at the current week.
		offers, err := periodOffers(ctx, c, selected)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get bonus offers: %v", err)), nil
		}
		type sourcedOffer struct {
			offer    bonusOfferItem
			personal bool
		}
		sourced := make([]sourcedOffer, 0, len(offers))
		for _, o := range offers {
			sourced = append(sourced, sourcedOffer{offer: o})
		}
		if period == "current" {
			if personal, err := fetchPersonalOffers(ctx, c); err == nil {
				for _, o := range numberAndSnapshot(personal) {
					sourced = append(sourced, sourcedOffer{offer: o, personal: true})
				}
			}
		}

		// Frequent products from order history (cached 30m).
		freq, err := computeFrequentProducts(ctx, c)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get frequent products: %v", err)), nil
		}

		// Match every frequent product against the offers.
		offerTokens := make([]map[string]bool, len(sourced))
		for i := range sourced {
			offerTokens[i] = titleTokens(sourced[i].offer.Title)
		}

		type match struct {
			ProductID       int            `json:"product_id"`
			ProductName     string         `json:"product_name"`
			OrderCount      int            `json:"order_count"`
			LastOrdered     string         `json:"last_ordered,omitempty"`
			MatchType       string         `json:"match_type"`
			MatchedOn       []string       `json:"matched_on,omitempty"`
			Personal        bool           `json:"personal,omitempty"`
			ResolvedExample string         `json:"resolved_example,omitempty"`
			Offer           bonusOfferItem `json:"offer"`
		}
		matches := make([]match, 0)
		for _, f := range freq {
			if f.Count < minCount || len(matches) >= limit {
				continue
			}
			pTokens := titleTokens(f.Name)

			bestIdx, bestScore, bestType := -1, 0, ""
			for i, so := range sourced {
				if so.offer.ID != 0 && so.offer.ID == f.ProductID {
					bestIdx, bestScore, bestType = i, 1000, "product_id"
					break
				}
				if score := len(sharedTokens(pTokens, offerTokens[i])); score > bestScore {
					bestIdx, bestScore, bestType = i, score, "title"
				}
			}
			if bestIdx < 0 {
				continue
			}
			m := match{
				ProductID:   f.ProductID,
				ProductName: f.Name,
				OrderCount:  f.Count,
				LastOrdered: f.LastOrderDate,
				MatchType:   bestType,
				Personal:    sourced[bestIdx].personal,
				Offer:       sourced[bestIdx].offer,
			}
			if bestType == "title" {
				m.MatchedOn = sharedTokens(pTokens, offerTokens[bestIdx])
			}
			matches = append(matches, m)
		}

		// Group deals (e.g. "2 VOOR 2.50") have no per-product bonus_price on
		// the section entry. Resolve the real price via the group endpoint:
		// prefer the specifically matched product, else the group's cheapest.
		// Capped to bound latency; results are cached 30m.
		const maxResolve = 15
		resolved := 0
		for i := range matches {
			off := &matches[i].Offer
			if off.BonusPrice > 0 || off.BonusSegmentID == "" || resolved >= maxResolve {
				continue
			}
			resolved++
			products, gErr := fetchGroupProducts(ctx, c, off.BonusSegmentID, selected.BonusStartDate, selected.BonusEndDate)
			if gErr != nil || len(products) == 0 {
				continue
			}
			chosen := products[0]
			for _, gp := range products {
				if gp.ID == matches[i].ProductID {
					chosen = gp
					break
				}
				if gp.PriceNow > 0 && (chosen.PriceNow == 0 || gp.PriceNow < chosen.PriceNow) {
					chosen = gp
				}
			}
			off.BonusPrice = chosen.PriceNow
			if off.OriginalPrice == 0 {
				off.OriginalPrice = chosen.PriceWas
			}
			if off.OriginalPrice > 0 && off.BonusPrice > 0 {
				off.DiscountPercentage = (1 - off.BonusPrice/off.OriginalPrice) * 100
			}
			matches[i].ResolvedExample = chosen.Title
		}

		LogInfo("ah_get_bonus_for_frequent_items", "period=%s offers=%d frequent=%d matches=%d duration=%v",
			period, len(sourced), len(freq), len(matches), time.Since(start))
		return jsonResult(map[string]any{
			"available":    true,
			"period_start": selected.BonusStartDate,
			"period_end":   selected.BonusEndDate,
			"matches":      matches,
		})
	})
}
