package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

func TestAntigravityRequestContextManagerIdentityIsStable(t *testing.T) {
	account := &Account{
		ID:       42,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"antigravity_client_profile": "manager"},
	}
	ctx := antigravityRequestContext(context.Background(), account, "session-hash")
	if antigravity.ClientProfileForContext(ctx) != antigravity.ClientProfileManager {
		t.Fatal("manager account did not select manager context")
	}
	identity, ok := antigravity.ManagerIdentityForContext(ctx)
	if !ok || len(identity.MachineID) != 32 || len(identity.SessionID) != 36 {
		t.Fatalf("unexpected manager identity: %+v (ok=%v)", identity, ok)
	}
	retry := antigravityRequestContext(context.Background(), account, "session-hash")
	retryIdentity, _ := antigravity.ManagerIdentityForContext(retry)
	if identity != retryIdentity {
		t.Fatalf("identity changed across retries: %+v != %+v", identity, retryIdentity)
	}
}

func TestAntigravityRequestContextDefaultsToOfficialIDE(t *testing.T) {
	account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	ctx := antigravityRequestContext(context.Background(), account, "session-hash")
	if antigravity.ClientProfileForContext(ctx) != antigravity.ClientProfileIDE {
		t.Fatal("default account did not select official IDE context")
	}
	if _, ok := antigravity.ManagerIdentityForContext(ctx); ok {
		t.Fatal("IDE context unexpectedly carried Manager identity")
	}
}

func TestAntigravityRequestContextCLIOptInHasNoManagerIdentity(t *testing.T) {
	account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Extra: map[string]any{"antigravity_client_profile": "cli"}}
	ctx := antigravityRequestContext(context.Background(), account, "session-hash")
	if antigravity.ClientProfileForContext(ctx) != antigravity.ClientProfileCLI {
		t.Fatal("CLI opt-in account did not select CLI context")
	}
	if _, ok := antigravity.ManagerIdentityForContext(ctx); ok {
		t.Fatal("CLI context unexpectedly carried Manager identity")
	}
}
