//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCandidateEdgeModelRejectionReselectsAuthorizedOrigin(t *testing.T) {
	for _, direct := range []bool{false, true} {
		groups := []Group{grp(21, PlatformAntigravity, 1, false), grp(1, PlatformAnthropic, 1, false)}
		relay := globalCandidateAccount(62, 1, 21)
		relay.Platform = PlatformAntigravity
		relay.Credentials["base_url"] = "https://api-us4.tokenkey.dev"
		relay.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}
		attachTestProtocolCapability(&relay, protocolrouter.ProtocolMessages)
		peer := globalCandidateAccount(69, 2, 1)
		peer.Platform = PlatformAnthropic
		peer.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}
		attachTestProtocolCapability(&peer, protocolrouter.ProtocolMessages)
		if direct {
			groups = groups[:1]
			peer.GroupIDs = []int64{21}
		}
		r, _, key := globalCandidateFixture(groups, []Account{relay, peer})
		if direct {
			key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &groups[0], &groups[0].ID
		}
		body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":32000,"thinking":{"type":"disabled"},"stream":true,"system":[{"type":"text","text":"retain cache","cache_control":{"type":"ephemeral"}}],"tools":[],"messages":[{"role":"user","content":"hello"}]}`)
		ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-sonnet-4-6", body, "sticky", "")
		require.NoError(t, err)
		require.Equal(t, int64(62), state.current.account.ID)
		rejection := candidateEdgeModelRejection(ctx, state.current.account, 400, nil, []byte(`{"error":{"type":"invalid_request_error","message":"Unsupported model: claude-sonnet-4-6"}}`), state.current.model)
		require.NotNil(t, rejection)
		require.True(t, rejection.ShouldRetryNextAccount())
		excluded := map[int64]struct{}{62: {}}
		selected, err := state.selectAccount(ctx, candidateSelectOptions{excluded: excluded, acquire: true})
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		require.Equal(t, int64(69), selected.Account.ID)
		require.Equal(t, peer.GroupIDs[0], *key.GroupID, "billing follows the successful authorized origin")
		require.Equal(t, body, state.body, "retry must retain thinking, system/cache, tools and output limit")
		excluded[69] = struct{}{}
		next, err := state.selectAccount(ctx, candidateSelectOptions{excluded: excluded})
		require.Error(t, err)
		require.Nil(t, next, "exhaustion cannot escape authorized groups or recycle rejected paths")
	}
}

func TestCandidateEdgeModelRejectionMappedPlan(t *testing.T) {
	const clientModel = "claude-opus-4-6"
	const wireModel = "claude-opus-4-6-thinking"
	for _, direct := range []bool{false, true} {
		groups := []Group{grp(21, PlatformAntigravity, 1, false)}
		relay := globalCandidateAccount(62, 1, 21)
		relay.Platform = PlatformAntigravity
		relay.Credentials["base_url"] = "https://api-us4.tokenkey.dev"
		relay.Credentials["model_mapping"] = map[string]any{clientModel: wireModel}
		attachTestProtocolCapability(&relay, protocolrouter.ProtocolMessages)
		peer := globalCandidateAccount(69, 2, 21)
		peer.Platform = PlatformAntigravity
		peer.Credentials["base_url"] = "https://api-us3.tokenkey.dev"
		peer.Credentials["model_mapping"] = map[string]any{clientModel: clientModel}
		attachTestProtocolCapability(&peer, protocolrouter.ProtocolMessages)
		router, _, key := globalCandidateFixture(groups, []Account{relay, peer})
		if direct {
			key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &groups[0], &groups[0].ID
		}
		body := []byte(`{"model":"claude-opus-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
		ctx, state, err := router.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", clientModel, body, "", "")
		require.NoError(t, err)
		require.Equal(t, int64(62), state.current.account.ID)
		require.Equal(t, clientModel, state.current.model)
		require.NotNil(t, state.current.plan)
		require.Equal(t, wireModel, state.current.plan.ResolvedModel())
		// The selected immutable plan, not later account mapping edits, owns the wire identity.
		state.current.account.Credentials["model_mapping"] = map[string]any{clientModel: "claude-unrelated"}

		payload := `{"error":{"type":"invalid_request_error","message":"Unsupported model: ` + wireModel + `"}}`
		for _, compat := range []bool{false, true} {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			response := &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
			var failure *UpstreamFailoverError
			if compat {
				failure = (&OpenAIGatewayService{}).failoverNativeMessagesUpstreamHTTPError(ctx, c, state.current.account, response, []byte(payload), TkUnsupportedModelMessage(wireModel), wireModel)
			} else {
				_, err := (&GatewayService{}).handleErrorResponse(ctx, response, c, state.current.account, wireModel)
				require.ErrorAs(t, err, &failure)
			}
			require.NotNil(t, failure)
			require.True(t, failure.ShouldRetryNextAccount())
			require.False(t, c.Writer.Written())
			require.Empty(t, recorder.Body.String())
		}
		for _, model := range []string{clientModel, "claude-unrelated"} {
			payload := []byte(`{"error":{"type":"invalid_request_error","message":"Unsupported model: ` + model + `"}}`)
			require.Nil(t, candidateEdgeModelRejection(ctx, state.current.account, 400, nil, payload, model))
		}
		selected, err := state.selectAccount(ctx, candidateSelectOptions{excluded: map[int64]struct{}{62: {}}, acquire: true})
		require.NoError(t, err)
		require.Equal(t, int64(69), selected.Account.ID)
		selected.ReleaseFunc()
		require.Equal(t, body, state.body)
	}
}

func TestCandidateEdgeModelRejectionBoundaries(t *testing.T) {
	const model = "claude-sonnet-4-6"
	const body = `{"error":{"type":"invalid_request_error","message":"Unsupported model: claude-sonnet-4-6"}}`
	for _, tc := range []struct {
		name   string
		mutate func(*Account, *CandidateRequest, *int, *string, *string)
		want   bool
	}{
		{"admitted_edge", nil, true},
		{"different_account", func(a *Account, r *CandidateRequest, _ *int, _ *string, _ *string) {
			r.current.account = &Account{ID: a.ID + 1}
		}, false},
		{"not_selected", func(_ *Account, r *CandidateRequest, _ *int, _ *string, _ *string) { r.current = nil }, false},
		{"non_edge", func(a *Account, _ *CandidateRequest, _ *int, _ *string, _ *string) {
			a.Credentials["base_url"] = "https://other.example.test"
		}, false},
		{"oauth", func(a *Account, _ *CandidateRequest, _ *int, _ *string, _ *string) { a.Type = AccountTypeOAuth }, false},
		{"other_status", func(_ *Account, _ *CandidateRequest, s *int, _ *string, _ *string) { *s = 403 }, false},
		{"other_model", func(_ *Account, _ *CandidateRequest, _ *int, _ *string, m *string) { *m = "claude-opus-4-6" }, false},
		{"schema_error", func(_ *Account, _ *CandidateRequest, _ *int, b *string, _ *string) {
			*b = `{"error":{"type":"invalid_request_error","message":"invalid tools schema"}}`
		}, false},
		{"wrong_type", func(_ *Account, _ *CandidateRequest, _ *int, b *string, _ *string) {
			*b = strings.ReplaceAll(*b, "invalid_request_error", "authentication_error")
		}, false},
		{"invalid_json", func(_ *Account, _ *CandidateRequest, _ *int, b *string, _ *string) { *b += "truncated" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 62, Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api-us4.tokenkey.dev"}}
			r := &CandidateRequest{current: &candidateExecutionPath{account: a, model: model}}
			status, payload, requested := 400, body, model
			if tc.mutate != nil {
				tc.mutate(a, r, &status, &payload, &requested)
			}
			ctx := context.WithValue(context.Background(), candidateRequestContextKey{}, r)
			err := candidateEdgeModelRejection(ctx, a, status, http.Header{"X-Request-Id": []string{"edge-request"}}, []byte(payload), requested)
			if !tc.want {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			require.True(t, err.ShouldRetryNextAccount())
			require.False(t, err.RetryableOnSameAccount)
			require.True(t, err.RequestScopedTransient)
			require.False(t, candidateFailureAttributable(err), "path rejection cannot penalize the account's other models")
			require.Equal(t, http.StatusBadGateway, err.ClientStatusCode)
			require.Equal(t, "edge-request", err.ResponseHeaders.Get("X-Request-Id"))
			require.Nil(t, candidateEdgeModelRejection(context.Background(), a, status, nil, []byte(payload), requested))
		})
	}
}

func TestCandidateEdgeModelRejectionBeforeResponseCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, compat := range []bool{false, true} {
		account := &Account{ID: 62, Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api-us4.tokenkey.dev"}}
		state := &CandidateRequest{current: &candidateExecutionPath{account: account, model: "claude-sonnet-4-6"}}
		ctx := context.WithValue(context.Background(), candidateRequestContextKey{}, state)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
		body := `{"error":{"type":"invalid_request_error","message":"Unsupported model: claude-sonnet-4-6"}}`
		response := &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
		var failure *UpstreamFailoverError
		if compat {
			failure = (&OpenAIGatewayService{}).failoverNativeMessagesUpstreamHTTPError(ctx, c, account, response, []byte(body), TkUnsupportedModelMessage(state.current.model), state.current.model)
		} else {
			_, err := (&GatewayService{}).handleErrorResponse(ctx, response, c, account, state.current.model)
			require.ErrorAs(t, err, &failure)
		}
		require.NotNil(t, failure)
		require.True(t, failure.ShouldRetryNextAccount())
		require.False(t, c.Writer.Written(), "another account must be able to write a successful response")
		require.Empty(t, recorder.Body.String())
	}
}
