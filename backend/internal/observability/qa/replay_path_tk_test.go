//go:build unit

package qa

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"

	"github.com/stretchr/testify/require"
)

func TestBuildBlobPreservesReplayActionWithoutChangingProtocolFamily(t *testing.T) {
	svc := &Service{bodyMaxBytes: 256 * 1024}
	for _, path := range []string{"/v1/messages/count_tokens", "/v1beta/models/gemini-test:streamGenerateContent", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			family := normalizeInboundEndpoint(path)
			blob, _, _, _, err := svc.buildBlob(CaptureInput{
				RequestID: "request", RequestPath: path, InboundEndpoint: family,
				RequestBody: []byte(`{"model":"test"}`), ResponseBody: []byte(`{"input_tokens":3}`),
			})
			require.NoError(t, err)
			request := decodeBlobPayload(t, blob)["request"].(map[string]any)
			require.Equal(t, path, request["original_path"])
			require.Equal(t, family, request["path"])
		})
	}
	blob, _, _, _, err := svc.buildBlob(CaptureInput{InboundEndpoint: "/v1/messages"})
	require.NoError(t, err)
	require.NotContains(t, decodeBlobPayload(t, blob)["request"], "original_path", "legacy input cannot claim original-path evidence")
}

func TestQAMiddlewareKeepsOriginalPathBeforeHandlerRewrite(t *testing.T) {
	// No auth subject: capture middleware runs, but persistence is never invoked.
	svc := &Service{cfg: config.QACaptureConfig{Enabled: true}, client: &ent.Client{}, store: newMemBlobStore()}
	router := gin.New()
	router.Use(svc.Middleware())
	var ctx *gin.Context
	router.POST("/v1/messages/count_tokens", func(c *gin.Context) {
		ctx = c
		c.Request.URL.Path = "/v1/messages"
		c.JSON(200, gin.H{"input_tokens": 3})
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages/count_tokens?key=private-query", nil))
	require.Equal(t, "/v1/messages/count_tokens", ctx.GetString(contextKeyRequestPath))
	blob, _, _, _, err := svc.buildBlob(CaptureInput{
		RequestPath: ctx.GetString(contextKeyRequestPath), InboundEndpoint: captureInboundEndpoint(ctx),
	})
	require.NoError(t, err)
	request := decodeBlobPayload(t, blob)["request"].(map[string]any)
	require.Equal(t, "/v1/messages/count_tokens", request["original_path"])
	require.Equal(t, "/v1/messages", request["path"])
}
