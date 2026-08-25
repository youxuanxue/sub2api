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
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
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

// ProbeOpenAIAPIKeyNativeMessagesSupport probes whether an OpenAI APIKey account
// upstream exposes Anthropic /v1/messages and persists the result to
// accounts.extra.openai_native_messages_supported.
func (s *AccountTestService) ProbeOpenAIAPIKeyNativeMessagesSupport(ctx context.Context, accountID int64) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_load_account_failed: account_id=%d err=%v", accountID, err)
		return
	}
	if !protocolProbeSupports(account, protocolrouter.ProtocolMessages) {
		return
	}
	revision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_revision_failed: account_id=%d err=%v", accountID, err)
		return
	}
	observation, observed := s.probeOpenAIAPIKeyNativeMessagesSupport(ctx, account, revision)
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
		logger.LegacyPrintf("service.openai_probe", "native_messages_persist_failed: account_id=%d err=%v", accountID, err)
	}
}

func (s *AccountTestService) probeOpenAIAPIKeyNativeMessagesSupport(
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
		logger.LegacyPrintf("service.openai_probe", "native_messages_skip_no_apikey: account_id=%d", accountID)
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

	probeURL := buildOpenAIEndpointURL(normalizedBaseURL, apipath.Messages)
	probeModel := selectResponsesProbeModel(account)

	probeCtx, cancel := context.WithTimeout(ctx, openaiNativeMessagesProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(openaiNativeMessagesProbePayload(probeModel)))
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_build_request_failed: account_id=%d err=%v", accountID, err)
		return protocolProbeObservation{}, false
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	if account.Platform == PlatformAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
		setAnthropicAPIKeyAuthHeader(req.Header, account, apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_request_failed: account_id=%d url=%s err=%v", accountID, probeURL, err)
		return protocolProbeObservation{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
	if readErr != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_read_body_failed: account_id=%d url=%s err=%v", accountID, probeURL, readErr)
		return protocolProbeObservation{}, false
	}

	supported := nativeMessagesProbeSupported(resp.StatusCode, bodyBytes)
	verdict := ProtocolProbeInconclusive
	if supported {
		verdict = ProtocolProbePositive
	} else if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		verdict = ProtocolProbeEndpointNegative
	}
	logger.LegacyPrintf("service.openai_probe",
		"native_messages_probe_done: account_id=%d base_url=%s probe_model=%s status=%d supported=%v",
		accountID, normalizedBaseURL, probeModel, resp.StatusCode, supported,
	)
	return protocolProbeObservation{
		protocol: protocolrouter.ProtocolMessages,
		verdict:  verdict,
		legacyUpdates: map[string]any{
			openai_compat.ExtraKeyNativeMessagesSupported: supported,
		},
	}, true
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
