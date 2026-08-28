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
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
)

const openaiNativeMessagesProbeTimeout = 15 * time.Second

func openaiNativeMessagesProbePayload(modelID string) []byte {
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

func (s *AccountTestService) probeOpenAIAPIKeyNativeMessagesSupport(
	ctx context.Context,
	account *Account,
) (protocolProbeObservation, bool) {
	accountID := account.ID
	authToken := protocolAuthorizationToken(account)
	if authToken == "" {
		logger.LegacyPrintf("service.openai_probe", "native_messages_skip_no_auth: account_id=%d", accountID)
		return protocolProbeObservation{}, false
	}
	baseURL := protocolProbeBaseURL(account, protocolrouter.ProtocolMessages)
	if baseURL == "" {
		logger.LegacyPrintf("service.openai_probe", "native_messages_skip_no_explicit_baseurl: account_id=%d", accountID)
		return protocolProbeObservation{}, false
	}
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_invalid_baseurl: account_id=%d base_url=%q err=%v", accountID, baseURL, err)
		return protocolProbeObservation{}, false
	}

	probeCtx, cancel := context.WithTimeout(ctx, openaiNativeMessagesProbeTimeout)
	defer cancel()

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	probeModels := protocolProbeModelCandidates(account)
	for index, probeModel := range probeModels {
		probeURL := buildOpenAIEndpointURL(normalizedBaseURL, apipath.Messages)
		req, requestErr := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(openaiNativeMessagesProbePayload(probeModel)))
		if requestErr != nil {
			logger.LegacyPrintf("service.openai_probe", "native_messages_build_request_failed: account_id=%d err=%v", accountID, requestErr)
			return protocolProbeObservation{}, false
		}
		req.Header.Set("Content-Type", "application/json")
		if account.Platform == PlatformAnthropic {
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
			if account.IsAnthropicOAuthOrSetupToken() {
				setAnthropicOAuthPassthroughAuthHeader(req.Header, authToken)
			} else {
				setAnthropicAPIKeyAuthHeader(req.Header, account, authToken)
			}
		} else {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		req.Header.Set("Accept", "application/json")
		req = applyProtocolProbeRequestIdentity(req, account, protocolrouter.ProtocolMessages)

		resp, requestErr := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
		if requestErr != nil {
			logger.LegacyPrintf("service.openai_probe", "native_messages_request_failed: account_id=%d url=%s err=%v", accountID, probeURL, requestErr)
			return protocolProbeObservation{}, false
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			logger.LegacyPrintf("service.openai_probe", "native_messages_read_body_failed: account_id=%d url=%s err=%v", accountID, probeURL, readErr)
			return protocolProbeObservation{}, false
		}

		supported := nativeMessagesProbeSupported(resp.StatusCode, bodyBytes)
		verdict := ProtocolProbeInconclusive
		if supported {
			verdict = ProtocolProbePositive
		} else if protocolProbeModelSpecificHTTPFailure(resp.StatusCode, bodyBytes) {
			verdict = ProtocolProbeModelSpecific
		} else if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			verdict = ProtocolProbeEndpointNegative
		}
		logger.LegacyPrintf("service.openai_probe",
			"native_messages_probe_done: account_id=%d base_url=%s probe_model=%s status=%d supported=%v verdict=%s",
			accountID, normalizedBaseURL, probeModel, resp.StatusCode, supported, verdict,
		)
		if protocolProbeShouldTryNextModel(account, protocolrouter.ProtocolMessages, probeModel, resp.StatusCode, verdict, index+1 < len(probeModels)) {
			continue
		}
		return protocolProbeObservation{
			protocol: protocolrouter.ProtocolMessages,
			verdict:  verdict,
		}, true
	}
	return protocolProbeObservation{}, false
}

func nativeMessagesProbeSupported(status int, body []byte) bool {
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return false
	}
	if status < 200 || status >= 300 {
		return false
	}
	msgType := strings.TrimSpace(gjson.GetBytes(body, "type").String())
	return msgType == "message" || gjson.GetBytes(body, "content").Exists()
}
