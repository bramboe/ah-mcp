package tools

import "testing"

func TestComputeTiersCherries(t *testing.T) {
	// AH Nederlandse kersen: priceBeforeBonus 4.99, tiered 1 stuk 30% / 2 stuks 50%.
	labels := []discountLabel{
		{Code: "DISCOUNT_TIERED_PERCENT", DefaultDescription: "2 stuks 50%", Count: 2, Percentage: 50},
		{Code: "DISCOUNT_TIERED_PERCENT", DefaultDescription: "1 stuk 30%", Count: 1, Percentage: 30},
	}
	tiers := computeTiers(4.99, labels)
	if len(tiers) != 2 {
		t.Fatalf("expected 2 tiers, got %d", len(tiers))
	}
	if tiers[0].PricePerPiece != 2.50 || tiers[0].TotalPrice != 5.00 {
		t.Errorf("2 stuks 50%%: got %+v", tiers[0])
	}
	if tiers[1].PricePerPiece != 3.49 {
		t.Errorf("1 stuk 30%%: got %+v", tiers[1])
	}
	if best := bestTierPrice(tiers); best != 2.50 {
		t.Errorf("best tier price = %v, want 2.50", best)
	}
}

func TestApplyPricingKoopzegel(t *testing.T) {
	// Cherries: bonus price 2.50 → koopzegel 6.12% = 0.15 → after 2.35.
	it := bonusOfferItem{OriginalPrice: 4.99}
	labels := []discountLabel{
		{Code: "DISCOUNT_TIERED_PERCENT", DefaultDescription: "2 stuks 50%", Count: 2, Percentage: 50},
	}
	it.applyPricing(4.99, labels)
	if it.BonusPrice != 2.50 {
		t.Fatalf("bonus_price = %v, want 2.50", it.BonusPrice)
	}
	if it.KoopzegelDiscount != 0.15 {
		t.Errorf("koopzegel_discount = %v, want 0.15", it.KoopzegelDiscount)
	}
	if it.PriceAfterKoopzegels != 2.35 {
		t.Errorf("price_after_koopzegels = %v, want 2.35", it.PriceAfterKoopzegels)
	}
	if it.DiscountPercentage != 49.9 && it.DiscountPercentage != 50 {
		t.Errorf("discount_percentage = %v, want ~50", it.DiscountPercentage)
	}
}

func TestComputeTiersOtherTypes(t *testing.T) {
	// 2 voor 4.99 (X_FOR_Y) — no base needed.
	x := computeTiers(0, []discountLabel{{Code: "DISCOUNT_X_FOR_Y", Count: 2, Price: 4.99}})
	if x[0].PricePerPiece != 2.50 || x[0].TotalPrice != 4.99 {
		t.Errorf("x_for_y: got %+v", x[0])
	}

	// 1+1 gratis (X_PLUS_Y_FREE) with base 3.00 → 2 pieces for 3.00 → 1.50 p.st.
	f := computeTiers(3.00, []discountLabel{{Code: "DISCOUNT_X_PLUS_Y_FREE", Count: 1, FreeCount: 1}})
	if f[0].Count != 2 || f[0].PricePerPiece != 1.50 || f[0].DiscountPercentage != 50 {
		t.Errorf("x_plus_y_free: got %+v", f[0])
	}

	// voor 3.79 (FIXED_PRICE) with base 4.99.
	fx := computeTiers(4.99, []discountLabel{{Code: "DISCOUNT_FIXED_PRICE", Price: 3.79}})
	if fx[0].PricePerPiece != 3.79 {
		t.Errorf("fixed_price: got %+v", fx[0])
	}

	// 25% korting (PERCENTAGE) with base 4.00 → 3.00.
	p := computeTiers(4.00, []discountLabel{{Code: "DISCOUNT_PERCENTAGE", Percentage: 25}})
	if p[0].PricePerPiece != 3.00 {
		t.Errorf("percentage: got %+v", p[0])
	}
}

func TestSearchProductToItem(t *testing.T) {
	// Bonus product with a tiered deal → full price picture.
	bonus := searchProduct{
		WebshopID: 1, Title: "Kersen", SalesUnitSize: "350 g", IsBonus: true,
		PriceBeforeBonus: 4.99, BonusMechanism: "2 Stapelen tot 50%",
		DiscountLabels: []discountLabel{{Code: "DISCOUNT_TIERED_PERCENT", Count: 2, Percentage: 50, DefaultDescription: "2 stuks 50%"}},
	}
	it := bonus.toItem()
	if it.Price != 4.99 || it.BonusPrice != 2.50 || it.KoopzegelDiscount != 0.15 || len(it.Tiers) != 1 {
		t.Errorf("bonus item mapping wrong: %+v", it)
	}
	if it.BonusMechanism == "" {
		t.Error("bonus_mechanism should be set")
	}

	// Non-bonus product → just the price, no bonus fields.
	plain := searchProduct{WebshopID: 2, Title: "Melk", CurrentPrice: 1.19}
	pit := plain.toItem()
	if pit.Price != 1.19 || pit.BonusPrice != 0 || pit.IsBonus || len(pit.Tiers) != 0 {
		t.Errorf("plain item mapping wrong: %+v", pit)
	}
}

func TestAlwaysDiscountPercent(t *testing.T) {
	// 1+1 gratis group (no base) → 50% surfaced even without a price.
	var o1 bonusOfferItem
	o1.applyPricing(0, []discountLabel{{Code: "DISCOUNT_X_PLUS_Y_FREE", Count: 1, FreeCount: 1, DefaultDescription: "1+1 gratis"}})
	if o1.DiscountPercentage != 50 {
		t.Errorf("1+1 gratis: discount=%v, want 50", o1.DiscountPercentage)
	}
	// 2e halve prijs → 25%.
	var o2 bonusOfferItem
	o2.applyPricing(0, []discountLabel{{Code: "DISCOUNT_ONE_HALF_PRICE", Count: 2, DefaultDescription: "2e halve prijs"}})
	if o2.DiscountPercentage != 25 {
		t.Errorf("2e halve prijs: discount=%v, want 25", o2.DiscountPercentage)
	}
	// With a base, per-piece is computed too.
	var o3 bonusOfferItem
	o3.applyPricing(2.00, []discountLabel{{Code: "DISCOUNT_ONE_HALF_PRICE", Count: 2}})
	if o3.Tiers[0].PricePerPiece != 1.50 {
		t.Errorf("2e halve prijs @2.00: per-piece=%v, want 1.50", o3.Tiers[0].PricePerPiece)
	}
}
