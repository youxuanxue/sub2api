//go:build unit

package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUniversalAliasUsesSameModelForPlanAndHandler(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, compressed := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/plain", true: "/gzip"}[compressed], func(t *testing.T) {
				const body = `{"model":"opus","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
				c, _ := newTestCtx(http.MethodPost, path, body)
				if compressed {
					var buf bytes.Buffer
					w := gzip.NewWriter(&buf)
					_, err := w.Write([]byte(body))
					require.NoError(t, err)
					require.NoError(t, w.Close())
					c.Request.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
					c.Request.ContentLength = int64(buf.Len())
					c.Request.Header.Set("Content-Encoding", "gzip")
				}
				key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
				resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(1, service.PlatformAnthropic)}})
				router := service.NewProtocolRouter()
				account := &service.Account{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
					Credentials: map[string]any{"base_url": "https://example.com", "api_key": "test-only", "model_mapping": map[string]any{"claude-opus-5": "claude-opus-5"}}}
				identity, governed, err := service.BuildProtocolEndpointIdentity(account)
				require.NoError(t, err)
				require.True(t, governed)
				account.ProtocolEndpointCapabilityID = &account.ID
				account.ProtocolEndpointCapability = &service.ProtocolEndpointCapability{ID: account.ID, CapabilityKey: identity.Key(), Identity: identity,
					SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, Revision: 1,
					ProbeEvidence: service.ProtocolProbeEvidence{InitialProbeCompleted: true}}
				evaluated := false
				resolver.SetCandidateEvaluator(router, func(ctx context.Context, _ service.Group, model string, shape service.UniversalShape) (service.GroupCandidateEligibility, error) {
					evaluated = true
					require.Nil(t, key.GroupID)
					require.Equal(t, "claude-opus-5", model)
					if shape == service.ShapeAnthropicMessages {
						request, ok := service.ProtocolRoutingRequest(ctx)
						require.True(t, ok)
						require.Equal(t, model, request.RequestedModel())
						require.Equal(t, model, gjson.GetBytes(request.Body(), "model").String())
						snapshot, err := service.ProtocolAccountSnapshot(account, model)
						require.NoError(t, err)
						plan, err := router.Plan(request, snapshot)
						require.NoError(t, err)
						require.Equal(t, model, plan.ResolvedModel())
					}
					return service.GroupCandidateEligibility{Supported: true, Available: true}, nil
				})
				require.False(t, MaybeResolveUniversal(c, key, resolver))
				require.True(t, evaluated)
				require.Equal(t, int64(1), *key.GroupID)
				handlerBody, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
				require.NoError(t, err)
				require.Equal(t, "claude-opus-5", gjson.GetBytes(handlerBody, "model").String())
				require.Equal(t, "hello", gjson.GetBytes(handlerBody, "messages.0.content").String())
				require.Equal(t, int64(len(handlerBody)), c.Request.ContentLength)
			})
		}
	}
}

func TestUniversalInternalErrorKeepsServerOwnership(t *testing.T) {
	c, recorder := newTestCtx(http.MethodPost, "/v1/messages", `{"model":"opus","messages":[{"role":"user","content":"hello"}]}`)
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{err: errors.New("capability unavailable")})
	key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
	require.True(t, MaybeResolveUniversal(c, key, resolver))
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, "api_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "universal_routing_internal_error", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Nil(t, key.GroupID)
}

func TestUniversalAliasPreservesUnmatchedAndForcedRequests(t *testing.T) {
	for _, tc := range []struct{ name, path, model, force string }{
		{"canonical", "/v1/messages", "claude-opus-5", ""},
		{"unknown", "/v1/messages", "unknown-family", ""},
		{"other_protocol", "/v1/chat/completions", "opus", ""},
		{"forced_platform", "/v1/messages", "opus", service.PlatformAntigravity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"` + tc.model + `","messages":[{"role":"user","content":"hello"}]}`
			c, _ := newTestCtx(http.MethodPost, tc.path, body)
			platform := service.PlatformAnthropic
			if tc.force != "" {
				platform = tc.force
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ForcePlatform, tc.force))
			}
			resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(1, platform)}})
			resolver.SetCandidateEvaluator(service.NewProtocolRouter(), func(_ context.Context, _ service.Group, model string, _ service.UniversalShape) (service.GroupCandidateEligibility, error) {
				require.Equal(t, tc.model, model)
				return service.GroupCandidateEligibility{Supported: true, Available: true}, nil
			})
			key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
			require.False(t, MaybeResolveUniversal(c, key, resolver))
			restored, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(restored))
		})
	}
}
