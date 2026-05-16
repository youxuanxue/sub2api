package service

import "testing"

func TestWindowCostStickyReserveExplicitZero(t *testing.T) {
	account := &Account{Extra: map[string]any{
		"window_cost_limit":          100,
		"window_cost_sticky_reserve": 0,
	}}

	if got := account.GetWindowCostStickyReserve(); got != 0 {
		t.Fatalf("GetWindowCostStickyReserve() = %v, want 0", got)
	}
	if got := account.CheckWindowCostSchedulability(100); got != WindowCostNotSchedulable {
		t.Fatalf("CheckWindowCostSchedulability(100) = %v, want WindowCostNotSchedulable", got)
	}
}

func TestWindowCostStickyReserveDefault(t *testing.T) {
	account := &Account{Extra: map[string]any{"window_cost_limit": 100}}

	if got := account.GetWindowCostStickyReserve(); got != 10 {
		t.Fatalf("GetWindowCostStickyReserve() = %v, want 10", got)
	}
	if got := account.CheckWindowCostSchedulability(105); got != WindowCostStickyOnly {
		t.Fatalf("CheckWindowCostSchedulability(105) = %v, want WindowCostStickyOnly", got)
	}
}
