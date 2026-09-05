//go:build unit

package service

import (
	"context"
	"errors"
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
		GroupIDs:      []int64{99, 100}, // legacy must be ignored
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
		GroupIDs:      []int64{1},
	})
	if err == nil {
		t.Fatal("expected error when billing user has no allowed groups")
	}
}

func TestResolveOpenRouterProviderSupplyGroupIDs_LegacyFallbackWithoutBillingUser(t *testing.T) {
	svc := &GatewayService{}
	got, err := svc.ResolveOpenRouterProviderSupplyGroupIDs(context.Background(), OpenRouterProviderConfig{
		GroupIDs: []int64{2, 1, 2},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("got %v", got)
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
