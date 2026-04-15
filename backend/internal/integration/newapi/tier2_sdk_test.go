//go:build unit

package newapi

import "testing"

func TestBuildStripeCheckoutParamsDefaults(t *testing.T) {
	p := BuildStripeCheckoutParams(StripeCheckoutInput{
		ReferenceID: "ref-1",
		AmountCents: 1234,
		SuccessURL:  "https://example.com/success",
		CancelURL:   "https://example.com/cancel",
	})
	if p == nil {
		t.Fatalf("expected non-nil params")
	}
	if p.ClientReferenceID == nil || *p.ClientReferenceID != "ref-1" {
		t.Fatalf("unexpected reference id: %#v", p.ClientReferenceID)
	}
	if p.SuccessURL == nil || *p.SuccessURL == "" {
		t.Fatalf("expected success url")
	}
	if p.CancelURL == nil || *p.CancelURL == "" {
		t.Fatalf("expected cancel url")
	}
	if len(p.LineItems) != 1 {
		t.Fatalf("expected one line item, got %d", len(p.LineItems))
	}
	if p.LineItems[0].PriceData == nil || p.LineItems[0].PriceData.UnitAmount == nil || *p.LineItems[0].PriceData.UnitAmount != 1234 {
		t.Fatalf("unexpected stripe unit amount")
	}
}

func TestNewEPayClientValidateConfig(t *testing.T) {
	if _, err := NewEPayClient(EPayConfig{}); err == nil {
		t.Fatalf("expected validation error for empty config")
	}
}
