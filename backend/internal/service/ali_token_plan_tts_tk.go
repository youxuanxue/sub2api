package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	aliTokenPlanTTSDefaultVoice      = "longanlingxin"
	aliTokenPlanTTSDefaultFormat     = "mp3"
	aliTokenPlanTTSDefaultSampleRate = 24000
	aliTokenPlanTTSAudioFetchTimeout = 60 * time.Second
)

// ForwardAliTokenPlanTTS synthesizes speech via Ali Token Plan SpeechSynthesizer
// and writes raw audio bytes to the client (OpenAI /v1/audio/speech shape).
// Does not use the new-api Ali adaptor (ConvertAudioRequest is unimplemented).
func (s *OpenAIGatewayService) ForwardAliTokenPlanTTS(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	if s == nil || account == nil {
		return nil, fmt.Errorf("ali token plan tts service/account is required")
	}
	if !isNewAPIAliTokenPlanAccount(account) {
		return nil, fmt.Errorf("account is not an Ali Token Plan newapi account")
	}

	synthReq, model, inputText, err := buildAliTokenPlanSpeechSynthesizerRequest(body)
	if err != nil {
		return nil, err
	}
	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}

	targetURL := newapiintegration.AliTokenPlanSpeechSynthesizerURL(account.GetBaseURL())
	upstreamCtx, release := detachUpstreamContext(ctx)
	defer release()

	req, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(synthReq))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	started := time.Now()
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		upstreamMsg := gjson.GetBytes(respBody, "message").String()
		if upstreamMsg == "" {
			upstreamMsg = gjson.GetBytes(respBody, "error.message").String()
		}
		shouldDisable := s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, model)
		retryableOnSameAccount := !shouldDisable && tkOpenAICompatRetryableOnSameAccount(account, resp.StatusCode, upstreamMsg, respBody, false)
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           respBody,
			RetryableOnSameAccount: retryableOnSameAccount,
		}
	}

	audioURL := strings.TrimSpace(gjson.GetBytes(respBody, "output.audio.url").String())
	if audioURL == "" {
		return nil, fmt.Errorf("ali token plan tts response missing output.audio.url")
	}
	chars := gjson.GetBytes(respBody, "usage.characters").Int()
	if chars <= 0 {
		chars = int64(utf8.RuneCountInString(inputText))
	}

	audioBytes, contentType, err := s.fetchAliTokenPlanTTSAudio(upstreamCtx, account, audioURL, proxyURL)
	if err != nil {
		return nil, err
	}
	if c != nil && c.Writer != nil {
		if contentType == "" {
			contentType = "audio/mpeg"
		}
		c.Header("Content-Type", contentType)
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write(audioBytes)
	}

	upstreamID := firstNonEmpty(
		resp.Header.Get("x-request-id"),
		gjson.GetBytes(respBody, "request_id").String(),
	)
	return &OpenAIForwardResult{
		RequestID:     StableGrokAudioBillingRequestID(upstreamID),
		Model:         model,
		UpstreamModel: model,
		Duration:      time.Since(started),
		AudioUsage: &AudioUsage{
			Mode:            "tts",
			DurationOrUnits: float64(chars) / 1_000_000.0,
		},
	}, nil
}

func buildAliTokenPlanSpeechSynthesizerRequest(body []byte) (payload []byte, model, inputText string, err error) {
	if !gjson.ValidBytes(body) {
		return nil, "", "", fmt.Errorf("request body must be JSON")
	}
	model = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return nil, "", "", fmt.Errorf("model is required")
	}
	inputText = strings.TrimSpace(gjson.GetBytes(body, "input").String())
	if inputText == "" {
		return nil, "", "", fmt.Errorf("input is required")
	}
	voice := strings.TrimSpace(gjson.GetBytes(body, "voice").String())
	if voice == "" {
		voice = aliTokenPlanTTSDefaultVoice
	}
	format := strings.TrimSpace(gjson.GetBytes(body, "response_format").String())
	if format == "" {
		format = aliTokenPlanTTSDefaultFormat
	}
	format = strings.ToLower(format)
	switch format {
	case "mp3", "wav", "pcm", "opus":
	default:
		format = aliTokenPlanTTSDefaultFormat
	}
	sampleRate := int(gjson.GetBytes(body, "sample_rate").Int())
	if sampleRate <= 0 {
		sampleRate = aliTokenPlanTTSDefaultSampleRate
	}

	out := map[string]any{
		"model": model,
		"input": map[string]any{
			"text":        inputText,
			"voice":       voice,
			"format":      format,
			"sample_rate": sampleRate,
		},
	}
	payload, err = json.Marshal(out)
	return payload, model, inputText, err
}

func (s *OpenAIGatewayService) fetchAliTokenPlanTTSAudio(
	ctx context.Context,
	account *Account,
	audioURL string,
	proxyURL string,
) ([]byte, string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, aliTokenPlanTTSAudioFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, "", fmt.Errorf("fetch ali tts audio: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("fetch ali tts audio: status=%d body=%s", resp.StatusCode, string(body))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}
