package tools

import (
	"context"
	"os"
	"testing"
	"time"

	appie "github.com/gwillem/appie-go"
)

// TestBonusLive exercises the raw bonuspage helpers against the real AH API
// using an anonymous token. Run with:
//
//	AH_LIVE_TEST=1 go test ./tools -run TestBonusLive -v
func TestBonusLive(t *testing.T) {
	if os.Getenv("AH_LIVE_TEST") != "1" {
		t.Skip("set AH_LIVE_TEST=1 to run live API tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := appie.New()
	if err := c.GetAnonymousToken(ctx); err != nil {
		t.Fatalf("anonymous token: %v", err)
	}

	meta, err := fetchBonusMetadata(ctx, c)
	if err != nil {
		t.Fatalf("fetchBonusMetadata: %v", err)
	}
	if len(meta.Periods) == 0 {
		t.Fatal("no bonus periods in metadata")
	}

	today := time.Now().Format("2006-01-02")
	var current *bonusPeriodResp
	for i := range meta.Periods {
		p := &meta.Periods[i]
		t.Logf("period %s -> %s (nextVisibleFrom=%s, tabs=%d)",
			p.BonusStartDate, p.BonusEndDate, p.NextPeriodVisibleFrom, len(p.Tabs))
		if p.BonusStartDate <= today && p.BonusEndDate >= today {
			current = p
		}
	}
	if current == nil {
		t.Fatal("no period covering today")
	}

	offers, err := collectTabOffers(ctx, c, current, "NATIONAL", "SPOTLIGHT")
	if err != nil {
		t.Fatalf("collectTabOffers: %v", err)
	}
	if len(offers) == 0 {
		t.Fatal("no offers in current period")
	}
	t.Logf("current period offers: %d", len(offers))
	for _, o := range offers[:min(5, len(offers))] {
		t.Logf("  id=%d segment=%q title=%q was=%.2f now=%.2f mechanism=%q",
			o.ID, o.BonusSegmentID, o.Title, o.OriginalPrice, o.BonusPrice, o.BonusMechanism)
	}
}
