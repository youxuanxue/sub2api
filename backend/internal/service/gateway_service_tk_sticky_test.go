package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkEnsureClaudeCodeSessionHeader_IngressParsedRequestFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	deviceID := strings.Repeat("a", 64)
	uid := FormatMetadataUserID(deviceID, "", "33333333-3333-3333-3333-333333333333", "2.1.22")
	pr := &ParsedRequest{MetadataUserID: uid}
	c.Set(ClaudeCodeParsedRequestGinKey, pr)

	hdr := http.Header{}
	body := []byte(`{"model":"claude-opus","messages":[]}`)
	tkEnsureClaudeCodeSessionHeader(hdr, body, c)
	require.Equal(t, "33333333-3333-3333-3333-333333333333", getHeaderRaw(hdr, "X-Claude-Code-Session-Id"))
}

func TestTkEnsureClaudeCodeSessionHeader_BodyOverridesIngressFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	bodyUID := FormatMetadataUserID(strings.Repeat("b", 64), "", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "2.1.22")
	payload, err := json.Marshal(map[string]any{
		"model":    "claude-opus",
		"metadata": map[string]string{"user_id": bodyUID},
		"messages": []any{},
	})
	require.NoError(t, err)

	ingressUID := FormatMetadataUserID(strings.Repeat("c", 64), "", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "2.1.22")
	c.Set(ClaudeCodeParsedRequestGinKey, &ParsedRequest{MetadataUserID: ingressUID})

	hdr := http.Header{}
	tkEnsureClaudeCodeSessionHeader(hdr, payload, c)
	require.Equal(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", getHeaderRaw(hdr, "X-Claude-Code-Session-Id"))
}
