package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// This file implements bonus tools that go beyond what appie-go exposes:
// upcoming-week offers and personal (member-specific) offers. Both are built
// on the raw bonuspage endpoints via appie.Client.DoRequest.

// --- bonuspage API response types (subset of the real payloads) ---

type bonusMetadataResp struct {
	Periods []bonusPeriodResp `json:"periods"`
}

type bonusPeriodResp struct {
	BonusStartDate        string `json:"bonusStartDate"`
	BonusEndDate          string `json:"bonusEndDate"`
	NextPeriodVisibleFrom string `json:"nextPeriodVisibleFrom"`
	Tabs                  []struct {
		Description     string `json:"description"`
		URLMetadataList []struct {
			URL         string `json:"url"`
			Count       int    `json:"count"`
			BonusType   string `json:"bonusType"`
			Description string `json:"description"`
		} `json:"urlMetadataList"`
	} `json:"tabs"`
}

type bonusSectionResp struct {
	BonusGroupOrProducts []struct {
		Product    *sectionProductResp `json:"product"`
		BonusGroup *sectionGroupResp   `json:"bonusGroup"`
	} `json:"bonusGroupOrProducts"`
}

type sectionProductResp struct {
	WebshopID            int             `json:"webshopId"`
	Title                string          `json:"title"`
	CurrentPrice         float64         `json:"currentPrice"`
	PriceBeforeBonus     float64         `json:"priceBeforeBonus"`
	BonusMechanism       string          `json:"bonusMechanism"`
	SalesUnitSize        string          `json:"salesUnitSize"`
	UnitPriceDescription string          `json:"unitPriceDescription"`
	DiscountLabels       []discountLabel `json:"discountLabels"`
	// Personal (choose-and-activate) offers carry activation metadata.
	OfferID          json.Number `json:"offerId"`
	ActivationStatus string      `json:"activationStatus"`
}

type sectionGroupResp struct {
	ID                  string               `json:"id"`
	SegmentDescription  string               `json:"segmentDescription"`
	DiscountDescription string               `json:"discountDescription"`
	ExampleFromPrice    float64              `json:"exampleFromPrice"`
	ExampleForPrice     float64              `json:"exampleForPrice"`
	Products            []sectionProductResp `json:"products"`
	DiscountLabels      []discountLabel      `json:"discountLabels"`
	// Personal (choose-and-activate) offers carry activation metadata.
	OfferID          json.Number `json:"offerId"`
	SegmentID        json.Number `json:"segmentId"`
	ActivationStatus string      `json:"activationStatus"`
}

// discountLabel is one promotion tier from the AH bonuspage. AH uses several
// codes; each maps to an effective price differently (see computeTiers).
type discountLabel struct {
	Code               string  `json:"code"`
	DefaultDescription string  `json:"defaultDescription"`
	Count              int     `json:"count"`
	FreeCount          int     `json:"freeCount"`
	Percentage         float64 `json:"percentage"`
	Price              float64 `json:"price"`
	Unit               string  `json:"unit"`
}

// priceTier is a computed, human-facing price step of a (possibly tiered)
// bonus offer, e.g. "1 stuk 30% → €3.49" and "2 stuks 50% → €2.50 p.st.".
type priceTier struct {
	Description        string  `json:"description"`
	Count              int     `json:"count,omitempty"`
	DiscountPercentage float64 `json:"discount_percentage,omitempty"`
	PricePerPiece      float64 `json:"price_per_piece,omitempty"`
	TotalPrice         float64 `json:"total_price,omitempty"`
	Unit               string  `json:"unit,omitempty"`
}

// bonusOfferItem is the JSON shape returned to the agent. It matches the
// items produced by ah_get_bonus_offers so results are interchangeable.
type bonusOfferItem struct {
	ID                 int     `json:"id,omitempty"`
	BonusSegmentID     string  `json:"bonus_segment_id,omitempty"`
	Title              string  `json:"title"`
	Unit               string  `json:"unit,omitempty"`
	UnitPrice          string  `json:"unit_price,omitempty"`
	OriginalPrice      float64 `json:"original_price,omitempty"`
	BonusPrice         float64 `json:"bonus_price"`
	PriceFrom          bool    `json:"price_from,omitempty"`
	DiscountPercentage float64 `json:"discount_percentage,omitempty"`
	BonusMechanism     string  `json:"bonus_mechanism,omitempty"`
	// Koopzegel (savings-stamp) value on top of the bonus: a full AH card
	// returns 6.12%. koopzegel_discount is that value on the bonus price;
	// price_after_koopzegels is the effective price once redeemed.
	KoopzegelDiscount    float64 `json:"koopzegel_discount,omitempty"`
	PriceAfterKoopzegels float64 `json:"price_after_koopzegels,omitempty"`
	// Tiers holds the per-step effective prices for tiered/stapel deals
	// (e.g. "1 stuk 30%", "2 stuks 50%"). Empty for simple single-price offers.
	Tiers []priceTier `json:"tiers,omitempty"`
	// Personal (choose-and-activate) offers only:
	Number           int    `json:"number,omitempty"`
	OfferID          string `json:"offer_id,omitempty"`
	ActivationStatus string `json:"activation_status,omitempty"`
}

// koopzegelRate is the effective return on AH koopzegels (savings stamps):
// a full digital card costs €49 and pays out €52 → 6.12% (AH, 2026).
const koopzegelRate = 0.0612

// round2 / round1 round to 2 / 1 decimals for money and percentages.
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// euro formats an amount as €X,XX (Dutch comma), empty for zero.
func euro(v float64) string {
	if v <= 0 {
		return "—"
	}
	return "€" + strings.Replace(fmt.Sprintf("%.2f", v), ".", ",", 1)
}

// perKg extracts the "€X" amount from AH's unit_price description
// ("normale prijs per kg €14.26") and returns it as "€14,26/kg".
func perKg(desc string) string {
	if desc == "" {
		return ""
	}
	idx := strings.LastIndex(desc, "€")
	if idx < 0 {
		return ""
	}
	amount := strings.TrimSpace(desc[idx:])
	unit := "kg"
	if strings.Contains(desc, "per l") || strings.Contains(desc, "per liter") {
		unit = "l"
	}
	return strings.Replace(amount, ".", ",", 1) + "/" + unit
}

// cell sanitises a value for a markdown table cell.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", "/")
}

// renderOffersTable renders offers as a markdown table. When numbered, the
// first column is the offer number and a Status column is appended (used by
// the personal bonus so the user can activate by number).
func renderOffersTable(offers []bonusOfferItem, numbered bool) string {
	var b strings.Builder
	if numbered {
		b.WriteString("| # | Product | Inhoud | Van | Voor | Korting | Na zegels | Normaal | Deal | Status |\n")
		b.WriteString("|--:|---|---|--:|--:|--:|--:|---|---|---|\n")
	} else {
		b.WriteString("| Product | Inhoud | Van | Voor | Korting | Na zegels | Normaal | Deal |\n")
		b.WriteString("|---|---|--:|--:|--:|--:|---|---|\n")
	}
	for _, o := range offers {
		korting := "—"
		if o.DiscountPercentage > 0 {
			korting = fmt.Sprintf("%.0f%%", o.DiscountPercentage)
		}
		van := euro(o.OriginalPrice)
		voor := euro(o.BonusPrice)
		if o.PriceFrom && o.BonusPrice > 0 {
			voor = "vanaf " + voor
		}
		na := euro(o.PriceAfterKoopzegels)
		normaal := cell(perKg(o.UnitPrice))
		if numbered {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
				o.Number, cell(o.Title), cell(o.Unit), van, voor, korting, na, normaal, cell(o.BonusMechanism), cell(o.ActivationStatus))
		} else {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
				cell(o.Title), cell(o.Unit), van, voor, korting, na, normaal, cell(o.BonusMechanism))
		}
	}
	return b.String()
}

// computeTiers turns AH discount labels into effective price tiers. base is the
// regular price (priceBeforeBonus / exampleFromPrice); it is required for
// percentage- and free-item deals but not for fixed/x-for-y deals.
func computeTiers(base float64, labels []discountLabel) []priceTier {
	tiers := make([]priceTier, 0, len(labels))
	for _, l := range labels {
		t := priceTier{Description: l.DefaultDescription, Count: l.Count}
		switch l.Code {
		case "DISCOUNT_TIERED_PERCENT", "DISCOUNT_PERCENTAGE":
			if t.Count == 0 {
				t.Count = 1
			}
			t.DiscountPercentage = l.Percentage
			if base > 0 {
				t.PricePerPiece = round2(base * (1 - l.Percentage/100))
				t.TotalPrice = round2(t.PricePerPiece * float64(t.Count))
			}
		case "DISCOUNT_X_FOR_Y", "DISCOUNT_TIERED_PRICE":
			// price is the total for `count` pieces (e.g. "2 stuks 2.49").
			if l.Count > 0 && l.Price > 0 {
				t.TotalPrice = round2(l.Price)
				t.PricePerPiece = round2(l.Price / float64(l.Count))
				if base > 0 {
					t.DiscountPercentage = round1((1 - t.PricePerPiece/base) * 100)
				}
			}
		case "DISCOUNT_FIXED_PRICE":
			if t.Count == 0 {
				t.Count = 1
			}
			t.PricePerPiece = round2(l.Price)
			t.TotalPrice = round2(l.Price * float64(t.Count))
			if base > 0 && l.Price > 0 {
				t.DiscountPercentage = round1((1 - l.Price/base) * 100)
			}
		case "DISCOUNT_X_PLUS_Y_FREE":
			pieces := l.Count + l.FreeCount
			t.Count = pieces
			if pieces > 0 {
				// The discount fraction is structural (pay Count of pieces),
				// so it is known even without a base price.
				t.DiscountPercentage = round1((1 - float64(l.Count)/float64(pieces)) * 100)
				if base > 0 {
					t.TotalPrice = round2(base * float64(l.Count))
					t.PricePerPiece = round2(t.TotalPrice / float64(pieces))
				}
			}
		case "DISCOUNT_ONE_HALF_PRICE":
			// "2e halve prijs": on Count items, one half is free — structural
			// saving of 0.5 unit spread over Count (25% for the usual count=2).
			if t.Count == 0 {
				t.Count = 2
			}
			t.DiscountPercentage = round1(0.5 / float64(t.Count) * 100)
			if base > 0 {
				t.PricePerPiece = round2(base * (1 - 0.5/float64(t.Count)))
				t.TotalPrice = round2(t.PricePerPiece * float64(t.Count))
			}
		case "DISCOUNT_WEIGHT":
			// Price per `count` units of `unit` (e.g. per 100 GRAM voor €2.69).
			t.Unit = l.Unit
			t.PricePerPiece = round2(l.Price)
		}
		tiers = append(tiers, t)
	}
	return tiers
}

// bestTierPrice returns the lowest positive per-piece price across weight-free
// tiers — the effective "from" bonus price to headline. Returns 0 if none.
func bestTierPrice(tiers []priceTier) float64 {
	best := 0.0
	for _, t := range tiers {
		if t.Unit != "" || t.PricePerPiece <= 0 {
			continue
		}
		if best == 0 || t.PricePerPiece < best {
			best = t.PricePerPiece
		}
	}
	return best
}

// applyPricing fills Tiers and guarantees BonusPrice/DiscountPercentage are set
// to the effective bonus price, even for tiered deals that carry no single
// currentPrice. base is the regular (pre-bonus) price.
func (it *bonusOfferItem) applyPricing(base float64, labels []discountLabel) {
	it.Tiers = computeTiers(base, labels)
	if it.BonusPrice == 0 {
		it.BonusPrice = bestTierPrice(it.Tiers)
	}
	if it.BonusPrice == 0 && base > 0 {
		it.BonusPrice = base // no usable discount data: at least show the price
	}
	if it.OriginalPrice > 0 && it.BonusPrice > 0 && it.BonusPrice < it.OriginalPrice {
		it.DiscountPercentage = round1((1 - it.BonusPrice/it.OriginalPrice) * 100)
	}
	// Always surface a discount % for bonus products: if it could not be
	// derived from prices (e.g. group '1+1 gratis' / '2e halve prijs' with no
	// price), fall back to the deepest structural tier percentage.
	if it.DiscountPercentage == 0 {
		best := 0.0
		for _, tr := range it.Tiers {
			if tr.DiscountPercentage > best {
				best = tr.DiscountPercentage
			}
		}
		it.DiscountPercentage = best
	}
	// Koopzegel value on the price actually paid (the bonus price).
	if it.BonusPrice > 0 {
		it.KoopzegelDiscount = round2(it.BonusPrice * koopzegelRate)
		it.PriceAfterKoopzegels = round2(it.BonusPrice - it.KoopzegelDiscount)
	}
}

// personalSnapshot holds the most recent full numbered personal offer list so
// ah_activate_personal_bonus can resolve numbers ("activate 1,3,4") to offer
// ids. Numbers are assigned on the full unfiltered list, so they stay stable
// regardless of the query/limit used when listing.
var personalSnapshot struct {
	sync.Mutex
	offers []bonusOfferItem
}

// numberAndSnapshot assigns 1-based numbers to the full personal offer list
// and stores it as the active snapshot.
func numberAndSnapshot(offers []bonusOfferItem) []bonusOfferItem {
	for i := range offers {
		offers[i].Number = i + 1
	}
	personalSnapshot.Lock()
	personalSnapshot.offers = offers
	personalSnapshot.Unlock()
	return offers
}

// snapshotOffer returns the snapshot entry for a 1-based number.
func snapshotOffer(n int) (bonusOfferItem, bool) {
	personalSnapshot.Lock()
	defer personalSnapshot.Unlock()
	if n < 1 || n > len(personalSnapshot.offers) {
		return bonusOfferItem{}, false
	}
	return personalSnapshot.offers[n-1], true
}

// clearPersonalSnapshot drops the numbered offer snapshot (e.g. on logout).
func clearPersonalSnapshot() {
	personalSnapshot.Lock()
	personalSnapshot.offers = nil
	personalSnapshot.Unlock()
}

// selectPeriods splits the metadata periods into the one covering today and
// the earliest upcoming one (nil when absent).
func selectPeriods(meta *bonusMetadataResp, today string) (current, upcoming *bonusPeriodResp) {
	for i := range meta.Periods {
		p := &meta.Periods[i]
		switch {
		case p.BonusStartDate > today:
			if upcoming == nil || p.BonusStartDate < upcoming.BonusStartDate {
				upcoming = p
			}
		case p.BonusEndDate >= today:
			current = p
		}
	}
	return current, upcoming
}

func (p *sectionProductResp) toOffer() bonusOfferItem {
	it := bonusOfferItem{
		ID:               p.WebshopID,
		Title:            p.Title,
		Unit:             p.SalesUnitSize,
		UnitPrice:        p.UnitPriceDescription,
		OriginalPrice:    p.PriceBeforeBonus,
		BonusPrice:       p.CurrentPrice,
		BonusMechanism:   p.BonusMechanism,
		OfferID:          p.OfferID.String(),
		ActivationStatus: p.ActivationStatus,
	}
	if it.OfferID == "" || it.OfferID == "0" {
		it.OfferID = ""
	}
	it.applyPricing(p.PriceBeforeBonus, p.DiscountLabels)
	return it
}

func (g *sectionGroupResp) toOffer() bonusOfferItem {
	it := bonusOfferItem{
		BonusSegmentID:   g.ID,
		Title:            g.SegmentDescription,
		OriginalPrice:    g.ExampleFromPrice,
		BonusPrice:       g.ExampleForPrice,
		BonusMechanism:   g.DiscountDescription,
		OfferID:          g.OfferID.String(),
		ActivationStatus: g.ActivationStatus,
	}
	if it.BonusSegmentID == "" {
		it.BonusSegmentID = g.SegmentID.String()
		if it.BonusSegmentID == "0" {
			it.BonusSegmentID = ""
		}
	}
	if it.OfferID == "" || it.OfferID == "0" {
		it.OfferID = ""
	}
	it.applyPricing(g.ExampleFromPrice, g.DiscountLabels)
	return it
}

// fetchBonusMetadata retrieves the raw bonuspage metadata (periods + tabs).
func fetchBonusMetadata(ctx context.Context, c *appie.Client) (*bonusMetadataResp, error) {
	var meta bonusMetadataResp
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/bonuspage/v3/metadata", nil, &meta); err != nil {
		return nil, fmt.Errorf("get bonus metadata: %w", err)
	}
	return &meta, nil
}

// fetchSectionOffers retrieves one bonus section. relURL is the relative URL
// as listed in the metadata urlMetadataList (e.g. "bonuspage/v2/section?...").
// Groups that contain products are flattened to the individual products,
// mirroring appie-go's collectBonusProducts.
func fetchSectionOffers(ctx context.Context, c *appie.Client, relURL string) ([]bonusOfferItem, error) {
	path := "/mobile-services/" + strings.TrimPrefix(relURL, "/")
	var section bonusSectionResp
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &section); err != nil {
		return nil, err
	}
	return sectionToOffers(section), nil
}

// sectionToOffers flattens a section response into offer items.
func sectionToOffers(section bonusSectionResp) []bonusOfferItem {
	var offers []bonusOfferItem
	for _, entry := range section.BonusGroupOrProducts {
		if entry.Product != nil {
			offers = append(offers, entry.Product.toOffer())
		}
		if entry.BonusGroup != nil {
			if len(entry.BonusGroup.Products) > 0 {
				group := entry.BonusGroup.toOffer()
				for i := range entry.BonusGroup.Products {
					it := entry.BonusGroup.Products[i].toOffer()
					// Products inside a personal group inherit the group's
					// activation metadata when they carry none themselves.
					if it.OfferID == "" {
						it.OfferID = group.OfferID
					}
					if it.ActivationStatus == "" {
						it.ActivationStatus = group.ActivationStatus
					}
					if it.BonusSegmentID == "" {
						it.BonusSegmentID = group.BonusSegmentID
					}
					offers = append(offers, it)
				}
			} else {
				offers = append(offers, entry.BonusGroup.toOffer())
			}
		}
	}
	return offers
}

// collectTabOffers fetches all section URLs of a period that match one of the
// given bonus types, deduplicating both URLs and resulting offers. Sections
// are fetched concurrently (a period has ~30 of them); the merged result
// keeps the tab order so output stays deterministic.
func collectTabOffers(ctx context.Context, c *appie.Client, period *bonusPeriodResp, bonusTypes ...string) ([]bonusOfferItem, error) {
	wanted := make(map[string]bool, len(bonusTypes))
	for _, t := range bonusTypes {
		wanted[t] = true
	}

	seenURL := make(map[string]bool)
	var urls, descs []string
	for _, tab := range period.Tabs {
		for _, meta := range tab.URLMetadataList {
			if !wanted[meta.BonusType] || seenURL[meta.URL] {
				continue
			}
			seenURL[meta.URL] = true
			urls = append(urls, meta.URL)
			descs = append(descs, meta.Description)
		}
	}

	sections := make([][]bonusOfferItem, len(urls))
	errs := make([]error, len(urls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sections[i], errs[i] = fetchSectionOffers(ctx, c, u)
		}(i, u)
	}
	wg.Wait()

	seenOffer := make(map[string]bool)
	var offers []bonusOfferItem
	for i := range urls {
		if errs[i] != nil {
			return nil, fmt.Errorf("section %q: %w", descs[i], errs[i])
		}
		for _, o := range sections[i] {
			key := fmt.Sprintf("%d:%s:%s", o.ID, o.BonusSegmentID, o.Title)
			if !seenOffer[key] {
				seenOffer[key] = true
				offers = append(offers, o)
			}
		}
	}
	return offers, nil
}

// cacheOffers stores an offer list in the global cache with the bonus TTL.
func cacheOffers(key string, offers []bonusOfferItem) {
	if data, err := json.Marshal(offers); err == nil {
		GlobalCache.Set(key, data, CacheTTLBonus)
	}
}

// unmarshalCached decodes cached JSON bytes into v.
func unmarshalCached(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// filterOffers applies the client-side keyword filter and limit used by all
// bonus listing tools.
func filterOffers(offers []bonusOfferItem, query string, limit int) []bonusOfferItem {
	query = strings.ToLower(query)
	results := make([]bonusOfferItem, 0)
	for _, o := range offers {
		if len(results) >= limit {
			break
		}
		if query != "" && !strings.Contains(strings.ToLower(o.Title), query) {
			continue
		}
		results = append(results, o)
	}
	return results
}

// RegisterBonusTools registers the extended bonus MCP tools.
func RegisterBonusTools(s *server.MCPServer, deps Deps) {
	registerGetUpcomingBonusOffers(s, deps)
	registerGetPersonalBonusOffers(s, deps)
	registerActivatePersonalBonus(s, deps)
	registerGetBonusOffersByType(s, deps)
}

// --- ah_get_bonus_offers_by_type ---

func registerGetBonusOffersByType(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_bonus_offers_by_type",
		mcp.WithTitleAnnotation("Albert Heijn: Bonus Offers by Type"),
		mcp.WithDescription(
			"Get Albert Heijn bonus offers of a specific type beyond the regular weekly bonus: "+
				"PREMIUM (AH Premium member deals), AHONLINE (online-only deals), "+
				"ETOS (Etos deals), GALL (Gall & Gall deals), GALLCARD (Gall & Gall loyalty card). "+
				"The available types depend on the account and week; when the requested type is not "+
				"available the response lists which types are. "+
				"For the regular bonus use ah_get_bonus_offers; for personal offers ah_get_personal_bonus_offers.",
		),
		mcp.WithString("bonus_type",
			mcp.Required(),
			mcp.Description("Bonus type, e.g. 'PREMIUM', 'AHONLINE', 'ETOS', 'GALL', 'GALLCARD'"),
		),
		mcp.WithString("period",
			mcp.Description("'current' (default) or 'next' for next week's offers"),
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of offers to return (default 20)"),
		),
		mcp.WithString("query",
			mcp.Description("Optional keyword filter (Dutch or English) applied client-side"),
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

		bonusType := strings.ToUpper(strings.TrimSpace(req.GetString("bonus_type", "")))
		if bonusType == "" {
			return errResult("bonus_type is required"), nil
		}
		period := req.GetString("period", "current")
		if period != "current" && period != "next" {
			return errResult("period must be 'current' or 'next'"), nil
		}
		limit := req.GetInt("limit", 20)
		query := req.GetString("query", "")
		start := time.Now()
		today := time.Now().Format("2006-01-02")

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
				msg = fmt.Sprintf("Next week's offers are not published yet; they become visible from %s.",
					current.NextPeriodVisibleFrom)
			}
			return jsonResult(map[string]any{"available": false, "message": msg})
		}

		// Which types does this period actually offer?
		available := map[string]bool{}
		for _, tab := range selected.Tabs {
			for _, m := range tab.URLMetadataList {
				available[m.BonusType] = true
			}
		}
		if !available[bonusType] {
			types := make([]string, 0, len(available))
			for t := range available {
				types = append(types, t)
			}
			sort.Strings(types)
			return jsonResult(map[string]any{
				"available":       false,
				"message":         fmt.Sprintf("No %s offers in this period for this account.", bonusType),
				"available_types": types,
			})
		}

		cacheKey := fmt.Sprintf("bonus:type:%s:%s", bonusType, selected.BonusStartDate)
		var offers []bonusOfferItem
		if cached, ok := GlobalCache.Get(cacheKey); ok {
			if err := unmarshalCached(cached, &offers); err == nil {
				LogInfo("ah_get_bonus_offers_by_type", "cache_hit type=%s duration=%v", bonusType, time.Since(start))
				return jsonResult(map[string]any{
					"available": true, "bonus_type": bonusType,
					"period_start": selected.BonusStartDate, "period_end": selected.BonusEndDate,
					"offers": filterOffers(offers, query, limit),
				})
			}
		}

		if err := withRetry(ctx, "ah_get_bonus_offers_by_type", func() error {
			var e error
			offers, e = collectTabOffers(ctx, c, selected, bonusType)
			return e
		}); err != nil {
			LogError("ah_get_bonus_offers_by_type", "type=%s err=%v", bonusType, err)
			return errResult(fmt.Sprintf("Failed to get %s offers: %v", bonusType, err)), nil
		}
		cacheOffers(cacheKey, offers)

		LogInfo("ah_get_bonus_offers_by_type", "type=%s offers=%d duration=%v", bonusType, len(offers), time.Since(start))
		return jsonResult(map[string]any{
			"available": true, "bonus_type": bonusType,
			"period_start": selected.BonusStartDate, "period_end": selected.BonusEndDate,
			"offers": filterOffers(offers, query, limit),
		})
	})
}

// --- ah_activate_personal_bonus ---

func registerActivatePersonalBonus(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_activate_personal_bonus",
		mcp.WithTitleAnnotation("Albert Heijn: Activate Personal Bonus Offers"),
		mcp.WithDescription(
			"Activate one or more of the member's personal bonus offers (the AH app's 'Kies & Activeer' bonus box). "+
				"Preferred: pass numbers='1,3,4' using the number field from a recent ah_get_personal_bonus_offers call. "+
				"Alternative: pass a single offer_id (with optional segment_id). "+
				"Standard accounts can activate up to 5 offers per bonus week, AH Premium members 10. "+
				"Returns the activation status per offer after the calls.",
		),
		mcp.WithString("numbers",
			mcp.Description("Comma-separated offer numbers from ah_get_personal_bonus_offers, e.g. '1,3,4'. Call that tool first in this server session."),
		),
		mcp.WithString("offer_id",
			mcp.Description("Single offer ID from the offer_id field (alternative to numbers)"),
		),
		mcp.WithString("segment_id",
			mcp.Description("Bonus segment ID belonging to offer_id (recommended; some offers require it)"),
		),
		mcp.WithString("start_date",
			mcp.Description("Bonus period start date YYYY-MM-DD (defaults to the current period's start)"),
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

		numbers := strings.TrimSpace(req.GetString("numbers", ""))
		offerID := req.GetString("offer_id", "")
		if numbers == "" && offerID == "" {
			return errResult("provide numbers (e.g. '1,3,4') or offer_id"), nil
		}
		startDate := req.GetString("start_date", "")
		if startDate == "" {
			startDate = currentPeriodStart(ctx, c)
		}

		// Resolve the offers to activate.
		type target struct {
			number    int
			offerID   string
			segmentID string
			title     string
		}
		var targets []target
		if numbers != "" {
			for _, field := range strings.FieldsFunc(numbers, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
				n, err := strconv.Atoi(field)
				if err != nil {
					return errResult(fmt.Sprintf("invalid number %q in numbers", field)), nil
				}
				o, ok := snapshotOffer(n)
				if !ok {
					return errResult(fmt.Sprintf(
						"number %d not found — call ah_get_personal_bonus_offers first to get the current numbered list", n)), nil
				}
				if o.OfferID == "" {
					return errResult(fmt.Sprintf("offer %d (%s) has no offer_id and cannot be activated", n, o.Title)), nil
				}
				targets = append(targets, target{number: n, offerID: o.OfferID, segmentID: o.BonusSegmentID, title: o.Title})
			}
		} else {
			targets = append(targets, target{offerID: offerID, segmentID: req.GetString("segment_id", "")})
		}

		// Activate each target.
		type result struct {
			Number           int    `json:"number,omitempty"`
			OfferID          string `json:"offer_id"`
			Title            string `json:"title,omitempty"`
			ActivationStatus string `json:"activation_status,omitempty"`
			Error            string `json:"error,omitempty"`
		}
		results := make([]result, 0, len(targets))
		for _, t := range targets {
			r := result{Number: t.number, OfferID: t.offerID, Title: t.title}
			if err := activateOffer(ctx, c, t.offerID, t.segmentID, startDate); err != nil {
				r.Error = err.Error()
				LogError("ah_activate_personal_bonus", "offer=%s err=%v", t.offerID, err)
			}
			results = append(results, r)
		}

		// Activation changes the personal offer list; drop the cached copy and
		// confirm the new status of each offer. The snapshot keeps its
		// numbering; only statuses are updated.
		GlobalCache.Invalidate("bonus:personal")
		if offers, _, err := fetchChooseAndActivate(ctx, c, startDate); err == nil {
			statusByOffer := make(map[string]string, len(offers))
			for _, o := range offers {
				statusByOffer[o.OfferID] = o.ActivationStatus
			}
			for i := range results {
				if s, ok := statusByOffer[results[i].OfferID]; ok {
					results[i].ActivationStatus = s
				}
			}
			personalSnapshot.Lock()
			for i := range personalSnapshot.offers {
				if s, ok := statusByOffer[personalSnapshot.offers[i].OfferID]; ok {
					personalSnapshot.offers[i].ActivationStatus = s
				}
			}
			personalSnapshot.Unlock()
		}

		LogInfo("ah_activate_personal_bonus", "activated=%d", len(results))
		return jsonResult(results)
	})
}

// activateOffer performs the activation PATCH for a single personal offer.
// The API expects segmentId and startDate as query parameters, not as a JSON
// body ("Required request parameter 'segmentId' ... is not present").
func activateOffer(ctx context.Context, c *appie.Client, offerID, segmentID, startDate string) error {
	params := url.Values{}
	params.Set("startDate", startDate)
	if segmentID != "" {
		params.Set("segmentId", segmentID)
	}
	path := "/mobile-services/bonuspage/v1/activate/" + url.PathEscape(offerID) + "?" + params.Encode()
	return c.DoRequest(ctx, http.MethodPatch, path, nil, nil)
}

// --- ah_get_upcoming_bonus_offers ---

func registerGetUpcomingBonusOffers(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_upcoming_bonus_offers",
		mcp.WithTitleAnnotation("Albert Heijn: Upcoming Bonus Offers (Next Week)"),
		mcp.WithDescription(
			"Get NEXT week's Albert Heijn bonus/promotional offers, before they become active. "+
				"Use this when the user asks about the bonus of next week / 'volgende week'. "+
				"AH publishes the upcoming period a few days before it starts (typically from Friday); "+
				"if it is not yet available the response says from which date it will be. "+
				"Returns period_start, period_end and offers with the same fields as ah_get_bonus_offers.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of offers to return (default 20)"),
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
		query := req.GetString("query", "")
		start := time.Now()
		today := time.Now().Format("2006-01-02")

		type response struct {
			Available           bool             `json:"available"`
			PeriodStart         string           `json:"period_start,omitempty"`
			PeriodEnd           string           `json:"period_end,omitempty"`
			Message             string           `json:"message,omitempty"`
			UpcomingVisibleFrom string           `json:"upcoming_visible_from,omitempty"`
			Offers              []bonusOfferItem `json:"offers,omitempty"`
		}

		var meta *bonusMetadataResp
		if err := withRetry(ctx, "ah_get_upcoming_bonus_offers", func() error {
			var e error
			meta, e = fetchBonusMetadata(ctx, c)
			return e
		}); err != nil {
			LogError("ah_get_upcoming_bonus_offers", "metadata failed err=%v", err)
			return errResult(fmt.Sprintf("Failed to get bonus metadata: %v", err)), nil
		}

		// AH adds the next period to the metadata a few days before it starts.
		current, upcoming := selectPeriods(meta, today)

		if upcoming == nil {
			resp := response{
				Available: false,
				Message:   "Next week's bonus offers are not published by Albert Heijn yet.",
			}
			if current != nil && current.NextPeriodVisibleFrom != "" {
				resp.UpcomingVisibleFrom = current.NextPeriodVisibleFrom
				resp.Message = fmt.Sprintf(
					"Next week's bonus offers are not published yet; they become visible from %s.",
					current.NextPeriodVisibleFrom,
				)
			}
			LogInfo("ah_get_upcoming_bonus_offers", "not_available duration=%v", time.Since(start))
			return jsonResult(resp)
		}

		cacheKey := fmt.Sprintf("bonus:upcoming:%s", upcoming.BonusStartDate)
		var offers []bonusOfferItem
		if cached, ok := GlobalCache.Get(cacheKey); ok {
			if err := unmarshalCached(cached, &offers); err == nil {
				LogInfo("ah_get_upcoming_bonus_offers", "cache_hit duration=%v", time.Since(start))
				return jsonResult(response{
					Available:   true,
					PeriodStart: upcoming.BonusStartDate,
					PeriodEnd:   upcoming.BonusEndDate,
					Offers:      filterOffers(offers, query, limit),
				})
			}
		}

		if err := withRetry(ctx, "ah_get_upcoming_bonus_offers", func() error {
			var e error
			offers, e = collectTabOffers(ctx, c, upcoming, "NATIONAL", "SPOTLIGHT")
			return e
		}); err != nil {
			LogError("ah_get_upcoming_bonus_offers", "sections failed err=%v", err)
			return errResult(fmt.Sprintf("Failed to get upcoming bonus offers: %v", err)), nil
		}
		cacheOffers(cacheKey, offers)

		LogInfo("ah_get_upcoming_bonus_offers", "period=%s offers=%d duration=%v",
			upcoming.BonusStartDate, len(offers), time.Since(start))
		return jsonResult(response{
			Available:   true,
			PeriodStart: upcoming.BonusStartDate,
			PeriodEnd:   upcoming.BonusEndDate,
			Offers:      filterOffers(offers, query, limit),
		})
	})
}

// --- ah_get_personal_bonus_offers ---

func registerGetPersonalBonusOffers(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_personal_bonus_offers",
		mcp.WithTitleAnnotation("Albert Heijn: Personal Bonus Offers"),
		mcp.WithDescription(
			"Get the logged-in member's PERSONAL Albert Heijn bonus offers ('persoonlijke bonus' / bonus box): "+
				"member-specific deals on top of the regular weekly bonus. "+
				"Use this when the user asks about their personal offers or bonus box. "+
				"Requires login. Each offer returns original_price, bonus_price, discount_percentage, "+
				"koopzegel_discount and price_after_koopzegels (6.12% koopzegel value), plus unit and unit_price. "+
				"By default this tool RETURNS A READY MARKDOWN TABLE with a # column (# | Product | Inhoud | Van | "+
				"Voor | Korting | Na zegels | Normaal | Deal | Status) — show it to the user as-is; the user can then "+
				"activate an offer with ah_activate_personal_bonus by its number. Pass format='json' for structured data.",
		),
		mcp.WithString("limit",
			mcp.Description("Maximum number of offers to return (default 20)"),
		),
		mcp.WithString("query",
			mcp.Description("Optional keyword filter (Dutch or English) applied client-side, e.g. 'kaas', 'koffie'"),
		),
		mcp.WithString("include_raw",
			mcp.Description("Set to 'true' to return the raw choose-and-activate API payload (debugging)"),
		),
		mcp.WithString("format",
			mcp.Description("'table' (default) returns a ready markdown table to show the user as-is; 'json' returns structured data"),
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
		query := req.GetString("query", "")
		start := time.Now()

		if strings.EqualFold(req.GetString("include_raw", ""), "true") {
			startDate := currentPeriodStart(ctx, c)
			_, raw, err := fetchChooseAndActivate(ctx, c, startDate)
			if err != nil {
				return errResult(fmt.Sprintf("choose-and-activate (bonusStartDate=%s) failed: %v", startDate, err)), nil
			}
			out := string(raw)
			if len(out) > 30000 {
				out = out[:30000] + "\n...truncated..."
			}
			return mcp.NewToolResultText(out), nil
		}

		asJSON := req.GetString("format", "table") == "json"
		render := func(offers []bonusOfferItem) (*mcp.CallToolResult, error) {
			shown := filterOffers(offers, query, limit)
			if asJSON {
				return jsonResult(shown)
			}
			return mcp.NewToolResultText(renderOffersTable(shown, true)), nil
		}

		cacheKey := "bonus:personal"
		if cached, ok := GlobalCache.Get(cacheKey); ok {
			var offers []bonusOfferItem
			if err := unmarshalCached(cached, &offers); err == nil {
				offers = numberAndSnapshot(offers)
				LogInfo("ah_get_personal_bonus_offers", "cache_hit duration=%v", time.Since(start))
				return render(offers)
			}
		}

		var offers []bonusOfferItem
		if err := withRetry(ctx, "ah_get_personal_bonus_offers", func() error {
			var e error
			offers, e = fetchPersonalOffers(ctx, c)
			return e
		}); err != nil {
			LogError("ah_get_personal_bonus_offers", "failed err=%v", err)
			return errResult(fmt.Sprintf(
				"Failed to get personal bonus offers: %v. "+
					"Personal offers require a logged-in AH member with a Bonuskaart linked to the account.", err)), nil
		}
		offers = numberAndSnapshot(offers)
		cacheOffers(cacheKey, offers)

		LogInfo("ah_get_personal_bonus_offers", "offers=%d duration=%v", len(offers), time.Since(start))
		return render(offers)
	})
}

// currentPeriodStart returns the bonusStartDate of the period covering today,
// falling back to today's date when the metadata is unavailable.
func currentPeriodStart(ctx context.Context, c *appie.Client) string {
	today := time.Now().Format("2006-01-02")
	meta, err := fetchBonusMetadata(ctx, c)
	if err != nil {
		return today
	}
	for i := range meta.Periods {
		p := &meta.Periods[i]
		if p.BonusStartDate <= today && p.BonusEndDate >= today {
			return p.BonusStartDate
		}
	}
	return today
}

// fetchChooseAndActivate retrieves the member's choose-and-activate personal
// offers (the AH app's "Kies & Activeer" bonus box), including activation
// status. Returns both the parsed offers and the raw payload for debugging.
func fetchChooseAndActivate(ctx context.Context, c *appie.Client, startDate string) ([]bonusOfferItem, json.RawMessage, error) {
	path := "/mobile-services/bonuspage/v1/choose-and-activate?bonusStartDate=" + startDate
	var raw json.RawMessage
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, nil, err
	}
	return parseOfferPayload(raw), raw, nil
}

// parseOfferPayload extracts offers from a bonus payload whose envelope
// varies per endpoint: an object with bonusGroupOrProducts, a bare array of
// {product|bonusGroup} entries, an object wrapping such an array under some
// key, or an array of flat group objects.
func parseOfferPayload(raw json.RawMessage) []bonusOfferItem {
	if len(raw) == 0 {
		return nil
	}

	var section bonusSectionResp
	if err := json.Unmarshal(raw, &section); err == nil {
		if offers := sectionToOffers(section); len(offers) > 0 {
			return offers
		}
	}

	var entries []struct {
		Product    *sectionProductResp `json:"product"`
		BonusGroup *sectionGroupResp   `json:"bonusGroup"`
	}
	if err := json.Unmarshal(raw, &entries); err == nil {
		section := bonusSectionResp{BonusGroupOrProducts: entries}
		if offers := sectionToOffers(section); len(offers) > 0 {
			return offers
		}
		// Array of flat group objects rather than {product|bonusGroup} wrappers.
		var groups []sectionGroupResp
		if err := json.Unmarshal(raw, &groups); err == nil {
			var offers []bonusOfferItem
			for i := range groups {
				if o := groups[i].toOffer(); o.Title != "" || o.OfferID != "" {
					offers = append(offers, o)
				}
			}
			if len(offers) > 0 {
				return offers
			}
		}
		return nil
	}

	// Object with the offer array under some wrapper key.
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		for _, v := range wrapper {
			trimmed := strings.TrimSpace(string(v))
			if strings.HasPrefix(trimmed, "[") {
				if offers := parseOfferPayload(v); len(offers) > 0 {
					return offers
				}
			}
		}
	}
	return nil
}

// fetchPersonalOffers retrieves member-specific offers. Primary source is the
// choose-and-activate endpoint (carries offer_id + activation_status); the
// personal bonus section and metadata PERSONAL/PREMIUM tabs are fallbacks.
func fetchPersonalOffers(ctx context.Context, c *appie.Client) ([]bonusOfferItem, error) {
	today := time.Now().Format("2006-01-02")
	startDate := currentPeriodStart(ctx, c)

	offers, _, caErr := fetchChooseAndActivate(ctx, c, startDate)
	if caErr == nil && len(offers) > 0 {
		return offers, nil
	}

	relURL := fmt.Sprintf("bonuspage/v2/section/personal?application=AHWEBSHOP&date=%s", today)
	secOffers, secErr := fetchSectionOffers(ctx, c, relURL)
	if secErr == nil && len(secOffers) > 0 {
		return secOffers, nil
	}

	// Tertiary: some accounts get personal deals via dedicated metadata tabs.
	if meta, err := fetchBonusMetadata(ctx, c); err == nil {
		for i := range meta.Periods {
			p := &meta.Periods[i]
			if p.BonusStartDate > today || p.BonusEndDate < today {
				continue
			}
			if tabOffers, err := collectTabOffers(ctx, c, p, "PERSONAL", "PREMIUM"); err == nil && len(tabOffers) > 0 {
				return tabOffers, nil
			}
		}
	}

	if caErr != nil && secErr != nil {
		return nil, fmt.Errorf("choose-and-activate: %v; personal section: %v", caErr, secErr)
	}
	return []bonusOfferItem{}, nil
}
