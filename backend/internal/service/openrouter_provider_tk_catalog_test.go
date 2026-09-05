//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveOpenRouterProviderSupplyGroupIDs_FromBillingUserAllowedGroups(t *testing.T) {
	svc := &GatewayService{
		userRepo: &mockUserRepo{
			getByIDUser: &User{ID: 32, AllowedGroups: []int64{19, 1, 2, 1}},
		},
	}
	got, err := svc.ResolveOpenRouterProviderSupplyGroupIDs(context.Background(), OpenRouterProviderConfig{
		BillingUserID: 32,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []int64{1, 2, 19}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestResolveOpenRouterProviderSupplyGroupIDs_BillingUserEmptyGroups(t *testing.T) {
	svc := &GatewayService{
		userRepo: &mockUserRepo{getByIDUser: &User{ID: 32, AllowedGroups: nil}},
	}
	_, err := svc.ResolveOpenRouterProviderSupplyGroupIDs(context.Background(), OpenRouterProviderConfig{
		BillingUserID: 32,
	})
	if err == nil {
		t.Fatal("expected error when billing user has no allowed groups")
	}
}

func TestResolveOpenRouterProviderSupplyGroupIDs_RequiresBillingUser(t *testing.T) {
	// Boundary: missing billing_user_id must fail (legacy group_ids fallback removed).
	svc := &GatewayService{}
	_, err := svc.ResolveOpenRouterProviderSupplyGroupIDs(context.Background(), OpenRouterProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "billing_user_id required") {
		t.Fatalf("expected billing_user_id required, got %v", err)
	}
}

func TestResolveOpenRouterProviderSupplyGroupIDs_BillingUserLookupError(t *testing.T) {
	svc := &GatewayService{
		userRepo: &mockUserRepo{getByIDErr: errors.New("db down")},
	}
	_, err := svc.ResolveOpenRouterProviderSupplyGroupIDs(context.Background(), OpenRouterProviderConfig{
		BillingUserID: 32,
	})
	if err == nil {
		t.Fatal("expected lookup error")
	}
}
