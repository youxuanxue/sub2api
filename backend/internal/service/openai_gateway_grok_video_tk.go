package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// Grok-native video generation arm.
//
// Video on TokenKey is normally a new-api task-adaptor path (channel_type>0).
// grok is the seventh platform: native xAI OAuth, account channel_type=0, raw
// Bearer to api.x.ai/v1 — it has no new-api TaskAdaptor. xAI exposes an
// OpenAI-shaped async video API on the SAME OAuth surface grok chat/image use:
//
//	POST {base}/videos/generations  {model,prompt,duration,...}  -> {"request_id": "..."}
//	GET  {base}/videos/{request_id}  -> {"status":"done","video":{"url":...,"duration":N},"usage":{...}}
//
// So grok video is served by THIS native submit/poll arm instead of the bridge.
// It returns the exact bridge.TaskSubmitOutcome / bridge.VideoFetchOutcome
// shapes the handler already consumes, so everything downstream (vt_ public id,
// VideoTaskCache record, balance hold, per-second billing, terminal refund, S3
// offload, ownership-404) is reused verbatim — only the upstream calls are new.
//
// Heavy-only: api.x.ai/v1 is entitlement-gated to SuperGrok Heavy; a standard
// account 403s on /videos exactly as on chat. We surface that as a clean
// honesty-403 (no failover, no penalty), reusing tkIsGrokEntitlement403.

// resolveGrokVideoCredential returns the Bearer token and base URL for the grok
// native video arm. OAuth accounts use xAI access_token; API-key relay stubs
// (prod→edge mirror accounts) use the edge TokenKey api_key + base_url — the
// same split as openai_gateway_chat_completions_raw.go.
func resolveGrokVideoCredential(account *Account) (token, baseURL string, err error) {
	if account == nil {
		return "", "", errors.New("grok video: account is nil")
	}
	switch {
	case account.IsGrokOAuth():
		token = account.GetGrokAccessToken()
		baseURL = account.GetGrokBaseURL()
	case account.IsGrokAPIKey():
		token = account.GetOpenAIApiKey()
		baseURL = account.GetGrokBaseURL()
		if baseURL == "" {
			baseURL = account.GetOpenAIBaseURL()
		}
	default:
		return "", "", fmt.Errorf("grok account %d unsupported type for video", account.ID)
	}
	if token == "" {
		if account.IsGrokAPIKey() {
			return "", "", fmt.Errorf("grok relay account %d missing api_key", account.ID)
		}
		return "", "", fmt.Errorf("grok account %d missing access_token", account.ID)
	}
	if strings.TrimSpace(baseURL) == "" {
		return "", "", fmt.Errorf("grok relay account %d missing base_url", account.ID)
	}
	return token, baseURL, nil
}

// grokNativeVideoSubmit POSTs the client's video request to xAI's OAuth video
// endpoint and writes the OpenAI-Video submit acknowledgement (carrying TK's
// public task id) to the gin response — mirroring the bridge contract that the
// dispatch writes the wire body and the handler does NOT call c.JSON again.
func (s *OpenAIGatewayService) grokNativeVideoSubmit(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	publicTaskID string,
	body []byte,
) (*bridge.TaskSubmitOutcome, error) {
	reqModel := gjson.GetBytes(body, "model").String()
	if reqModel == "" {
		return nil, fmt.Errorf("grok video submit: model is required")
	}
	// Honor a per-account model_mapping if any (account 6 has none → passthrough),
	// identical to the chat raw path.
	billingModel := resolveOpenAIForwardModel(account, reqModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	upstreamBody := body
	if upstreamModel != "" && upstreamModel != reqModel {
		upstreamBody = ReplaceModelInBody(body, upstreamModel)
	}

	token, grokBase, cerr := resolveGrokVideoCredential(account)
	if cerr != nil {
		return nil, cerr
	}
	base, err := s.validateUpstreamBaseURL(grokBase)
	if err != nil {
		return nil, fmt.Errorf("invalid grok base_url: %w", err)
	}
	url := strings.TrimRight(base, "/") + "/videos/generations"

	respBody, status, herr := s.grokVideoHTTP(ctx, account, http.MethodPost, url, token, upstreamBody)
	if herr != nil {
		return nil, herr
	}
	if status >= 400 {
		return nil, s.grokVideoUpstreamError(status, respBody)
	}
	upstreamTaskID := gjson.GetBytes(respBody, "request_id").String()
	if upstreamTaskID == "" {
		upstreamTaskID = gjson.GetBytes(respBody, "id").String()
	}
	if upstreamTaskID == "" {
		return nil, fmt.Errorf("grok video submit: upstream returned no request_id")
	}

	// Write the OpenAI-Video submit acknowledgement carrying TK's PUBLIC id (the
	// client polls GET /v1/videos/{id} with it; the registry maps public→upstream).
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(buildGrokVideoSubmitResponse(publicTaskID, reqModel))

	logger.L().Info("openai_gateway.grok_native_video_submit",
		zap.Int64("account_id", account.ID),
		zap.String("model", reqModel),
		zap.String("upstream_task_id", upstreamTaskID),
	)

	return &bridge.TaskSubmitOutcome{
		PublicTaskID:   publicTaskID,
		UpstreamTaskID: upstreamTaskID,
		UpstreamModel:  upstreamModel,
		OriginModel:    reqModel,
		ChannelType:    account.ChannelType, // 0 for grok native — pinned on the record
		BaseURL:        base,
		APIKey:         token,
	}, nil
}

// grokNativeVideoFetch polls xAI for a previously-submitted grok video task.
// The fetch is account-agnostic at the bridge boundary, but grok's OAuth Bearer
// rotates (GrokTokenRefresher), so the pinned record APIKey may be stale by poll
// time — we re-resolve a FRESH token via in.AccountID instead of trusting it.
func (s *OpenAIGatewayService) grokNativeVideoFetch(
	ctx context.Context,
	in bridge.VideoFetchInput,
) (*bridge.VideoFetchOutcome, error) {
	if strings.TrimSpace(in.UpstreamTaskID) == "" {
		return nil, errors.New("grok video fetch requires upstream_task_id")
	}
	base := strings.TrimRight(in.BaseURL, "/")
	token := in.APIKey
	var account *Account
	if in.AccountID > 0 {
		if acc, err := s.accountRepo.GetByID(ctx, in.AccountID); err == nil && acc != nil && acc.IsGrok() {
			account = acc
			if freshToken, freshBase, rerr := resolveGrokVideoCredential(acc); rerr == nil {
				token = freshToken // re-resolve: OAuth Bearer may rotate; relay api_key is stable
				if b, verr := s.validateUpstreamBaseURL(freshBase); verr == nil && b != "" {
					base = strings.TrimRight(b, "/")
				}
			}
		}
	}
	if token == "" {
		return nil, errors.New("grok video fetch: no usable bearer credential")
	}
	url := base + "/videos/" + in.UpstreamTaskID

	respBody, status, herr := s.grokVideoHTTP(ctx, account, http.MethodGet, url, token, nil)
	if herr != nil {
		return nil, herr
	}
	if status >= 400 {
		return nil, s.grokVideoUpstreamError(status, respBody)
	}

	xaiStatus := gjson.GetBytes(respBody, "status").String()
	// Surface a top-level video_url on success so the client / Studio reads it the
	// same way it reads the bridge's normalized (and S3-offloaded) shape, while the
	// rest of xAI's body passes through untouched.
	out := respBody
	if videoURL := gjson.GetBytes(respBody, "video.url").String(); videoURL != "" {
		if b, serr := sjson.SetBytes(respBody, "video_url", videoURL); serr == nil {
			out = b
		}
	}

	return &bridge.VideoFetchOutcome{
		RawResponse: out,
		// Normalize xAI's status enum (queued/processing/done/failed/expired) into
		// the handler's videoTerminalOutcome vocabulary (success/failure/other) so
		// terminal-success retention + terminal-failure refund fire correctly.
		Status: normalizeGrokVideoStatus(xaiStatus),
	}, nil
}

// grokVideoHTTP issues a single grok-OAuth request to xAI's video API via the
// shared upstream HTTP client (proxy + per-account concurrency honored). Bodies
// are small JSON (request_id on submit; status+url on poll), so reading fully is
// fine. account may be nil on the fetch path (used only for proxy/concurrency).
// grokVideoResponseMaxBytes bounds the grok-native video submit/poll response we
// read into memory. xAI returns small URL-JSON (the clip is hosted upstream, never
// inline base64), so a few-MB ceiling is honest headroom while bounding a hostile or
// runaway upstream. This is the grok-arm counterpart to the new-api bridge's
// videoFetchResponseMaxBytes (80MB); we keep a SEPARATE service-local const rather
// than importing the bridge's package-private value — grok never carries inline
// bytes, so a smaller cap is more honest, and widening the bridge surface buys nothing.
const grokVideoResponseMaxBytes int64 = 16 << 20

var errGrokVideoResponseTooLarge = errors.New("grok video upstream response too large")

// readGrokVideoResponseLimited reads at most maxBytes (+1 sentinel byte) from r and
// errors if the body exceeds the cap — the same bounded-read shape the bridge uses in
// readVideoFetchResponseBodyLimited. Extracted so the bound is unit-testable without a
// live upstream.
func readGrokVideoResponseLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = grokVideoResponseMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errGrokVideoResponseTooLarge
	}
	return body, nil
}

func (s *OpenAIGatewayService) grokVideoHTTP(
	ctx context.Context,
	account *Account,
	method, url, token string,
	body []byte,
) ([]byte, int, error) {
	upstreamCtx, release := detachUpstreamContext(ctx)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(upstreamCtx, method, url, rdr)
	release()
	if err != nil {
		return nil, 0, fmt.Errorf("build grok video request: %w", err)
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	proxyURL := ""
	accID := int64(0)
	concurrency := 0
	if account != nil {
		if account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		accID = account.ID
		concurrency = account.Concurrency
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, accID, concurrency)
	if err != nil {
		return nil, 0, fmt.Errorf("grok video upstream request failed: %s", sanitizeUpstreamErrorMessage(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, rerr := readGrokVideoResponseLimited(resp.Body, grokVideoResponseMaxBytes)
	if rerr != nil {
		return nil, resp.StatusCode, fmt.Errorf("read grok video response: %w", rerr)
	}
	return respBody, resp.StatusCode, nil
}

// grokVideoUpstreamError maps an xAI video error response to a client error.
// A Heavy-only entitlement 403 is surfaced as a clean honesty-403 (skip-retry,
// no penalty), identical to the grok chat posture; other statuses pass through.
func (s *OpenAIGatewayService) grokVideoUpstreamError(status int, body []byte) error {
	msg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if tkIsGrokEntitlement403(status, body) {
		return &NewAPIRelayError{Err: newapitypes.NewErrorWithStatusCode(
			errors.New(tkGrokEntitlement403ClientMessage(msg)),
			newapitypes.ErrorCodeInvalidRequest,
			http.StatusForbidden,
			newapitypes.ErrOptionWithSkipRetry(),
		)}
	}
	clientStatus := http.StatusBadGateway
	if status >= 400 && status < 500 {
		clientStatus = status
	}
	if msg == "" {
		msg = "grok video upstream error"
	}
	return &NewAPIRelayError{Err: newapitypes.NewErrorWithStatusCode(
		errors.New(msg),
		newapitypes.ErrorCodeInvalidRequest,
		clientStatus,
		newapitypes.ErrOptionWithSkipRetry(),
	)}
}

// buildGrokVideoSubmitResponse builds an OpenAI-Video-compatible submit
// acknowledgement carrying TK's public task id. The client extracts `id` and
// polls GET /v1/videos/{id}; TK resolves it to the pinned upstream request_id.
func buildGrokVideoSubmitResponse(publicTaskID, model string) []byte {
	payload := map[string]any{
		"id":         publicTaskID,
		"object":     "video",
		"model":      model,
		"status":     "queued",
		"progress":   0,
		"created_at": time.Now().Unix(),
	}
	b, _ := json.Marshal(payload)
	return b
}

// normalizeGrokVideoStatus maps xAI's video status enum onto the handler's
// videoTerminalOutcome vocabulary. xAI uses queued/processing/done/failed/
// expired; the handler keys terminal-success retention on "success"/"succeeded"
// and terminal-failure refund on "failure"/"failed".
//
// "expired" is deliberately NOT mapped to a terminal status — it falls through
// as a non-terminal passthrough, exactly like the bridge/new-api path (whose
// videoTerminalOutcome only recognizes failure/failed + success/succeeded and
// never treats "expired" as terminal). The reason is money-safety on the
// submit-time billing model: the user is charged once at submit, a "done" poll
// is billed-and-KEPT, and "expired" can be the RESULT-TTL state observed AFTER
// a successful "done" (the clip URL aged out). If "expired" triggered the
// terminal-failure refund, a user who already received and was billed for a
// "done" clip would get refunded on a later poll — a refund-after-success leak.
// Leaving it non-terminal means a genuinely-never-completed task simply stops
// at the record TTL (no auto-refund, same as every other video channel); the
// rare true-expiry refund is a support action, which is the correct trade for
// not leaking money. "canceled" stays terminal-failure: a cancel cannot follow
// a "done", so its refund is always for an unconsumed task.
func normalizeGrokVideoStatus(xaiStatus string) string {
	switch strings.ToLower(strings.TrimSpace(xaiStatus)) {
	case "done", "success", "succeeded", "completed":
		return "success"
	case "failed", "failure", "canceled", "cancelled":
		return "failure"
	default:
		return xaiStatus // queued / processing / expired → non-terminal (see above)
	}
}
