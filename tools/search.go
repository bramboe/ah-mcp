package tools

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	appie "github.com/gwillem/appie-go"
)

// The AH product search endpoint returns the full bonus picture per product
// (mechanism, shelf price, discount labels) — the same data the bonus sections
// carry. appie-go's SearchProducts drops the labels, so the search tools only
// showed a bare price. These helpers call the endpoint directly and run each
// product through the shared pricing logic, so search results carry the same
// original_price / bonus_price / discount_percentage / koopzegel / tiers as
// ah_get_bonus_offers.

type searchResponse struct {
	Products []searchProduct `json:"products"`
}

type searchProduct struct {
	WebshopID            int             `json:"webshopId"`
	Title                string          `json:"title"`
	SalesUnitSize        string          `json:"salesUnitSize"`
	UnitPriceDescription string          `json:"unitPriceDescription"`
	CurrentPrice         float64         `json:"currentPrice"`
	PriceBeforeBonus     float64         `json:"priceBeforeBonus"`
	IsBonus              bool            `json:"isBonus"`
	BonusMechanism       string          `json:"bonusMechanism"`
	DiscountLabels       []discountLabel `json:"discountLabels"`
	Images               []struct {
		URL string `json:"url"`
	} `json:"images"`
}

// searchItem is the enriched search result. It keeps the original fields
// (id, title, price, bonus_price, unit, is_bonus, image_url) and adds the
// bonus detail so callers always see how a promotion is priced.
type searchItem struct {
	ID                   int         `json:"id"`
	Title                string      `json:"title"`
	Unit                 string      `json:"unit,omitempty"`
	UnitPrice            string      `json:"unit_price,omitempty"`
	Price                float64     `json:"price"`
	BonusPrice           float64     `json:"bonus_price,omitempty"`
	BonusMechanism       string      `json:"bonus_mechanism,omitempty"`
	DiscountPercentage   float64     `json:"discount_percentage,omitempty"`
	KoopzegelDiscount    float64     `json:"koopzegel_discount,omitempty"`
	PriceAfterKoopzegels float64     `json:"price_after_koopzegels,omitempty"`
	Tiers                []priceTier `json:"tiers,omitempty"`
	IsBonus              bool        `json:"is_bonus"`
	ImageURL             string      `json:"image_url,omitempty"`
}

func (p *searchProduct) toItem() searchItem {
	it := searchItem{ID: p.WebshopID, Title: p.Title, Unit: p.SalesUnitSize, UnitPrice: p.UnitPriceDescription, IsBonus: p.IsBonus}
	if len(p.Images) > 0 {
		it.ImageURL = p.Images[0].URL
	}
	if p.IsBonus {
		off := bonusOfferItem{OriginalPrice: p.PriceBeforeBonus, BonusPrice: p.CurrentPrice, BonusMechanism: p.BonusMechanism}
		off.applyPricing(p.PriceBeforeBonus, p.DiscountLabels)
		it.Price = off.OriginalPrice
		if it.Price == 0 {
			it.Price = off.BonusPrice
		}
		it.BonusPrice = off.BonusPrice
		it.BonusMechanism = off.BonusMechanism
		it.DiscountPercentage = off.DiscountPercentage
		it.KoopzegelDiscount = off.KoopzegelDiscount
		it.PriceAfterKoopzegels = off.PriceAfterKoopzegels
		it.Tiers = off.Tiers
	} else {
		it.Price = p.CurrentPrice
	}
	return it
}

// searchProductsEnriched searches products and returns enriched items. When
// bonusOnly is set, only products currently on bonus are returned (the
// endpoint is over-fetched and filtered client-side to still fill limit).
func searchProductsEnriched(ctx context.Context, c *appie.Client, query string, limit int, bonusOnly bool) ([]searchItem, error) {
	if limit <= 0 {
		limit = 10
	}
	size := limit
	if bonusOnly {
		size = limit * 4 // over-fetch so we can fill limit after filtering
		if size > 100 {
			size = 100
		}
	}

	params := url.Values{}
	params.Set("query", query)
	params.Set("size", strconv.Itoa(size))
	path := "/mobile-services/product/search/v2?" + params.Encode()

	var resp searchResponse
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}

	items := make([]searchItem, 0, limit)
	for i := range resp.Products {
		p := &resp.Products[i]
		if bonusOnly && !p.IsBonus {
			continue
		}
		items = append(items, p.toItem())
		if len(items) >= limit {
			break
		}
	}
	return items, nil
}
