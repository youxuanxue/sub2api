package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestProbeOpenAIAPIKeyChatCompletionsSupportPersistsPositiveCapability(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          97,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example/v1",
		},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}

	svc.ProbeOpenAIAPIKeyChatCompletionsSupport(context.Background(), account.ID)

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://compat-upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	updates := <-updateCalls
	require.Equal(t, []string{"chat_completions"}, updates[SupportedProtocolsExtraKey])
}

func TestProbeOpenAIAPIKeyChatCompletionsSupportRemovesEndpointNegativeCapability(t *testing.T) {
	updateCalls := make(chan map[string]any, 1)
	account := Account{
		ID:          98,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat-upstream.example/v1",
		},
		Extra: map[string]any{
			SupportedProtocolsExtraKey: []string{
				string(protocolrouter.ProtocolMessages),
				string(protocolrouter.ProtocolChatCompletions),
			},
		},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      updateCalls,
	}
	svc := &AccountTestService{
		accountRepo: repo,
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"not found"}}`)),
		}},
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}

	svc.ProbeOpenAIAPIKeyChatCompletionsSupport(context.Background(), account.ID)

	updates := <-updateCalls
	require.Equal(t, []string{"messages"}, updates[SupportedProtocolsExtraKey])
}

func TestProbeOpenAIAPIKeyChatCompletionsSupportRequiresExplicitBaseURL(t *testing.T) {
	account := Account{
		ID:          99,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
	}
	repo := &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
		updateExtraCalls:      make(chan map[string]any, 1),
	}
	upstream := &httpUpstreamRecorder{}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}

	svc.ProbeOpenAIAPIKeyChatCompletionsSupport(context.Background(), account.ID)

	require.Nil(t, upstream.lastReq)
	select {
	case updates := <-repo.updateExtraCalls:
		t.Fatalf("missing explicit base URL persisted capability update: %v", updates)
	default:
	}
}
