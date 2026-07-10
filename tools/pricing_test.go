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
