package tools

import (
	"testing"
	"time"
)

func TestDiscountCategory(t *testing.T) {
	cases := map[string]string{
		"ZAANLANDER":           "bonus",
		"Philadelphia":         "bonus",
		"bio premium":          "premium",
		"MIJN AH MILES":        "miles",
		"KOOPZEGELS INLEVEREN": "koopzegels",
	}
	for name, want := range cases {
		if got := discountCategory(name); got != want {
			t.Errorf("discountCategory(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestResolvePeriod(t *testing.T) {
	from, to, err := resolvePeriod("2026-06", "", "")
	if err != nil || from != "2026-06-01" || to != "2026-06-30" {
		t.Errorf("month: got %s..%s err=%v", from, to, err)
	}

	from, to, err = resolvePeriod("", "2026-01-15", "2026-02-10")
	if err != nil || from != "2026-01-15" || to != "2026-02-10" {
		t.Errorf("range: got %s..%s err=%v", from, to, err)
	}

	// Default: current month up to today.
	from, to, err = resolvePeriod("", "", "")
	now := time.Now()
	wantFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	if err != nil || from != wantFrom || to != now.Format("2006-01-02") {
		t.Errorf("default: got %s..%s err=%v", from, to, err)
	}

	if _, _, err = resolvePeriod("2026-06", "2026-06-01", ""); err == nil {
		t.Error("month + from_date should conflict")
	}
	if _, _, err = resolvePeriod("juni", "", ""); err == nil {
		t.Error("invalid month should error")
	}
	if _, _, err = resolvePeriod("", "2026-03-01", "2026-02-01"); err == nil {
		t.Error("from after to should error")
	}
}
