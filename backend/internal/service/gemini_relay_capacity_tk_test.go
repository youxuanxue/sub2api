//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise forwarding entrypoints, including the chat error exit that used to
// turn an edge's empty pool into a prod-wide midnight cooldown.
func TestGeminiRelayCapacity_AllEntrypoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	envelopes := []struct {
		name   string
		status int
		body   string
	}{
		{"empty_pool", 429, `{"error":{"message":"No available accounts for this request. Please retry."}}`},
		{"rate_limit", 429, `{"error":{"message":"Upstream rate limit exceeded, please retry later"}}`},
		{"failover_exhausted", 502, `{"error":{"message":"All available accounts exhausted"}}`},
	}
	for _, wire := range []string{"chat", "responses", "messages", "native"} {
		for _, pool := range []bool{false, true} {
			for _, envelope := range envelopes {
				t.Run(fmt.Sprintf("%s/pool=%t/%s", wire, pool, envelope.name), func(t *testing.T) {
					account := antigravityEdgeRelayStub(61)
					account.Credentials["api_key"] = "test-relay-key"
					if !pool {
						delete(account.Credentials, "pool_mode")
					}
					repo := &rateLimitAccountRepoStub{}
					counter := &fakeAntigravitySaturationCounter{}
					limits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
					limits.SetAntigravitySaturationCounter(counter)
					upstream := &geminiCompatHTTPUpstreamStub{response: geminiCompatResponse(envelope.status, "edge-request", envelope.body)}
					svc := &GeminiMessagesCompatService{accountRepo: repo, rateLimitService: limits, httpUpstream: upstream, cfg: &config.Config{}}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
					var err error
					switch wire {
					case "chat":
						_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"gemini-3-flash","messages":[{"role":"user","content":"hi"}]}`))
					case "responses":
						_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(`{"model":"gemini-3-flash","input":"hi"}`))
					case "messages":
						_, err = svc.Forward(context.Background(), c, account, []byte(`{"model":"gemini-3-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
					case "native":
						_, err = svc.ForwardNative(context.Background(), c, account, "gemini-3-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
					}
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.Zero(t, repo.setRateLimitedCalls, "downstream capacity must not cool the prod account")
					require.Zero(t, repo.tempCalls)
					require.Zero(t, len(repo.modelRateLimitCalls))
					require.Equal(t, 1, upstream.calls, "switch to another edge instead of retrying this pool")
					require.Equal(t, []string{"gemini-3-flash-tiered"}, counter.modelKeys)
					require.Equal(t, AntigravityRelayCapacityReason, failover.Reason)
					require.Equal(t, envelope.status, failover.StatusCode)
					require.Equal(t, []byte(envelope.body), failover.ResponseBody)
					require.Equal(t, http.StatusTooManyRequests, failover.ClientStatusCode)
					require.False(t, failover.RetryableOnSameAccount)
					require.Equal(t, "5", failover.ResponseHeaders.Get("Retry-After"))
					require.False(t, c.Writer.Written(), "failover must leave the response uncommitted")
				})
			}
		}
	}
}

func TestGeminiRelayCapacity_RealProvider429StillCools(t *testing.T) {
	for _, scenario := range []string{"external_apikey", "oauth", "raw_provider_quota"} {
		t.Run(scenario, func(t *testing.T) {
			account := antigravityEdgeRelayStub(61)
			delete(account.Credentials, "pool_mode")
			body := []byte(`{"error":{"message":"Upstream rate limit exceeded, please retry later"}}`)
			switch scenario {
			case "external_apikey":
				account.Credentials["base_url"] = "https://generativelanguage.googleapis.com"
			case "oauth":
				account.Type = AccountTypeOAuth
			case "raw_provider_quota":
				body = []byte(`{"error":{"message":"Individual quota reached. Resets in 3h13m55s."}}`)
			}
			repo := &rateLimitAccountRepoStub{}
			svc := &GeminiMessagesCompatService{accountRepo: repo, rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
			svc.handleGeminiUpstreamError(context.Background(), account, 429, http.Header{}, body)
			require.Equal(t, 1, repo.setRateLimitedCalls)
			require.Equal(t, account.ID, repo.lastRateLimitedID)
		})
	}
}
