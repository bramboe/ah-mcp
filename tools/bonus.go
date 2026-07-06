package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	WebshopID        int     `json:"webshopId"`
	Title            string  `json:"title"`
	CurrentPrice     float64 `json:"currentPrice"`
	PriceBeforeBonus float64 `json:"priceBeforeBonus"`
	BonusMechanism   string  `json:"bonusMechanism"`
	SalesUnitSize    string  `json:"salesUnitSize"`
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
	// Personal (choose-and-activate) offers carry activation metadata.
	OfferID          json.Number `json:"offerId"`
	SegmentID        json.Number `json:"segmentId"`
	ActivationStatus string      `json:"activationStatus"`
}

// bonusOfferItem is the JSON shape returned to the agent. It matches the
// items produced by ah_get_bonus_offers so results are interchangeable.
type bonusOfferItem struct {
	ID                 int     `json:"id,omitempty"`
	BonusSegmentID     string  `json:"bonus_segment_id,omitempty"`
	Title              string  `json:"title"`
	OriginalPrice      float64 `json:"original_price,omitempty"`
	BonusPrice         float64 `json:"bonus_price"`
	DiscountPercentage float64 `json:"discount_percentage,omitempty"`
	BonusMechanism     string  `json:"bonus_mechanism,omitempty"`
	// Personal (choose-and-activate) offers only:
	OfferID          string `json:"offer_id,omitempty"`
	ActivationStatus string `json:"activation_status,omitempty"`
}

func (p *sectionProductResp) toOffer() bonusOfferItem {
	it := bonusOfferItem{
		ID:               p.WebshopID,
		Title:            p.Title,
		OriginalPrice:    p.PriceBeforeBonus,
		BonusPrice:       p.CurrentPrice,
		BonusMechanism:   p.BonusMechanism,
		OfferID:          p.OfferID.String(),
		ActivationStatus: p.ActivationStatus,
	}
	if it.OfferID == "" || it.OfferID == "0" {
		it.OfferID = ""
	}
	if it.OriginalPrice > 0 && it.BonusPrice > 0 {
		it.DiscountPercentage = (1 - it.BonusPrice/it.OriginalPrice) * 100
	}
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
	if it.OriginalPrice > 0 && it.BonusPrice > 0 {
		it.DiscountPercentage = (1 - it.BonusPrice/it.OriginalPrice) * 100
	}
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
// given bonus types, deduplicating both URLs and resulting offers.
func collectTabOffers(ctx context.Context, c *appie.Client, period *bonusPeriodResp, bonusTypes ...string) ([]bonusOfferItem, error) {
	wanted := make(map[string]bool, len(bonusTypes))
	for _, t := range bonusTypes {
		wanted[t] = true
	}

	seenURL := make(map[string]bool)
	seenOffer := make(map[string]bool)
	var offers []bonusOfferItem
	for _, tab := range period.Tabs {
		for _, meta := range tab.URLMetadataList {
			if !wanted[meta.BonusType] || seenURL[meta.URL] {
				continue
			}
			seenURL[meta.URL] = true
			sectionOffers, err := fetchSectionOffers(ctx, c, meta.URL)
			if err != nil {
				return nil, fmt.Errorf("section %q: %w", meta.Description, err)
			}
			for _, o := range sectionOffers {
				key := fmt.Sprintf("%d:%s:%s", o.ID, o.BonusSegmentID, o.Title)
				if !seenOffer[key] {
					seenOffer[key] = true
					offers = append(offers, o)
				}
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
}

// --- ah_activate_personal_bonus ---

func registerActivatePersonalBonus(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_activate_personal_bonus",
		mcp.WithTitleAnnotation("Albert Heijn: Activate Personal Bonus Offer"),
		mcp.WithDescription(
			"Activate one of the member's personal bonus offers (the AH app's 'Kies & Activeer' bonus box). "+
				"Get offer_id and segment_id from ah_get_personal_bonus_offers (offers with activation_status not yet ACTIVATED). "+
				"Standard accounts can activate up to 5 offers per bonus week, AH Premium members 10. "+
				"Returns the offer's activation status after the call.",
		),
		mcp.WithString("offer_id",
			mcp.Required(),
			mcp.Description("Offer ID from the offer_id field in ah_get_personal_bonus_offers"),
		),
		mcp.WithString("segment_id",
			mcp.Description("Bonus segment ID from the bonus_segment_id field (recommended; some offers require it)"),
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

		offerID := req.GetString("offer_id", "")
		if offerID == "" {
			return errResult("offer_id is required"), nil
		}
		segmentID := req.GetString("segment_id", "")
		startDate := req.GetString("start_date", "")
		if startDate == "" {
			startDate = currentPeriodStart(ctx, c)
		}

		body := map[string]any{"startDate": startDate}
		if segmentID != "" {
			// The API uses numeric segment ids; send a number when possible.
			if n, err := strconv.Atoi(segmentID); err == nil {
				body["segmentId"] = n
			} else {
				body["segmentId"] = segmentID
			}
		}

		path := "/mobile-services/bonuspage/v1/activate/" + url.PathEscape(offerID)
		var raw json.RawMessage
		if err := c.DoRequest(ctx, http.MethodPatch, path, body, &raw); err != nil {
			LogError("ah_activate_personal_bonus", "offer=%s err=%v", offerID, err)
			return errResult(fmt.Sprintf("Failed to activate offer %s: %v", offerID, err)), nil
		}

		// Activation changes the personal offer list; drop the cached copy and
		// look up the offer's new status for confirmation.
		GlobalCache.Invalidate("bonus:personal")
		status := "UNKNOWN"
		if offers, _, err := fetchChooseAndActivate(ctx, c, startDate); err == nil {
			for _, o := range offers {
				if o.OfferID == offerID {
					status = o.ActivationStatus
					break
				}
			}
		}

		type response struct {
			OfferID          string          `json:"offer_id"`
			ActivationStatus string          `json:"activation_status"`
			APIResponse      json.RawMessage `json:"api_response,omitempty"`
		}
		LogInfo("ah_activate_personal_bonus", "offer=%s status=%s", offerID, status)
		return jsonResult(response{OfferID: offerID, ActivationStatus: status, APIResponse: raw})
	})
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

		// The upcoming period is any period that starts after today. The
		// current period is the one covering today; AH adds the next period
		// to the metadata a few days before it starts.
		var upcoming *bonusPeriodResp
		var current *bonusPeriodResp
		for i := range meta.Periods {
			p := &meta.Periods[i]
			if p.BonusStartDate > today {
				if upcoming == nil || p.BonusStartDate < upcoming.BonusStartDate {
					upcoming = p
				}
			} else if p.BonusEndDate >= today {
				current = p
			}
		}

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
				"Requires login. Returns the same fields as ah_get_bonus_offers.",
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

		cacheKey := "bonus:personal"
		if cached, ok := GlobalCache.Get(cacheKey); ok {
			var offers []bonusOfferItem
			if err := unmarshalCached(cached, &offers); err == nil {
				LogInfo("ah_get_personal_bonus_offers", "cache_hit duration=%v", time.Since(start))
				return jsonResult(filterOffers(offers, query, limit))
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
		cacheOffers(cacheKey, offers)

		LogInfo("ah_get_personal_bonus_offers", "offers=%d duration=%v", len(offers), time.Since(start))
		return jsonResult(filterOffers(offers, query, limit))
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
