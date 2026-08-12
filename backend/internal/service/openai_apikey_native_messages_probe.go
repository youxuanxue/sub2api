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
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
		return
	}

	apiKey := account.GetOpenAIApiKey()
	if apiKey == "" {
		logger.LegacyPrintf("service.openai_probe", "native_messages_skip_no_apikey: account_id=%d", accountID)
		return
	}
	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_invalid_baseurl: account_id=%d base_url=%q err=%v", accountID, baseURL, err)
		return
	}

	probeURL := buildOpenAIEndpointURL(normalizedBaseURL, apipath.Messages)
	probeModel := selectResponsesProbeModel(account)

	probeCtx, cancel := context.WithTimeout(ctx, openaiNativeMessagesProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(openaiNativeMessagesProbePayload(probeModel)))
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_build_request_failed: account_id=%d err=%v", accountID, err)
		return
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

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_request_failed: account_id=%d url=%s err=%v", accountID, probeURL, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responsesProbeMaxBodyBytes))
	if readErr != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_read_body_failed: account_id=%d url=%s err=%v", accountID, probeURL, readErr)
		return
	}

	supported := nativeMessagesProbeSupported(resp.StatusCode, bodyBytes)
	if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
		openai_compat.ExtraKeyNativeMessagesSupported: supported,
	}); err != nil {
		logger.LegacyPrintf("service.openai_probe", "native_messages_persist_failed: account_id=%d supported=%v err=%v", accountID, supported, err)
		return
	}

	logger.LegacyPrintf("service.openai_probe",
		"native_messages_probe_done: account_id=%d base_url=%s probe_model=%s status=%d supported=%v",
		accountID, normalizedBaseURL, probeModel, resp.StatusCode, supported,
	)
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
