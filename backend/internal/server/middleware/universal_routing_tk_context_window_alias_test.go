//go:build unit

package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
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

// Universal-key ingress mirrors the direct-key handler throat: the Claude Code
// "[1m]" context-window alias must be gone before candidate evaluation, or the
// per-group / per-account support predicate judges a bracketed id that no
// catalog contains (claude-code#60913). Planning and the downstream handler must
// both read the stripped body, including when the request arrived gzipped.
func TestUniversalContextWindowAliasStrippedBeforeCandidateEvaluation(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, compressed := range []bool{false, true} {
			name := path + map[bool]string{false: "/plain", true: "/gzip"}[compressed]
			t.Run(name, func(t *testing.T) {
				const body = `{"model":"claude-opus-5[1m]","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
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
				account := &service.Account{
					ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
					Credentials: map[string]any{
						"base_url": "https://example.com", "api_key": "test-only",
						"model_mapping": map[string]any{"claude-opus-5": "claude-opus-5"},
					},
				}
				identity, governed, err := service.BuildProtocolEndpointIdentity(account)
				require.NoError(t, err)
				require.True(t, governed)
				account.ProtocolEndpointCapabilityID = &account.ID
				account.ProtocolEndpointCapability = &service.ProtocolEndpointCapability{
					ID: account.ID, CapabilityKey: identity.Key(), Identity: identity,
					SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, Revision: 1,
					ProbeEvidence: service.ProtocolProbeEvidence{InitialProbeCompleted: true},
				}

				evaluated := false
				resolver.SetCandidateEvaluator(router, func(ctx context.Context, _ service.Group, model string, shape service.UniversalShape) (service.GroupCandidateEligibility, error) {
					evaluated = true
					// The defect: without the ingress strip this was "claude-opus-5[1m]",
					// which the account's model_mapping does not claim.
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

				// The handler downstream reads the stripped body, with the rest intact.
				handlerBody, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
				require.NoError(t, err)
				require.Equal(t, "claude-opus-5", gjson.GetBytes(handlerBody, "model").String())
				require.Equal(t, "hello", gjson.GetBytes(handlerBody, "messages.0.content").String())
				require.Equal(t, int64(16), gjson.GetBytes(handlerBody, "max_tokens").Int())
				require.Equal(t, int64(len(handlerBody)), c.Request.ContentLength)
			})
		}
	}
}

// The alias strip and the bare-family alias compose: "opus[1m]" must reach
// candidate evaluation as the servable full id, not as a half-resolved name.
func TestUniversalContextWindowAliasComposesWithBareFamilyAlias(t *testing.T) {
	c, _ := newTestCtx(http.MethodPost, "/v1/messages",
		`{"model":"opus[1m]","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)

	key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(1, service.PlatformAnthropic)}})
	evaluated := false
	resolver.SetCandidateEvaluator(service.NewProtocolRouter(), func(_ context.Context, _ service.Group, model string, _ service.UniversalShape) (service.GroupCandidateEligibility, error) {
		evaluated = true
		require.Equal(t, "claude-opus-5", model) // real servable-table pin
		return service.GroupCandidateEligibility{Supported: true, Available: true}, nil
	})

	require.False(t, MaybeResolveUniversal(c, key, resolver))
	require.True(t, evaluated)

	handlerBody, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-5", gjson.GetBytes(handlerBody, "model").String())
}

// No collateral damage: requests the strip must not touch keep a byte-identical
// body. A forced non-Anthropic platform is out of scope (the OpenAI-compat shape
// owns its own strip), and a non-Anthropic protocol path is never parsed here.
func TestUniversalContextWindowAliasPreservesOutOfScopeRequests(t *testing.T) {
	for _, tc := range []struct{ name, path, model, force string }{
		{"bare id untouched", "/v1/messages", "claude-opus-5", ""},
		{"chat completions path", "/v1/chat/completions", "claude-opus-5[1m]", ""},
		{"forced antigravity platform", "/v1/messages", "claude-opus-5[1m]", service.PlatformAntigravity},
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
				require.Equal(t, tc.model, model, "out-of-scope request must reach evaluation unmodified")
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

// A forced OpenAI platform on the Anthropic-shaped path is gated out of the
// strip. It never reaches candidate evaluation anyway (universal plan policy
// rejects it with 403 for the bare id too), so the only assertable contract is
// that the suffix is still present in the rejection — proof the Anthropic-only
// gate held rather than silently widening to OpenAI-compat.
func TestUniversalContextWindowAliasSkipsForcedOpenAIPlatform(t *testing.T) {
	c, rec := newTestCtx(http.MethodPost, "/v1/messages",
		`{"model":"gpt-5.5[1m]","messages":[{"role":"user","content":"hello"}]}`)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ForcePlatform, service.PlatformOpenAI))

	resolver := service.NewUniversalRoutingResolver(&stubSpanLister{groups: []service.Group{activeGroup(1, service.PlatformOpenAI)}})
	resolver.SetCandidateEvaluator(service.NewProtocolRouter(), func(_ context.Context, _ service.Group, _ string, _ service.UniversalShape) (service.GroupCandidateEligibility, error) {
		return service.GroupCandidateEligibility{Supported: true, Available: true}, nil
	})
	key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}

	require.True(t, MaybeResolveUniversal(c, key, resolver))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "gpt-5.5[1m]")
}
