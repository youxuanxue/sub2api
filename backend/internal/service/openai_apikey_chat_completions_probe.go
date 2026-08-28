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

func (s *AccountTestService) probeOpenAIAPIKeyChatCompletionsSupport(
	ctx context.Context,
	account *Account,
) (protocolProbeObservation, bool) {
	accountID := account.ID
	apiKey := protocolAuthorizationToken(account)
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

	probeCtx, cancel := context.WithTimeout(ctx, openaiChatCompletionsProbeTimeout)
	defer cancel()

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	probeModels := protocolProbeModelCandidates(account)
	for index, probeModel := range probeModels {
		probeURL := buildOpenAIEndpointURL(normalizedBaseURL, apipath.ChatCompletions)
		if exactEndpoint, exactErr := protocolExactEndpoint(account, protocolrouter.ProtocolChatCompletions, probeModel); exactErr != nil {
			logger.LegacyPrintf("service.openai_probe", "chat_completions_resolve_endpoint_failed: account_id=%d err=%v", accountID, exactErr)
			return protocolProbeObservation{}, false
		} else if exactEndpoint != "" {
			probeURL, err = s.validateUpstreamBaseURL(exactEndpoint)
			if err != nil {
				logger.LegacyPrintf("service.openai_probe", "chat_completions_invalid_endpoint: account_id=%d endpoint=%q err=%v", accountID, exactEndpoint, err)
				return protocolProbeObservation{}, false
			}
		}

		req, requestErr := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(openaiChatCompletionsProbePayload(probeModel)))
		if requestErr != nil {
			logger.LegacyPrintf("service.openai_probe", "chat_completions_build_request_failed: account_id=%d err=%v", accountID, requestErr)
			return protocolProbeObservation{}, false
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")
		req = applyProtocolProbeRequestIdentity(req, account, protocolrouter.ProtocolChatCompletions)

		resp, requestErr := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.resolveAccountTestTLSProfile(account))
		if requestErr != nil {
			logger.LegacyPrintf("service.openai_probe", "chat_completions_request_failed: account_id=%d url=%s err=%v", accountID, probeURL, requestErr)
			return protocolProbeObservation{}, false
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			logger.LegacyPrintf("service.openai_probe", "chat_completions_read_body_failed: account_id=%d url=%s err=%v", accountID, probeURL, readErr)
			return protocolProbeObservation{}, false
		}

		verdict := ProtocolProbeInconclusive
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && gjson.GetBytes(bodyBytes, "choices").IsArray() {
			verdict = ProtocolProbePositive
		} else if protocolProbeModelSpecificHTTPFailure(resp.StatusCode, bodyBytes) {
			verdict = ProtocolProbeModelSpecific
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
		if protocolProbeShouldTryNextModel(account, protocolrouter.ProtocolChatCompletions, probeModel, resp.StatusCode, bodyBytes, verdict, index+1 < len(probeModels)) {
			continue
		}
		return protocolProbeObservation{
			protocol: protocolrouter.ProtocolChatCompletions,
			verdict:  verdict,
		}, true
	}
	return protocolProbeObservation{}, false
}
