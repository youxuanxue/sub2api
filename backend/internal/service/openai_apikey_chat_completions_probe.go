package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/apipath"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
)

const openaiChatCompletionsProbeTimeout = 15 * time.Second

func openaiChatCompletionsProbePayload(modelID string) []byte {
	if strings.TrimSpace(modelID) == "" {
		modelID = openai.DefaultTestModel
	}
	body, _ := json.Marshal(map[string]any{
		"model":      modelID,
		"max_tokens": 8,
		"stream":     false,
		"messages": []map[string]any{
			{"role": "user", "content": "Reply OK only."},
		},
	})
	return body
}

func (s *AccountTestService) ProbeOpenAIAPIKeyChatCompletionsSupport(ctx context.Context, accountID int64) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_load_account_failed: account_id=%d err=%v", accountID, err)
		return
	}
	if !protocolProbeSupports(account, protocolrouter.ProtocolChatCompletions) {
		return
	}
	revision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_revision_failed: account_id=%d err=%v", accountID, err)
		return
	}
	observation, observed := s.probeOpenAIAPIKeyChatCompletionsSupport(ctx, account, revision)
	if !observed {
		return
	}
	if err := PersistProtocolProbeVerdicts(
		ctx,
		s.accountRepo,
		accountID,
		revision,
		map[protocolrouter.Protocol]ProtocolProbeVerdict{observation.protocol: observation.verdict},
		observation.legacyUpdates,
	); err != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_persist_failed: account_id=%d err=%v", accountID, err)
	}
}

func (s *AccountTestService) probeOpenAIAPIKeyChatCompletionsSupport(
	ctx context.Context,
	account *Account,
	_ string,
) (protocolProbeObservation, bool) {
	accountID := account.ID
	apiKey := account.GetOpenAIProtocolAPIKey()
	if apiKey == "" {
		apiKey = account.GetCredential("api_key")
	}
	if apiKey == "" {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_skip_no_apikey: account_id=%d", accountID)
		return protocolProbeObservation{}, false
	}
	baseURL := protocolProbeBaseURL(account, protocolrouter.ProtocolChatCompletions)
	if baseURL == "" {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_skip_no_explicit_baseurl: account_id=%d", accountID)
		return protocolProbeObservation{}, false
	}
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_invalid_baseurl: account_id=%d base_url=%q err=%v", accountID, baseURL, err)
		return protocolProbeObservation{}, false
	}

	probeURL := buildOpenAIEndpointURL(normalizedBaseURL, apipath.ChatCompletions)
	probeModel := selectResponsesProbeModel(account)
	probeCtx, cancel := context.WithTimeout(ctx, openaiChatCompletionsProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(openaiChatCompletionsProbePayload(probeModel)))
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_build_request_failed: account_id=%d err=%v", accountID, err)
		return protocolProbeObservation{}, false
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.resolveAccountTestTLSProfile(account))
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_request_failed: account_id=%d url=%s err=%v", accountID, probeURL, err)
		return protocolProbeObservation{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
	if readErr != nil {
		logger.LegacyPrintf("service.openai_probe", "chat_completions_read_body_failed: account_id=%d url=%s err=%v", accountID, probeURL, readErr)
		return protocolProbeObservation{}, false
	}

	verdict := ProtocolProbeInconclusive
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && gjson.GetBytes(bodyBytes, "choices").IsArray() {
		verdict = ProtocolProbePositive
	} else if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		verdict = ProtocolProbeEndpointNegative
	}
	logger.LegacyPrintf(
		"service.openai_probe",
		"chat_completions_probe_done: account_id=%d base_url=%s probe_model=%s status=%d verdict=%s",
		accountID,
		normalizedBaseURL,
		probeModel,
		resp.StatusCode,
		verdict,
	)
	return protocolProbeObservation{
		protocol: protocolrouter.ProtocolChatCompletions,
		verdict:  verdict,
	}, true
}
