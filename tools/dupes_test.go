package tools

import "testing"

// Real items from Bram's 19 July order, which contained two different lasagne
// products (the bug) alongside several genuinely distinct tomato products.
func TestFindDuplicatePairsRealOrder(t *testing.T) {
	items := []namedItem{
		{ID: 234872, Title: "Mutti Pomodori pelati", Quantity: 2},
		{ID: 481844, Title: "AH Tomaten passata gezeefd", Quantity: 1},
		{ID: 234853, Title: "Mutti Passata gezeefde fluweelzachte tomaten", Quantity: 3},
		{ID: 208514, Title: "Grand' Italia Lasagne all'uovo", Quantity: 2},
		{ID: 210489, Title: "De Cecco Lasagne all'uovo", Quantity: 2},
		{ID: 210483, Title: "De Cecco Mezzi rigatoni nr26", Quantity: 2},
		{ID: 67739, Title: "AH Biologisch Cherrytomaten", Quantity: 6},
		{ID: 209178, Title: "AH Biologisch Appel zak", Quantity: 2},
	}
	pairs := findDuplicatePairs(items)

	var found bool
	for _, p := range pairs {
		ids := map[int]bool{p.A.ID: true, p.B.ID: true}
		if ids[208514] && ids[210489] {
			found = true
		}
		// Brand-only overlap must not be flagged.
		if ids[234872] && ids[234853] {
			t.Errorf("false positive: two different Mutti tomato products flagged (%v)", p.Shared)
		}
		if ids[67739] && ids[209178] {
			t.Errorf("false positive: cherrytomaten vs appel flagged (%v)", p.Shared)
		}
	}
	if !found {
		t.Errorf("the two lasagne all'uovo products were not flagged; pairs=%d", len(pairs))
	}
}

func TestFindDuplicatePairsSameProductIsNotDuplicate(t *testing.T) {
	// The same product id twice is a quantity, not a duplicate.
	items := []namedItem{
		{ID: 111, Title: "AH Bananen tros", Quantity: 2},
		{ID: 111, Title: "AH Bananen tros", Quantity: 1},
	}
	if pairs := findDuplicatePairs(items); len(pairs) != 0 {
		t.Errorf("same product id should not be a duplicate, got %d pairs", len(pairs))
	}
}

func TestRenderDupeWarningEmpty(t *testing.T) {
	if w := renderDupeWarning(nil); w != "" {
		t.Errorf("expected empty warning, got %q", w)
	}
}
