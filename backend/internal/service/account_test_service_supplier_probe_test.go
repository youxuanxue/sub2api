package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestUS048_SupplierProbeUsesInMemoryAccountWithoutRepositoryLookup(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		hits++
		var payload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.Equal(t, "vendor-model", payload["model"])
		require.Equal(t, "Bearer secret", request.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	repo := &supplierProbeAccountRepoFake{}
	svc := &AccountTestService{accountRepo: repo}
	result := svc.ProbeSupplierModel(context.Background(), SupplierProbeInput{
		Account: &Account{
			Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1, Concurrency: 1,
			Credentials: supplierManagedCredentials(
				server.URL, "secret", map[string]string{"client-model": "vendor-model"},
			),
		},
		ClientModelID: "client-model", UpstreamModelID: "vendor-model",
	})

	require.Equal(t, SupplierProbeStatusPassed, result.Status)
	require.Equal(t, "client-model", result.ClientModelID)
	require.Equal(t, "vendor-model", result.UpstreamModelID)
	require.Equal(t, "openai_chat_completions", result.Protocol)
	require.Equal(t, 1, hits)
	require.Zero(t, repo.getCalls, "supplier probe must not require a persisted account")
}

func TestUS048_FMGoSeedanceIsProtocolUnsupportedWithoutAccountWrite(t *testing.T) {
	repo := &supplierProbeAccountRepoFake{}
	svc := &AccountTestService{accountRepo: repo}
	result := svc.ProbeSupplierModel(context.Background(), SupplierProbeInput{
		Account:         &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1},
		ClientModelID:   "doubao-seedance-2-0-260128",
		UpstreamModelID: "feimiao-seedance-2-0-260128",
	})

	require.Equal(t, SupplierProbeStatusProtocolUnsupported, result.Status)
	require.Equal(t, "supplier protocol unsupported", result.Detail)
	require.Zero(t, repo.getCalls)
}

func TestUS048_SupplierManagedAccountDeclaresOnlyChatProtocol(t *testing.T) {
	account := &Account{
		ID:          41,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"client-model": "vendor-model"},
		),
		Extra: map[string]any{
			SupplierSourceIDExtraKey:     int64(7),
			SupplierDiscountBandExtraKey: 3,
		},
	}

	identity, governed, err := BuildProtocolEndpointIdentity(account)

	require.NoError(t, err)
	require.True(t, governed)
	require.Equal(t, map[protocolrouter.Protocol]ProtocolEndpoint{
		protocolrouter.ProtocolChatCompletions: {URL: "https://supplier.example/v1/chat/completions"},
	}, identity.ProtocolEndpoints)
	require.Equal(t, []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}, ProtocolProbeCandidates(account))
}

func TestUS048_SupplierManagedOpenAIEndpointDoesNotDuplicateV1(t *testing.T) {
	account := &Account{
		ID:          41,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"client-model": "vendor-model"},
		),
		Extra: map[string]any{
			SupplierSourceIDExtraKey:     int64(7),
			SupplierDiscountBandExtraKey: 3,
		},
	}

	endpoint, err := protocolExactEndpoint(account, protocolrouter.ProtocolChatCompletions, "vendor-model")

	require.NoError(t, err)
	require.Equal(t, "https://supplier.example/v1/chat/completions", endpoint)
}

func TestUS048_SupplierProbeDoesNotLogRawUpstreamError(t *testing.T) {
	const upstreamSecret = "supplier-upstream-secret-canary-6f05d18b"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"credential %s rejected"}}`, upstreamSecret)
	}))
	defer server.Close()

	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	svc := &AccountTestService{accountRepo: &supplierProbeAccountRepoFake{}}
	result := svc.ProbeSupplierModel(context.Background(), SupplierProbeInput{
		Account: &Account{
			Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1, Concurrency: 1,
			Credentials: supplierManagedCredentials(
				server.URL, "secret", map[string]string{"client-model": "vendor-model"},
			),
		},
		ClientModelID: "client-model", UpstreamModelID: "vendor-model",
	})

	require.NotEqual(t, SupplierProbeStatusPassed, result.Status)
	require.NotContains(t, logs.String(), upstreamSecret)
}

func TestUS048_SupplierProbeDoesNotInferProtocolFromModelPrefix(t *testing.T) {
	result := (*AccountTestService)(nil).ProbeSupplierModel(
		context.Background(),
		SupplierProbeInput{
			Account: &Account{}, ClientModelID: "client-model", UpstreamModelID: "vendor-model",
		},
	)

	require.Equal(t, SupplierProbeStatusFailed, result.Status)
	require.Equal(t, "account test service unavailable", result.Detail)
}

type supplierProbeAccountRepoFake struct {
	AccountRepository
	getCalls int
}

func (r *supplierProbeAccountRepoFake) GetByID(context.Context, int64) (*Account, error) {
	r.getCalls++
	return nil, errors.New("supplier probe must not load an account row")
}

func TestUS048_SupplierProbeClassifiesEventsWithoutPersistingUpstreamDetail(t *testing.T) {
	t.Run("authentication failure", func(t *testing.T) {
		const secret = "arbitrary-vendor-secret-6f05d18b"
		result := supplierProbeResultFromSSE(
			"data: {\"type\":\"error\",\"error\":\"API returned 401: credential "+secret+"\"}\n\n",
			errors.New("API returned 401: credential "+secret),
		)

		require.Equal(t, SupplierProbeStatusAuthFailed, result.Status)
		require.Equal(t, "upstream authentication failed", result.Detail)
		require.NotContains(t, result.Detail, secret)
	})

	t.Run("private task protocol", func(t *testing.T) {
		result := supplierProbeResultFromSSE(
			"data: {\"type\":\"error\",\"error\":\"task protocol endpoint returned 404\"}\n\n",
			errors.New("task protocol endpoint returned 404"),
		)

		require.Equal(t, SupplierProbeStatusProtocolUnsupported, result.Status)
		require.Equal(t, "supplier protocol unsupported", result.Detail)
	})

	t.Run("completion without explicit success", func(t *testing.T) {
		result := supplierProbeResultFromSSE("data: {\"type\":\"test_complete\"}\n\n", nil)

		require.Equal(t, SupplierProbeStatusFailed, result.Status)
		require.Equal(t, "upstream probe failed", result.Detail)
	})

	t.Run("explicit successful completion", func(t *testing.T) {
		result := supplierProbeResultFromSSE(
			"data: {\"type\":\"content\",\"data\":{\"object\":\"chat.completion\"}}\n\n"+
				"data: {\"type\":\"test_complete\",\"success\":true}\n\n",
			nil,
		)

		require.Equal(t, SupplierProbeStatusPassed, result.Status)
		require.Equal(t, "openai_chat_completions", result.Protocol)
		require.Empty(t, result.Detail)
	})

	t.Run("successful non-chat protocol remains unsupported", func(t *testing.T) {
		result := supplierProbeResultFromSSE(
			"data: {\"type\":\"content\",\"data\":{\"type\":\"response.completed\"}}\n\n"+
				"data: {\"type\":\"test_complete\",\"success\":true}\n\n",
			nil,
		)

		require.Equal(t, SupplierProbeStatusProtocolUnsupported, result.Status)
		require.Equal(t, "openai_responses", result.Protocol)
		require.Equal(t, "supplier protocol unsupported", result.Detail)
	})
}
