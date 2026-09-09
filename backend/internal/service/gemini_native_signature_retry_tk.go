package service

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// tkRepairGeminiNativeSignature preserves the response for ordinary error handling
// unless an upstream signature rejection can be repaired on the same account.
func (s *GeminiMessagesCompatService) tkRepairGeminiNativeSignature(c *gin.Context, account *Account, resp *http.Response, body []byte) ([]byte, bool) {
	if resp.StatusCode != http.StatusBadRequest {
		return body, false
	}
	hasSignatureToRepair := false
	for _, content := range gjson.GetBytes(body, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if signature := part.Get("thoughtSignature"); signature.Exists() && signature.String() != geminiDummyThoughtSignature {
				hasSignatureToRepair = true
			}
		}
	}
	if !hasSignatureToRepair {
		return body, false
	}
	respBody := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	message := extractUpstreamErrorMessage(unwrapIfNeeded(account.Type == AccountTypeOAuth, respBody))
	normalized := strings.ToLower(message)
	if !strings.Contains(normalized, "thought signature") && !strings.Contains(normalized, "thought_signature") && !strings.Contains(normalized, "thoughtsignature") {
		return body, false
	}
	repaired := CleanGeminiNativeThoughtSignatures(body)
	if bytes.Equal(repaired, body) {
		return body, false
	}
	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = resp.Header.Get("x-goog-request-id")
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: requestID,
		Kind: "signature_error", Message: sanitizeUpstreamErrorMessage(message),
	})
	return repaired, true
}
