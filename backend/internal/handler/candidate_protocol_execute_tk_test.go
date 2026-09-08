//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type candidateNativeHTTPUpstream struct{ baseURL string }

func (u candidateNativeHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	target, err := url.Parse(u.baseURL)
	if err != nil {
		return nil, err
	}
	request := req.Clone(req.Context())
	request.URL.Scheme, request.URL.Host, request.Host = target.Scheme, target.Host, target.Host
	return http.DefaultClient.Do(request)
}

func (u candidateNativeHTTPUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

type candidateNativeRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *candidateNativeRepo) ListCandidateAccounts(context.Context, []int64) ([]service.Account, error) {
	return append([]service.Account(nil), r.accounts...), nil
}

func (r *candidateNativeRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.accounts {
		if account.ID == id {
			return &account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func TestUS050_CandidateNativeRetryUsesAccountTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []struct {
		protocol    protocolrouter.Protocol
		accountType string
	}{
		{protocolrouter.ProtocolMessages, service.AccountTypeAPIKey},
		{protocolrouter.ProtocolChatCompletions, service.AccountTypeAPIKey},
		{protocolrouter.ProtocolResponses, service.AccountTypeAPIKey},
		{protocolrouter.ProtocolMessages, service.AccountTypeOAuth},
		{protocolrouter.ProtocolChatCompletions, service.AccountTypeOAuth},
		{protocolrouter.ProtocolResponses, service.AccountTypeOAuth},
	} {
		protocol := scenario.protocol
		t.Run(string(protocol)+"/"+scenario.accountType, func(t *testing.T) {
			var received []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				received, err = io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Equal(t, "/v1/messages", r.URL.Path)
				if scenario.accountType == service.AccountTypeAPIKey {
					assert.Equal(t, "upstream-key", r.Header.Get("x-api-key"))
				} else {
					assert.Equal(t, "Bearer upstream-token", r.Header.Get("Authorization"))
				}
				if !gjson.GetBytes(received, "stream").Bool() {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg_native","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_native\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer upstream.Close()
			group := service.Group{ID: 10, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true, AllowMessagesDispatch: true}
			group.MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"claude-sonnet-4-6": "claude-sonnet-4-6"}
			account := service.Account{ID: 115, Platform: service.PlatformAnthropic, Type: scenario.accountType, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{10},
				Credentials: map[string]any{"api_key": "upstream-key", "access_token": "upstream-token", "base_url": upstream.URL, "model_mapping": map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}}}
			attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolMessages)
			initial := account
			initial.ID, initial.Platform, initial.Type, initial.Priority = 114, service.PlatformOpenAI, service.AccountTypeAPIKey, -1
			attachHandlerTestProtocolCapability(t, &initial, protocolrouter.ProtocolMessages)
			repo := &candidateNativeRepo{accounts: []service.Account{initial, account}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Security.URLAllowlist.AllowPrivateHosts = true
			transport := candidateNativeHTTPUpstream{baseURL: upstream.URL}
			gateway := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			openai := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil)
			api := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			router := service.NewProtocolRouter()
			service.ProvideTKUniversalModelsProvider(api, gateway, nil, openai, router)
			key := &service.APIKey{ID: 1, UserID: 7, Group: &group, GroupID: &group.ID, User: &service.User{ID: 7, Balance: 10}}
			shape, path := service.ShapeOpenAIChat, "/v1/chat/completions"
			body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
			if protocol == protocolrouter.ProtocolMessages {
				shape, path = service.ShapeAnthropicMessages, "/v1/messages"
			} else if protocol == protocolrouter.ProtocolResponses {
				path = "/v1/responses"
				body = []byte(`{"model":"claude-sonnet-4-6","input":"hello","max_output_tokens":32}`)
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
			c.Set(string(middleware.ContextKeyAPIKey), key)
			_, err := api.UniversalResolver().PrepareCandidateIngress(c, key, shape, path, "claude-sonnet-4-6", body, "")
			require.NoError(t, err)
			selection, err := gateway.SelectAccountWithLoadAwareness(c.Request.Context(), key.GroupID, "", "claude-sonnet-4-6", nil, "", key.UserID)
			require.NoError(t, err)
			require.Equal(t, initial.ID, selection.Account.ID)
			h := &OpenAIGatewayHandler{gatewayService: openai, nativeGatewayService: gateway, cfg: cfg}
			upstreamFailure := errors.New("first account unavailable")
			initialCalls := 0
			original := func(_ context.Context, selected *service.Account, _ protocolrouter.Plan, _ protocolrouter.CanonicalRequest) (any, error) {
				initialCalls++
				require.Equal(t, initial.ID, selected.ID, "native account reached the OpenAI credential transport")
				return nil, upstreamFailure
			}
			executors := h.candidateProtocolExecutors(c, service.ProtocolExecutors{NonGoverned: original, MessagesIdentity: original, ChatToMessages: original, ResponsesToMessages: original})
			_, err = service.ExecuteSelectedProtocol(c.Request.Context(), router, selection, selection.Account, gateway.ValidateProtocolEndpoint, gateway.LoadProtocolExecutionAccount, executors)
			require.ErrorIs(t, err, upstreamFailure)
			selection.ReleaseFunc()
			selection, err = gateway.SelectAccountWithLoadAwareness(c.Request.Context(), key.GroupID, "", "claude-sonnet-4-6", map[int64]struct{}{initial.ID: {}}, "", key.UserID)
			require.NoError(t, err)
			require.Equal(t, account.ID, selection.Account.ID)
			defer selection.ReleaseFunc()
			result, err := service.ExecuteSelectedProtocol(c.Request.Context(), router, selection, selection.Account, gateway.ValidateProtocolEndpoint, gateway.LoadProtocolExecutionAccount, executors)
			require.NoError(t, err, recorder.Body.String())
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "OK")
			require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(received, "model").String())
			require.Equal(t, int64(10), *key.GroupID)
			require.Equal(t, 1, initialCalls)
		})
	}
}
