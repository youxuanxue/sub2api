//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func webCandidateAccount(relay bool) Account {
	a := Account{ID: 200, Platform: PlatformGemini, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{740},
		Credentials: map[string]any{"api_key": "test-only", "base_url": "https://example.invalid",
			"model_mapping": map[string]any{"gemini-3-flash": "gemini-3.8-flash", "nano-2": "nano-2"}}}
	if relay {
		a.Credentials[GeminiWebRelayCredentialKey] = true
	} else {
		a.Credentials["gemini_web"] = map[string]any{}
	}
	return a
}

func TestGeminiWebCandidateRejectsProductionChatWithoutPoisoningPeer(t *testing.T) {
	for _, relay := range []bool{false, true} {
		web := webCandidateAccount(relay)
		peer := *protocolRoutingOpenAIAccount(62, "chat_completions")
		peer.GroupIDs = []int64{16}
		peer.Credentials["model_mapping"] = map[string]any{"gemini-3-flash": "gemini-3.8-flash"}
		groups := []Group{grp(740, PlatformGemini, 1, false), grp(16, PlatformOpenAI, 2, false)}
		r, _, key := globalCandidateFixture(groups, []Account{web, peer})
		body := []byte(`{"model":"gemini-3-flash","messages":[{"role":"system","content":"Synthetic instruction"},{"role":"user","content":"first"},{"role":"user","content":"second"}]}`)
		ctx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gemini-3-flash", body)
		ok, err := r.candidateGateway.candidateSupportsRequest(ctx, &web, PlatformGemini, false, "gemini-3-flash", ShapeOpenAIChat)
		require.NoError(t, err)
		require.False(t, ok)
		_, state, err := r.PrepareCandidateRequest(ctx, key, ShapeOpenAIChat, "/v1/chat/completions", "gemini-3-flash", body, "", "")
		require.NoError(t, err)
		require.Equal(t, peer.ID, state.current.account.ID)
		require.Equal(t, int64(16), state.current.group.ID)
		// Direct keys cannot escape their bound group to find that peer.
		key.GroupID, key.Group = &groups[0].ID, &groups[0]
		key.RoutingMode = RoutingModeDirect
		_, state, err = r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gemini-3-flash", body, "", "")
		require.Error(t, err)
		require.Nil(t, state)
	}
}

func TestGeminiWebCandidateNativeTextAndImageRemainAvailable(t *testing.T) {
	for _, relay := range []bool{false, true} {
		for _, model := range []string{"gemini-3-flash", "nano-2"} {
			web := webCandidateAccount(relay)
			group := grp(740, PlatformGemini, 1, false)
			group.AllowImageGeneration = true
			r, _, key := globalCandidateFixture([]Group{group}, []Account{web})
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			if model == "nano-2" {
				body = []byte(`{"contents":[{"parts":[{"text":"Draw a cube"}]}],"generationConfig":{"responseModalities":["IMAGE"]}}`)
			}
			_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeGemini, "/v1beta/models/"+model+":generateContent", model, body, "", "")
			require.NoError(t, err)
			require.Equal(t, web.ID, state.current.account.ID)
		}
	}
}

func TestGeminiWebCandidatePreservesLocalCountTokens(t *testing.T) {
	for _, relay := range []bool{false, true} {
		web := webCandidateAccount(relay)
		r, _, key := globalCandidateFixture([]Group{grp(740, PlatformGemini, 1, false)}, []Account{web})
		body := []byte(`{"model":"gemini-3-flash","system":"abcd","messages":[{"role":"user","content":"hello"}]}`)
		ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicCountTokens, "/v1/messages/count_tokens", "gemini-3-flash", body, "", "")
		require.NoError(t, err)
		require.Equal(t, web.ID, state.current.account.ID)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil).WithContext(ctx)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, err)
		// No upstream client: this capability is implemented by the local estimator.
		require.NoError(t, (&GatewayService{}).ForwardCountTokens(ctx, c, state.current.account, parsed))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.JSONEq(t, `{"input_tokens":3}`, recorder.Body.String())
	}
}

func TestGeminiWebCandidateNativeActionCapability(t *testing.T) {
	for _, relay := range []bool{false, true} {
		for _, action := range []string{"generateContent", "streamGenerateContent", "countTokens"} {
			t.Run(fmt.Sprintf("relay=%t/%s", relay, action), func(t *testing.T) {
				web := webCandidateAccount(relay)
				peer := webCandidateAccount(false)
				peer.ID = 62
				delete(peer.Credentials, "gemini_web")
				group := grp(740, PlatformGemini, 1, false)
				r, _, key := globalCandidateFixture([]Group{group}, []Account{web})
				body := []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`)
				path := "/v1beta/models/gemini-3-flash:" + action
				_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeGemini, path, "gemini-3-flash", body, "", "")
				if action != "countTokens" {
					require.NoError(t, err)
					require.Equal(t, web.ID, state.current.account.ID)
					return
				}
				require.Error(t, err)
				require.Nil(t, state)
				r, _, key = globalCandidateFixture([]Group{group}, []Account{web, peer})
				_, state, err = r.PrepareCandidateRequest(context.Background(), key, ShapeGemini, path, "gemini-3-flash", body, "", "")
				require.NoError(t, err)
				require.Equal(t, peer.ID, state.current.account.ID)
			})
		}
	}
}

func TestGeminiWebAdmissionMatchesWorkerContractFixtures(t *testing.T) {
	data, err := os.ReadFile("../../../ops/gemini-web/request_contract_cases.json")
	require.NoError(t, err)
	var cases []struct {
		Name     string          `json:"name"`
		Model    string          `json:"model"`
		Body     json.RawMessage `json:"body"`
		Accepted bool            `json:"accepted"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			require.Equal(t, tc.Accepted, geminiWebNativeBodySupported(tc.Body, isImageGenerationModel(tc.Model)))
		})
	}
	for _, count := range []int{32000, 32001} {
		body, err := json.Marshal(map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": strings.Repeat("中", count)}}}}})
		require.NoError(t, err)
		require.Equal(t, count == 32000, geminiWebNativeBodySupported(body, false))
	}
}

func TestGeminiWebCapabilityMarkerDoesNotGuessFromModelOrName(t *testing.T) {
	web := webCandidateAccount(true)
	delete(web.Credentials, GeminiWebRelayCredentialKey)
	web.Name = "gemini-us4"
	require.False(t, isGeminiWebAccount(&web))
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, "", "gemini-3-flash", false, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
	require.True(t, geminiWebSupportsRequest(ctx, &web, "gemini-3-flash", ShapeOpenAIChat))
	web.Credentials[GeminiWebRelayCredentialKey] = true
	require.False(t, geminiWebSupportsRequest(ctx, &web, "gemini-3-flash", ShapeOpenAIChat))
	require.False(t, geminiWebSupportsRequest(context.Background(), &web, "gemini-3-flash", ShapeGemini))
}

func TestGeminiWebChatAndResponsesDefaultLimitIsNotWorkerCapability(t *testing.T) {
	chat := apicompat.ChatCompletionsRequest{}
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gemini-3-flash","messages":[{"role":"user","content":"hello"}]}`), &chat))
	responses, err := apicompat.ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	for _, input := range []*apicompat.ResponsesRequest{responses, {Model: "gemini-3-flash", Input: json.RawMessage(`"hello"`)}} {
		messages, err := apicompat.ResponsesToAnthropicRequest(input)
		require.NoError(t, err)
		claudeBody, err := json.Marshal(messages)
		require.NoError(t, err)
		body, err := convertClaudeMessagesToGeminiGenerateContent(claudeBody)
		require.NoError(t, err)
		require.JSONEq(t, `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":8192}}`, string(body))
		require.False(t, geminiWebNativeBodySupported(body, false))
	}
}

func TestGeminiWebCandidateRechecksCapabilityAfterWait(t *testing.T) {
	a := webCandidateAccount(true)
	delete(a.Credentials, GeminiWebRelayCredentialKey)
	r, repo, key := globalCandidateFixture([]Group{grp(740, PlatformGemini, 1, false)}, []Account{a})
	body := []byte(`{"model":"gemini-3-flash","messages":[{"role":"system","content":"instruction"},{"role":"user","content":"hello"}]}`)
	_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gemini-3-flash", body, "", "")
	require.NoError(t, err)
	repo.fresh = func(account *Account) *Account {
		account.Credentials[GeminiWebRelayCredentialKey] = true
		return account
	}
	require.ErrorIs(t, state.recheck(state.current, candidateSelectOptions{}), ErrUniversalCapacityUnavailable)
}
