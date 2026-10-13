//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func promptCacheCandidateFixture() (*UniversalRoutingResolver, *globalCandidateRepo, *APIKey, *candidateIdentityTestCache) {
	// The billing label deliberately differs from the actual supply platform.
	groups := []Group{grp(10, PlatformOpenAI, 1, false)}
	accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 1, 10)}
	for i := range accounts {
		accounts[i].Platform, accounts[i].ChannelType = PlatformNewAPI, 1
		accounts[i].Credentials["model_mapping"] = map[string]any{"glm-5.3-flash": "glm-5.3-flash", "glm-5.3": "glm-5.3"}
		attachTestProtocolCapability(&accounts[i], protocolrouter.ProtocolChatCompletions)
	}
	r, repo, key := globalCandidateFixture(groups, accounts)
	cache := &candidateIdentityTestCache{}
	r.candidateGateway.cache = cache
	return r, repo, key, cache
}

func preparePromptCacheCandidate(t *testing.T, r *UniversalRoutingResolver, key *APIKey, model, prefix, user, explicit string) (context.Context, *CandidateRequest) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	messages := []map[string]string{}
	if prefix != "" {
		messages = append(messages, map[string]string{"role": "system", "content": prefix})
	}
	messages = append(messages, map[string]string{"role": "user", "content": user})
	body, err := json.Marshal(map[string]any{"model": model, "messages": messages})
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("api_key", key)
	if explicit != "" {
		c.Request.Header.Set("session-id", explicit)
	}
	state, err := r.PrepareCandidateIngress(c, key, ShapeOpenAIChat, c.Request.URL.Path, model, body, "")
	require.NoError(t, err, "candidate diagnostics: %+v", CandidateCapacityDiagFromError(err))
	return c.Request.Context(), state
}

func acquirePromptCacheCandidate(t *testing.T, ctx context.Context, state *CandidateRequest, excluded map[int64]struct{}) int64 {
	t.Helper()
	result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true, excluded: excluded})
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Nil(t, result.WaitPlan)
	result.ReleaseFunc()
	return result.Account.ID
}

func TestCandidatePromptCacheIndependentTurnsAndConflictingOldBinding(t *testing.T) {
	r, _, key, cache := promptCacheCandidateFixture()
	ctx, first := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared prefix", "first question", "")
	firstID := acquirePromptCacheCandidate(t, ctx, first, nil)
	// A different question with the same prefix keeps the same account.
	ctx, second := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared prefix", "second question", "")
	require.NotEqual(t, first.session, second.session)
	require.Equal(t, firstID, second.current.account.ID, "initial billing admission must see prefix affinity")
	require.True(t, second.current.sticky, "independent user turns must recover prefix affinity before admission")
	require.Equal(t, firstID, acquirePromptCacheCandidate(t, ctx, second, nil))
	// A retry must escape the failed account and update the shared prefix binding.
	otherID := acquirePromptCacheCandidate(t, ctx, second, map[int64]struct{}{firstID: {}})
	require.NotEqual(t, firstID, otherID)
	// Repeating the original question must not compete with its old user binding.
	staleID, err := cache.GetSessionAccountID(ctx, 0, first.session)
	require.NoError(t, err)
	require.Equal(t, firstID, staleID)
	ctx, repeated := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared prefix", "first question", "")
	require.Equal(t, otherID, repeated.current.account.ID)
	require.Equal(t, otherID, acquirePromptCacheCandidate(t, ctx, repeated, nil))
}

func TestCandidatePromptCacheIsolationAndExplicitSession(t *testing.T) {
	for _, scenario := range []string{"key", "user", "model", "prefix", "no-prefix", "explicit"} {
		t.Run(scenario, func(t *testing.T) {
			r, _, key, cache := promptCacheCandidateFixture()
			ctx, first := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared", "q1", "")
			id := acquirePromptCacheCandidate(t, ctx, first, nil)
			nextKey := *key
			model, prefix, explicit := "glm-5.3-flash", "shared", ""
			switch scenario {
			case "key":
				nextKey.ID++
			case "user":
				nextKey.UserID++
			case "model":
				model = "glm-5.3"
			case "prefix":
				prefix = "different"
			case "no-prefix":
				prefix = ""
			case "explicit":
				explicit = "client-session"
			}
			ctx, next := preparePromptCacheCandidate(t, r, &nextKey, model, prefix, "q2", explicit)
			paths, _, err := next.candidates(ctx, candidateSelectOptions{})
			require.NoError(t, err)
			for _, path := range paths {
				require.False(t, path.sticky, scenario)
			}
			if explicit != "" {
				otherID := int64(3) - id
				require.NoError(t, cache.SetSessionAccountID(ctx, 0, next.session, otherID, time.Hour))
				require.Equal(t, otherID, acquirePromptCacheCandidate(t, ctx, next, nil), "explicit session must retain sole affinity authority")
			}
		})
	}
}

func TestCandidatePromptCacheNeverOverridesEligibilityOrCapacity(t *testing.T) {
	for _, scenario := range []string{"revoked", "plan", "priority", "full", "slot-race", "failure-penalty"} {
		t.Run(scenario, func(t *testing.T) {
			r, repo, key, _ := promptCacheCandidateFixture()
			ctx, first := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared", "q1", "")
			id := acquirePromptCacheCandidate(t, ctx, first, nil)
			for i := range repo.accounts {
				if repo.accounts[i].ID != id {
					continue
				}
				switch scenario {
				case "revoked":
					repo.accounts[i].GroupIDs = nil
				case "plan":
					repo.accounts[i].Credentials["model_mapping"] = map[string]any{"unrelated-model": "unrelated-model"}
				case "priority":
					repo.accounts[i].Priority += 100
				}
			}
			switch scenario {
			case "full":
				r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{id: {AccountID: id, CurrentConcurrency: 10}}})
			case "slot-race":
				r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{id: false}})
			case "failure-penalty":
				counter := &candidateFailureTestCounter{counts: map[CandidateFailureScope]int64{{AccountID: id, Model: "glm-5.3-flash"}: candidateFailureThreshold}}
				r.candidateGateway.rateLimitService = &RateLimitService{openaiSaturationCounter: counter}
			}
			ctx, next := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared", "q2", "")
			require.NotEqual(t, id, acquirePromptCacheCandidate(t, ctx, next, nil))
		})
	}
}

func TestCandidatePromptCacheWaitedSlotPublishesAffinity(t *testing.T) {
	r, _, key, cache := promptCacheCandidateFixture()
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 10}, 2: {AccountID: 2, CurrentConcurrency: 10}}})
	ctx, state := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared", "q1", "")
	result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
	require.NoError(t, err)
	require.NotNil(t, result.WaitPlan)
	require.NoError(t, RecheckCandidateAccountSlot(ctx, result.Account.ID))
	id, err := cache.GetSessionAccountID(ctx, 0, state.promptCacheSession)
	require.NoError(t, err)
	require.Equal(t, result.Account.ID, id)
}

func TestCandidatePromptCacheDoesNotChangeOtherPlatforms(t *testing.T) {
	r, repo, key, cache := promptCacheCandidateFixture()
	ctx, first := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared", "q1", "")
	id := acquirePromptCacheCandidate(t, ctx, first, nil)
	for i := range repo.accounts {
		repo.accounts[i].Platform = PlatformOpenAI
		attachTestProtocolCapability(&repo.accounts[i], protocolrouter.ProtocolChatCompletions)
	}
	ctx, next := preparePromptCacheCandidate(t, r, key, "glm-5.3-flash", "shared", "q2", "")
	paths, _, err := next.candidates(ctx, candidateSelectOptions{})
	require.NoError(t, err)
	for _, path := range paths {
		require.False(t, path.sticky)
	}
	// Acquiring another platform must not overwrite the NewAPI prefix binding.
	chosen := acquirePromptCacheCandidate(t, ctx, next, map[int64]struct{}{id: {}})
	require.NotEqual(t, id, chosen)
	require.Equal(t, id, cache.entries[fmt.Sprintf("0:%s", first.promptCacheSession)])
}

func TestCandidatePromptCacheResponsesAndBodyExplicitSession(t *testing.T) {
	r, repo, key, cache := promptCacheCandidateFixture()
	for i := range repo.accounts {
		attachTestProtocolCapability(&repo.accounts[i], protocolrouter.ProtocolResponses)
	}
	prepare := func(user, explicit string) (context.Context, *CandidateRequest) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"model": "glm-5.3-flash", "instructions": "shared instructions", "input": user, "prompt_cache_key": explicit})
		require.NoError(t, err)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set("api_key", key)
		state, err := r.PrepareCandidateIngress(c, key, ShapeOpenAIChat, c.Request.URL.Path, "glm-5.3-flash", body, "")
		require.NoError(t, err)
		return c.Request.Context(), state
	}
	ctx, first := prepare("first", "")
	id := acquirePromptCacheCandidate(t, ctx, first, nil)
	ctx, next := prepare("second", "")
	require.True(t, next.current.sticky)
	require.Equal(t, id, acquirePromptCacheCandidate(t, ctx, next, nil))
	ctx, explicit := prepare("third", "client-cache-id")
	require.NoError(t, cache.SetSessionAccountID(ctx, 0, explicit.session, int64(3)-id, time.Hour))
	require.Equal(t, int64(3)-id, acquirePromptCacheCandidate(t, ctx, explicit, nil))
}
