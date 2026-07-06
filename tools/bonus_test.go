package tools

import (
	"encoding/json"
	"testing"
)

func TestParseOfferPayloadEnvelopes(t *testing.T) {
	group := `{"id":"794985","offerId":178448,"segmentId":794985,"segmentDescription":"Alle Magnum ijs","discountDescription":"1 + 1 GRATIS","activationStatus":"AVAILABLE","exampleFromPrice":3.99,"exampleForPrice":2.0}`
	product := `{"webshopId":42,"title":"AH Melk","currentPrice":1.0,"priceBeforeBonus":2.0,"bonusMechanism":"50% korting","offerId":999,"activationStatus":"ACTIVATED"}`
	entryArray := `[{"bonusGroup":` + group + `},{"product":` + product + `}]`

	cases := map[string]string{
		"section object": `{"sectionType":"PO","bonusGroupOrProducts":` + entryArray + `}`,
		"bare array":     entryArray,
		"wrapped array":  `{"collection":` + entryArray + `}`,
		"flat groups":    `[` + group + `]`,
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			offers := parseOfferPayload(json.RawMessage(payload))
			if len(offers) == 0 {
				t.Fatalf("no offers parsed from %s", name)
			}
			first := offers[0]
			if first.OfferID != "178448" && first.OfferID != "999" {
				t.Errorf("offer_id not propagated: %+v", first)
			}
			if first.ActivationStatus == "" {
				t.Errorf("activation_status not propagated: %+v", first)
			}
		})
	}

	if offers := parseOfferPayload(json.RawMessage(`{"sectionType":"PO","bonusGroupOrProducts":[]}`)); len(offers) != 0 {
		t.Errorf("expected no offers for empty section, got %d", len(offers))
	}
}
