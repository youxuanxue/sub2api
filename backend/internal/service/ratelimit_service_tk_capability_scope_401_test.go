//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// The exact OpenAI OAuth 401 body from the GPT专线 incident (2026-06-13): a
// gpt-image-1 request hit an OAuth account whose token lacks the image scope.
const tkCapabilityScope401IncidentBody = `{"error":{"message":"You have insufficient permissions for this operation. Missing scopes: api.model.images.request. Check that you have the correct role in your organization (Reader, Writer, Owner) and project (Member, Owner), and if you're using a restricted API key, that it has the necessary scopes.","type":"invalid_request_error","code":"insufficient_scope"}}`

func TestTkIsCapabilityScope401(t *testing.T) {
	// The incident body — both anchors present → capability-scope 401.
	require.True(t, tkIsCapabilityScope401(401, []byte(tkCapabilityScope401IncidentBody)))

	// Same signature carried in the {"detail":...} envelope shape.
	require.True(t, tkIsCapabilityScope401(401,
		[]byte(`{"detail":"You have insufficient permissions for this operation. Missing scopes: api.model.images.request."}`)))

	// Generic invalid-credentials 401 → NOT capability-scope (must keep cooldown).
	require.False(t, tkIsCapabilityScope401(401,
		[]byte(`{"error":{"message":"invalid or expired credentials","type":"invalid_request_error"}}`)))
	require.False(t, tkIsCapabilityScope401(401, []byte(`{"detail":"Unauthorized"}`)))

	// Only one anchor present → NOT a match (precise, not a generic "scope" grep).
	require.False(t, tkIsCapabilityScope401(401,
		[]byte(`{"error":{"message":"Missing scopes: api.model.foo"}}`)))
	require.False(t, tkIsCapabilityScope401(401,
		[]byte(`{"error":{"message":"insufficient permissions for this operation"}}`)))

	// Non-401 status with the signature → NOT a match (this gate is 401-only).
	require.False(t, tkIsCapabilityScope401(403, []byte(tkCapabilityScope401IncidentBody)))
	require.False(t, tkIsCapabilityScope401(401, nil))
}

func TestTkCapabilityScope401ClientMessage(t *testing.T) {
	base := tkCapabilityScope401ClientMessage("")
	require.Contains(t, base, "not available on the serving account")
	require.Contains(t, base, "missing the required scope")

	withDetail := tkCapabilityScope401ClientMessage("Missing scopes: api.model.images.request")
	require.Contains(t, withDetail, "Upstream detail: Missing scopes: api.model.images.request")
}

// Branch (a): a capability-scope 401 must NOT cool/disable the WHOLE account.
// Image-scoped variants cool openai:image_generation only (see
// tkHandleOpenAIImageScope401Fastpath) and may failover; non-image variants stay
// SharedFault with no penalty.
func TestRateLimitService_HandleUpstreamError_CapabilityScope401_DoesNotCoolAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	// The incident account shape: OpenAI OAuth (id 9 "GPT-pro1" / id 48 "GPT-pro2").
	account := &Account{
		ID:       9,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
		},
	}

	body := []byte(tkCapabilityScope401IncidentBody)
	// Even when hit repeatedly (a heavy image client), the account is never cooled.
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)
		require.False(t, shouldDisable, "iteration %d: capability-scope 401 must not disable the account", i)
	}

	require.Equal(t, 0, repo.tempCalls, "capability-scope 401 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls, "capability-scope 401 must never SetError")
}

// Branch (b): a GENUINE account-level 401 (invalid/expired credentials, no
// capability-scope signature) must keep the EXISTING cooldown behavior. For an
// OAuth account this is a temp_unschedulable cooldown (give the refresh window).
func TestRateLimitService_HandleUpstreamError_GenericOAuth401_StillCoolsAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       9,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
		},
	}

	body := []byte(`{"error":{"message":"invalid or expired credentials","type":"invalid_request_error"}}`)
	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	require.True(t, shouldDisable, "generic 401 must still trigger failover/cooldown")
	require.Equal(t, 1, repo.tempCalls, "generic OAuth 401 must temp_unschedulable as before")
	require.Equal(t, 0, repo.setErrorCalls)
}

// Image-scoped capability 401 must failover so mixed pools can try another
// account; non-image capability-scope 401 stays SharedFault (no failover).
func TestOpenAIGatewayService_ShouldFailover_ImageCapabilityScope401AllowsFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	capBody := []byte(tkCapabilityScope401IncidentBody)
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(nil, 401, "", capBody),
		"image capability-scope 401 must failover")

	nonImage := []byte(`{"error":{"message":"You have insufficient permissions for this operation. Missing scopes: api.model.other.request.","type":"invalid_request_error","code":"insufficient_scope"}}`)
	require.True(t, tkIsCapabilityScope401(401, nonImage))
	require.False(t, tkIsImageCapabilityScope401(401, nonImage))
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(nil, 401, "", nonImage),
		"non-image capability-scope 401 must not failover")

	genericBody := []byte(`{"error":{"message":"invalid or expired credentials"}}`)
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(nil, 401, "invalid or expired credentials", genericBody),
		"generic 401 must still failover")
}

func TestOpenAIGatewayService_HandleOpenAIAccountUpstreamError_ImageScope401CoolsImageOnlyAndFailovers(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	body := []byte(tkCapabilityScope401IncidentBody)

	disabled := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body, "gpt-image-2.5-flare")
	require.True(t, disabled, "image-scope 401 must signal failover")
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, openAIImageGenerationRateLimitKey, repo.modelRateLimitCalls[0].scope)
	_, wholeAccountBlocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, wholeAccountBlocked)
	require.Zero(t, repo.tempCalls)
}

// Branch (b'): a genuine permanent-auth 401 ({"detail":"Unauthorized"}) on an
// OpenAI account must still SetError (permanent disable), unchanged.
func TestRateLimitService_HandleUpstreamError_OpenAIUnauthorized401_StillSetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 48, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	body := []byte(`{"detail":"Unauthorized"}`)
	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "permanent-auth 401 must SetError unchanged")
	require.Equal(t, 0, repo.tempCalls)
}
