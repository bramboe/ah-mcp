package tools

import "testing"

func TestTitleTokens(t *testing.T) {
	tokens := titleTokens("AH Halfvolle melk 2 liter")
	if !tokens["HALFVOLLE"] || !tokens["MELK"] {
		t.Errorf("expected HALFVOLLE and MELK, got %v", tokens)
	}
	if tokens["AH"] || tokens["LITER"] {
		t.Errorf("stopwords should be dropped: %v", tokens)
	}

	// Pure numbers and short tokens are dropped.
	tokens = titleTokens("Perla Superiore capsules 20 stuks")
	if tokens["20"] || tokens["STUKS"] {
		t.Errorf("numbers/units should be dropped: %v", tokens)
	}
	if !tokens["PERLA"] || !tokens["CAPSULES"] {
		t.Errorf("expected PERLA and CAPSULES, got %v", tokens)
	}
}

func TestSharedTokensMatching(t *testing.T) {
	product := titleTokens("AH Halfvolle melk 2L")
	offer := titleTokens("Alle AH melk")
	shared := sharedTokens(product, offer)
	if len(shared) != 1 || shared[0] != "MELK" {
		t.Errorf("expected [MELK], got %v", shared)
	}

	unrelated := titleTokens("Ecover afwasmiddel")
	if got := sharedTokens(product, unrelated); len(got) != 0 {
		t.Errorf("expected no overlap, got %v", got)
	}

	// Brand match: PHILADELPHIA product vs group offer.
	if got := sharedTokens(titleTokens("PHILADELPHIA"), titleTokens("Philadelphia")); len(got) != 1 {
		t.Errorf("brand should match case-insensitively, got %v", got)
	}
}
