//go:build unit

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMaybeResolveUniversal_CancellationOwnership(t *testing.T) {
	for _, candidateScheduling := range []bool{false, true} {
		for _, endpoint := range []struct{ path, body string }{
			{"/v1/chat/completions", `{"model":"gpt-5"}`},
			{"/v1/responses", `{"model":"gpt-5","input":"hello"}`},
			{"/v1/messages", `{"model":"claude-opus-5"}`},
			{"/v1/messages/count_tokens", `{"model":"claude-opus-5"}`},
			{"/v1beta/models/gemini-3.8-flash:generateContent", `{"contents":[]}`},
		} {
			for _, tc := range []struct {
				name       string
				err        error
				requestErr error
				wantClosed bool
			}{
				{"canceled", context.Canceled, nil, true},
				{"wrapped_canceled", fmt.Errorf("load candidates: %w", context.Canceled), nil, true},
				{"postgres_canceled", errors.New("pq: canceling statement due to user request"), nil, true},
				{"request_canceled", errors.New("query interrupted"), context.Canceled, true},
				{"deadline", context.DeadlineExceeded, nil, false},
				{"deadline_over_canceled_request", context.DeadlineExceeded, context.Canceled, false},
				{"postgres_after_deadline", errors.New("pq: canceling statement due to user request"), context.DeadlineExceeded, false},
				{"database_failure", errors.New("database unavailable"), nil, false},
			} {
				t.Run(fmt.Sprintf("candidate=%t/%s/%s", candidateScheduling, endpoint.path, tc.name), func(t *testing.T) {
					logs, restore := captureMiddlewareStructuredLog(t)
					defer restore()
					c, w := newTestCtx(http.MethodPost, endpoint.path, endpoint.body)
					if tc.requestErr != nil {
						ctx, cancel := context.WithCancel(c.Request.Context())
						if errors.Is(tc.requestErr, context.DeadlineExceeded) {
							cancel()
							ctx, cancel = context.WithDeadline(c.Request.Context(), time.Now().Add(-time.Second))
						}
						cancel()
						c.Request = c.Request.WithContext(ctx)
					}
					resolver := service.NewUniversalRoutingResolver(&stubSpanLister{err: tc.err})
					if candidateScheduling {
						users := &stubUserRepo{getByID: func(context.Context, int64) (*service.User, error) { return nil, tc.err }}
						api := service.NewAPIKeyService(nil, users, nil, nil, nil, nil, &config.Config{})
						service.ProvideTKUniversalModelsProvider(api, &service.GatewayService{}, nil, &service.OpenAIGatewayService{}, service.NewProtocolRouter())
						resolver = api.UniversalResolver()
						require.True(t, resolver.CandidateSchedulingEnabled())
					}
					key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
					require.True(t, MaybeResolveUniversal(c, key, resolver))
					require.True(t, c.IsAborted())
					require.Nil(t, key.GroupID, "no billing group may be bound after preparation fails")
					require.Equal(t, tc.wantClosed, service.HasOpsClientClosedRequest(c))
					require.Equal(t, !tc.wantClosed, c.GetBool(service.OpsRoutingInternalErrorKey))
					require.Equal(t, !tc.wantClosed, logs.ContainsMessageAtLevel("universal_routing.resolve_failed", "error"))
					if tc.wantClosed {
						require.Equal(t, StatusClientClosedRequest, w.Code)
						require.Equal(t, "context canceled", gjson.Get(w.Body.String(), "error.message").String())
						switch endpoint.path {
						case "/v1/messages", "/v1/messages/count_tokens":
							require.Equal(t, "error", gjson.Get(w.Body.String(), "type").String())
							require.Equal(t, "invalid_request_error", gjson.Get(w.Body.String(), "error.type").String())
						case "/v1beta/models/gemini-3.8-flash:generateContent":
							require.Equal(t, int64(StatusClientClosedRequest), gjson.Get(w.Body.String(), "error.code").Int())
						default:
							require.Equal(t, "invalid_request_error", gjson.Get(w.Body.String(), "error.type").String())
						}
					} else {
						require.Equal(t, http.StatusInternalServerError, w.Code)
					}
				})
			}
		}
	}
}
