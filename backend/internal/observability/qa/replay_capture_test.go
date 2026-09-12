//go:build unit

package qa

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/replaycapture"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestReplayCaptureKeepsCompleteBodyOutsideOrdinaryQA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	require.NoError(t, err)
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	svc, client, _ := newQAExportTestService(t)
	dir := filepath.Join(t.TempDir(), "replay")
	svc.replay, err = replaycapture.Open(dir, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	require.NoError(t, err)
	svc.bodyMaxBytes = 256 * 1024
	body := []byte(`{"model":"real-model","messages":[{"role":"user","content":"` + strings.Repeat("x", 300*1024) + `"}],"api_key":"business-secret"}`)
	var ordinary []byte
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 5, UserID: 7, User: &service.User{ID: 7}})
		c.Next()
		if v, ok := c.Get(contextKeyRequestBytes); ok {
			var castOK bool
			ordinary, castOK = v.([]byte)
			require.True(t, castOK)
		}
	})
	r.Use(Middleware(svc))
	r.POST("/v1/messages", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, body, raw)
		c.Set("ops_request_body", raw)
		c.JSON(200, gin.H{"content": []string{"ok"}})
	})
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body)).WithContext(context.WithValue(context.Background(), ctxkey.RequestID, "encrypted-capture-test"))
	req.Header.Set("Authorization", "Bearer never-store")
	req.Header.Set("X-Api-Key", "never-store")
	req.Header.Set("Anthropic-Beta", "real-beta")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.NoError(t, svc.replay.Close())
	svc.replay = nil
	envelopes, err := replaycapture.ReadEnvelopes(dir)
	require.NoError(t, err)
	require.Len(t, envelopes, 1)
	capture, err := replaycapture.Decrypt(key, envelopes[0], time.Now())
	require.NoError(t, err)
	require.Equal(t, body, capture.Body)
	require.Equal(t, "real-beta", capture.Headers["Anthropic-Beta"])
	require.NotContains(t, capture.Headers, "Authorization")
	require.NotContains(t, capture.Headers, "X-Api-Key")
	record, err := client.QARecord.Query().Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, "real-model", record.RequestedModel)
	// Existing QA is capped while the encrypted original remains complete.
	require.Len(t, ordinary, 256*1024)
	require.NotEqual(t, body, ordinary)
}
func TestReplayIngressPreservesGETGeminiAndDropsCredentials(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"/v1/models", "/v1/models"},
		{"/v1beta/models/gemini-2.5:streamGenerateContent?key=secret&alt=sse", "/v1beta/models/gemini-2.5:streamGenerateContent?alt=sse"},
		{"/v1/models?unknown=value", ""},
	} {
		u, err := url.Parse(tc.raw)
		require.NoError(t, err)
		require.Equal(t, tc.want, replayRequestPath(u))
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1beta/models/gemini-2.5:generateContent", nil)
	require.Equal(t, "gemini-2.5", captureRequestModel(c, []byte(`{"contents":[]}`)))
}

func TestReplayAudioMultipartStaysEncryptedAndKeepsModel(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	require.NoError(t, err)
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	svc, client, _ := newQAExportTestService(t)
	dir := filepath.Join(t.TempDir(), "replay")
	svc.replay, err = replaycapture.Open(dir, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	require.NoError(t, err)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "whisper-1"))
	file, err := writer.CreateFormFile("file", "input.wav")
	require.NoError(t, err)
	_, err = file.Write([]byte("RIFF\x00private-audio-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	var ordinary []byte
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 5, UserID: 7, User: &service.User{ID: 7}})
		c.Next()
		if v, ok := c.Get(contextKeyRequestBytes); ok {
			var castOK bool
			ordinary, castOK = v.([]byte)
			require.True(t, castOK)
		}
	})
	r.Use(Middleware(svc))
	r.POST("/v1/audio/transcriptions", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, body.Bytes(), raw)
		c.Data(200, "text/plain", []byte("recognized speech"))
	})
	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body.Bytes())).WithContext(context.WithValue(context.Background(), ctxkey.RequestID, "audio-capture-test"))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.NoError(t, svc.replay.Close())
	svc.replay = nil
	envelopes, err := replaycapture.ReadEnvelopes(dir)
	require.NoError(t, err)
	require.Len(t, envelopes, 1)
	capture, err := replaycapture.Decrypt(key, envelopes[0], time.Now())
	require.NoError(t, err)
	require.Equal(t, body.Bytes(), capture.Body)
	require.Equal(t, "whisper-1", capture.Model)
	require.True(t, capture.Multimodal)
	require.Equal(t, writer.FormDataContentType(), capture.Headers["Content-Type"])
	require.NotContains(t, string(ordinary), "private-audio-bytes")
	require.Contains(t, string(ordinary), "_qa_body_omitted")
	record, err := client.QARecord.Query().Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, "whisper-1", record.RequestedModel)
	require.True(t, record.MultimodalPresent)
	_, ok := replayMultipartModel(body.Bytes(), "wrong-boundary")
	require.False(t, ok)
}
