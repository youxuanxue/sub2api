//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type candidateFailureTestCounter struct {
	OpenAISaturationCounterCache
	counts map[CandidateFailureScope]int64
	err    error
}

func (c *candidateFailureTestCounter) IncrementCandidateFailure(_ context.Context, scope CandidateFailureScope, _ int) (int64, error) {
	c.counts[scope]++
	return c.counts[scope], nil
}
func (c *candidateFailureTestCounter) GetCandidateFailures(_ context.Context, scopes []CandidateFailureScope) (map[CandidateFailureScope]int64, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := map[CandidateFailureScope]int64{}
	for _, scope := range scopes {
		out[scope] = c.counts[scope]
	}
	return out, nil
}

func TestCandidateFailurePriorityDirectUniversalAndModelIsolation(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformNewAPI, PlatformAnthropic, PlatformAntigravity, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek} {
		for _, direct := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/direct=%t", platform, direct), func(t *testing.T) {
				group := grp(10, platform, 1, false)
				group.AllowMessagesDispatch = true
				model, upstream := "gpt-5.4", "gpt-4o"
				protocol, shape, endpoint := protocolrouter.ProtocolChatCompletions, ShapeOpenAIChat, "/v1/chat/completions"
				if platform == PlatformAnthropic {
					model, upstream = "claude-fable-5", "claude-sonnet-4-6"
					protocol, shape, endpoint = protocolrouter.ProtocolMessages, ShapeAnthropicMessages, "/v1/messages"
				} else if platform == PlatformAntigravity {
					model, upstream = "gemini-3.8-flash", "gemini-2.5-flash"
					protocol = protocolrouter.ProtocolGeminiGenerateContent
				}
				accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 10)}
				for i := range accounts {
					accounts[i].Platform = platform
					accounts[i].ChannelType = 1
					accounts[i].Credentials["model_mapping"] = map[string]any{model: upstream}
					if platform == PlatformAntigravity {
						accounts[i].Type = AccountTypeOAuth
						accounts[i].Credentials["access_token"] = "test-token"
						accounts[i].Credentials["project_id"] = "test-project"
					}
					attachTestProtocolCapability(&accounts[i], protocol)
				}
				accounts[0].Concurrency = 1000
				r, _, key := globalCandidateFixture([]Group{group}, accounts)
				counter := &candidateFailureTestCounter{counts: map[CandidateFailureScope]int64{}}
				r.candidateGateway.rateLimitService = &RateLimitService{openaiSaturationCounter: counter}
				if direct {
					key.RoutingMode = "direct"
					key.GroupID = &group.ID
					key.Group = &group
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, model))
				ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, shape, endpoint, model, body, "", "")
				require.NoError(t, err)
				state.session = "existing"
				r.candidateGateway.cache = &stubGatewayCache{sessionBindings: map[string]int64{"existing": 1}}
				require.Equal(t, int64(1), state.current.account.ID)
				plan := *state.current.plan
				require.Equal(t, upstream, plan.ResolvedModel())
				failure := &UpstreamFailoverError{StatusCode: http.StatusBadGateway, Scope: GatewayFailureScopeAccount}
				for range 2 {
					_, err := ExecuteSelectedProtocol(ctx, r.router, &AccountSelectionResult{Account: &accounts[0], ProtocolPlan: &plan}, &accounts[0],
						func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(&accounts[0]),
						protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
							return nil, failure
						}))
					require.ErrorIs(t, err, failure)
				}
				picked, err := state.selectAccount(ctx, candidateSelectOptions{})
				require.NoError(t, err)
				require.Equal(t, int64(1), picked.Account.ID)
				observed := state.observeFailure(ctx, &accounts[0], plan, failure)
				require.ErrorIs(t, observed, failure)
				state.observeFailure(ctx, &accounts[0], plan, observed)
				_, _, legacyEligible := classifyOpenAIAPIKeyHealthFailure(observed)
				require.False(t, legacyEligible, "shared failure cannot also trigger the legacy account-wide breaker")
				require.Equal(t, int64(3), counter.counts[CandidateFailureScope{1, upstream}])
				require.Zero(t, counter.counts[CandidateFailureScope{1, model}])
				picked, err = state.selectAccount(ctx, candidateSelectOptions{})
				require.NoError(t, err)
				require.Equal(t, int64(2), picked.Account.ID, "large capacity cannot rescue the failing priority-1 account")
				state.continuationAccountID = 1
				picked, err = state.selectAccount(ctx, candidateSelectOptions{})
				require.NoError(t, err)
				require.Equal(t, int64(1), picked.Account.ID, "hard continuation cannot migrate due to a soft penalty")
				state.continuationAccountID = 0
				counter.err = errors.New("counter unavailable")
				picked, err = state.selectAccount(ctx, candidateSelectOptions{})
				require.NoError(t, err)
				require.Equal(t, int64(1), picked.Account.ID, "counter outage preserves configured priority")
				counter.err = nil
				counter.counts = map[CandidateFailureScope]int64{{1, "unrelated-model"}: 10}
				picked, err = state.selectAccount(ctx, candidateSelectOptions{})
				require.NoError(t, err)
				require.Equal(t, int64(1), picked.Account.ID)
			})
		}
	}
}

func TestCandidateFailureNativeExecutionUsesForwardModel(t *testing.T) {
	for _, tc := range []struct {
		name, platform, accountType, model, upstream string
		credentials                                  map[string]any
	}{
		{"gemini", PlatformGemini, AccountTypeAPIKey, "gemini-3.8-flash", "gemini-2.5-flash", map[string]any{"model_mapping": map[string]any{"gemini-3.8-flash": "gemini-2.5-flash"}}},
		{"kiro", PlatformKiro, AccountTypeOAuth, "claude-sonnet-4-5", "claude-sonnet-4.5", nil},
		{"bedrock", PlatformAnthropic, AccountTypeBedrock, "claude-sonnet-4-5", "eu.anthropic.claude-sonnet-4-5-20250929-v1:0", map[string]any{"aws_region": "eu-west-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 42, Platform: tc.platform, Type: tc.accountType, Credentials: tc.credentials, Priority: 1}
			counter := &candidateFailureTestCounter{counts: map[CandidateFailureScope]int64{}}
			path := &candidateExecutionPath{account: account, model: tc.model, ctx: context.Background()}
			state := &CandidateRequest{model: "different-public-alias", current: path, resolver: &UniversalRoutingResolver{
				candidateGateway: &GatewayService{rateLimitService: &RateLimitService{openaiSaturationCounter: counter}},
			}}
			ctx := context.WithValue(context.Background(), candidateRequestContextKey{}, state)
			failure := &UpstreamFailoverError{StatusCode: 502}
			for range 3 {
				_, err := ExecuteSelectedProtocol(ctx, nil, &AccountSelectionResult{Account: account}, account, nil, nil, ProtocolExecutors{
					NonGoverned: func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
						return nil, failure
					},
				})
				require.ErrorIs(t, err, failure)
			}
			require.Equal(t, map[CandidateFailureScope]int64{{42, tc.upstream}: 3}, counter.counts)
			counts := map[int64]int64{42: 3}
			state.mergeFailureCounts(ctx, []*candidateExecutionPath{path}, counts)
			require.Equal(t, 1001, candidateEffectivePriority(account, counts), "existing feedback merges without adding penalties")
			counts = map[int64]int64{}
			state.mergeFailureCounts(ctx, []*candidateExecutionPath{path}, counts)
			require.Equal(t, int64(3), counts[42], "native candidates must read their own feedback")
		})
	}
}

func TestCandidateTransportFailureAttribution(t *testing.T) {
	ctx := context.WithValue(context.Background(), candidateRequestContextKey{}, &CandidateRequest{})
	rendered := errors.New("Upstream request failed")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		cause error
		want  bool
	}{
		{"upstream_deadline", ctx, context.DeadlineExceeded, true},
		{"dial", ctx, &url.Error{Op: "Post", URL: "https://secret.invalid", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}, true},
		{"truncated", ctx, io.ErrUnexpectedEOF, true},
		{"client_cancel", canceled, context.DeadlineExceeded, false},
		{"client_deadline", expired, context.DeadlineExceeded, false},
		{"cancel_error", ctx, context.Canceled, false},
		{"local_validation", ctx, errors.New("missing project_id"), false},
		{"non_candidate", context.Background(), context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := candidateTransportFailure(tc.ctx, rendered, tc.cause)
			require.Equal(t, rendered.Error(), err.Error(), "raw transport errors must not escape sanitization")
			require.Equal(t, tc.want, candidateFailureAttributable(err))
		})
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer upstream.Close()
	client := &http.Client{Timeout: 20 * time.Millisecond}
	_, err := client.Get(upstream.URL)
	require.Error(t, err)
	require.True(t, candidateFailureAttributable(candidateTransportFailure(ctx, rendered, err)))
}

func TestCandidateTransportForwardersPreserveAttribution(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), candidateRequestContextKey{}, &CandidateRequest{})
			upstream := &anthropicHTTPUpstreamRecorder{err: &url.Error{Op: "Post", URL: "https://upstream.invalid", Err: context.DeadlineExceeded}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			var err error
			if platform == PlatformAnthropic {
				svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				_, err = svc.forwardAnthropicAPIKeyPassthrough(ctx, c, newAnthropicAPIKeyAccountForTest(), []byte(`{"model":"claude-sonnet-4-6"}`), "claude-sonnet-4-6", "claude-sonnet-4-6", false, time.Now())
			} else {
				svc := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
				account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
				body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
				c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
				_, err = svc.ForwardNative(ctx, c, account, "gemini-3.8-flash", "generateContent", false, body)
			}
			require.Error(t, err)
			if platform == PlatformAnthropic {
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, http.StatusBadGateway, failover.StatusCode)
				require.False(t, c.Writer.Written(), "failover must leave the response available for the next account")
			} else {
				require.Equal(t, http.StatusBadGateway, recorder.Code)
			}
			require.True(t, candidateFailureAttributable(err), "terminal forwarder must retain transport attribution")
		})
	}
}

func TestCandidateFailureAttributionDoesNotPenalizeCallerOrDuplicateCooldown(t *testing.T) {
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded,
		&UpstreamFailoverError{StatusCode: 400},
		&UpstreamFailoverError{StatusCode: 429},
		&UpstreamFailoverError{StatusCode: 529},
		&UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeRequest},
		&UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeProvider},
		&UpstreamFailoverError{StatusCode: 503, RequestScopedTransient: true},
		&UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true},
		&UpstreamFailoverError{StatusCode: 503, Stage: GatewayFailureStageAccountAuth},
	} {
		require.False(t, candidateFailureAttributable(err), "%v", err)
	}
	require.True(t, candidateFailureAttributable(&UpstreamFailoverError{StatusCode: 504, Scope: GatewayFailureScopeAccount}))
}
