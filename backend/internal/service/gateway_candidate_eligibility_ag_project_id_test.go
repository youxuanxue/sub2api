package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayAccountEligibility_AntigravityOAuthRequiresProjectID(t *testing.T) {
	t.Parallel()

	svc := &GatewayService{}
	ctx := context.Background()

	hollow := &Account{
		ID:          245,
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "tok",
			"refresh_token": "ref",
			"model_mapping": map[string]any{"gemini-3-flash": "gemini-3-flash"},
		},
	}
	require.Equal(t, "missing_project_id",
		svc.gatewayAccountEligibilityReason(ctx, hollow, PlatformAntigravity, false, "gemini-3-flash", false))
	require.False(t, svc.gatewayAccountEligible(ctx, hollow, PlatformAntigravity, false, "gemini-3-flash", false))

	ready := &Account{
		ID:          246,
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "tok",
			"refresh_token": "ref",
			"project_id":    "proj-ready",
			"model_mapping": map[string]any{"gemini-3-flash": "gemini-3-flash"},
		},
	}
	// Other gates (profit/model/rpm) may still reject; project_id alone must not.
	require.NotEqual(t, "missing_project_id",
		svc.gatewayAccountEligibilityReason(ctx, ready, PlatformAntigravity, false, "gemini-3-flash", false))
}

func TestAntigravityOAuthProjectIDReady(t *testing.T) {
	t.Parallel()

	require.True(t, AntigravityOAuthProjectIDReady(nil))
	require.True(t, AntigravityOAuthProjectIDReady(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.False(t, AntigravityOAuthProjectIDReady(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "tok"},
	}))
	require.True(t, AntigravityOAuthProjectIDReady(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: map[string]any{"project_id": "p1"},
	}))
	require.True(t, AntigravityOAuthProjectIDReady(&Account{
		Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Credentials: map[string]any{antigravityProjectIDFallbackCredentialKey: "fallback"},
	}))
}
