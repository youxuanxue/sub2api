package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	cursorbridge "github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func validateCursorBaseURL(account *Account, raw string, fallback func(string) (string, error)) (string, error) {
	if !account.IsCursor() || isEdgeMirrorStub(account, edgeIDPattern) {
		return fallback(raw)
	}
	if strings.TrimRight(strings.TrimSpace(raw), "/") != cursorbridge.AgentBaseURL {
		return "", errors.New("cursor account requires browser reauthorization for the native endpoint")
	}
	return cursorbridge.AgentBaseURL, nil
}

func cursorModelParameters(account *Account, model string) ([]cursorbridge.Parameter, bool) {
	if !account.IsCursor() {
		return nil, false
	}
	all, ok := account.Credentials[CursorModelParametersKey].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := all[model]
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var params []cursorbridge.Parameter
	if json.Unmarshal(raw, &params) != nil {
		return nil, false
	}
	return params, true
}

func cursorMappedModelAllowed(account *Account, requested, resolved string) bool {
	_, mapped := account.ResolveMappedModel(requested)
	_, known := cursorModelParameters(account, resolved)
	if isEdgeMirrorStub(account, edgeIDPattern) {
		return mapped && known
	}
	all, _ := account.Credentials[CursorWireModelsKey].(map[string]any)
	wireModel, _ := all[resolved].(string)
	return mapped && known && wireModel != ""
}

func prepareCursorUpstreamRequest(req *http.Request, _ *gin.Context, account *Account) error {
	if !account.IsCursor() || isEdgeMirrorStub(account, edgeIDPattern) {
		return nil
	}
	if req.URL.String() != cursorbridge.AgentBaseURL+"/v1/messages" {
		return errors.New("invalid Cursor native endpoint")
	}
	return nil
}

// Native Messages, converted Chat/Responses and probes share this transport.
func (s *OpenAIGatewayService) doNativeMessagesRequest(req *http.Request, account *Account) (*http.Response, error) {
	if account.IsCursor() && !isEdgeMirrorStub(account, edgeIDPattern) {
		return executeCursorMessages(req, account, s.httpUpstream)
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
}

func executeCursorMessages(req *http.Request, account *Account, upstream HTTPUpstream) (*http.Response, error) {
	if req.Body == nil {
		return nil, errors.New("missing Cursor request body")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, (16<<20)+1))
	_ = req.Body.Close()
	if err != nil || len(body) > 16<<20 {
		return nil, errors.New("invalid Cursor request size")
	}
	model := gjson.GetBytes(body, "model").String()
	parameters, known := cursorModelParameters(account, model)
	all, _ := account.Credentials[CursorWireModelsKey].(map[string]any)
	wireModel, _ := all[model].(string)
	if !known || wireModel == "" {
		return nil, errors.New("cursor model is absent from the account catalog; reconnect the account")
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return cursorbridge.Messages(req.Context(), account.GetCredential("api_key"), body, parameters, wireModel, func(native *http.Request) (*http.Response, error) {
		ctx := WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(native.Context(), HTTPUpstreamProfileCursor))
		return upstream.Do(native.WithContext(ctx), proxyURL, account.ID, account.Concurrency)
	})
}

func cursorResponseOutcome(account *Account, resp *http.Response, result *OpenAIForwardResult, err *error) {
	if !account.IsCursor() || resp == nil {
		return
	}
	if body, ok := resp.Body.(*cursorbridge.MessagesBody); ok {
		tier, nativeErr := body.Outcome()
		if result != nil {
			result.BillingTier = tier
		}
		if *err == nil && nativeErr != nil {
			*err = nativeErr
		}
	}
}

func cursorBillingTier(tier string) string {
	switch tier {
	case cursorbridge.ReportedBillingTier, cursorbridge.EstimatedBillingTier:
		return tier
	default:
		return ""
	}
}
