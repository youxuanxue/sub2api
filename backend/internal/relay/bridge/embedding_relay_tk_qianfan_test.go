//go:build unit

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

func TestDispatchEmbeddings_QianfanV2_OK(t *testing.T) {
	ensureNewAPIDeps()

	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer qf-test-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],
			"model":"bge-large-en",
			"usage":{"prompt_tokens":3,"total_tokens":3}
		}`))
	}))
	defer srv.Close()

	body := mustJSONQianfanEmbeddingTest(t, map[string]any{
		"model": "bge-large-en",
		"input": "hello embedding",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	in := ChannelContextInput{
		ChannelType:      newapiconstant.ChannelTypeBaiduV2,
		ChannelID:        90,
		BaseURL:          srv.URL,
		APIKey:           "qf-test-key",
		ModelMappingJSON: string(mustJSONQianfanEmbeddingTest(t, map[string]string{"bge-large-en": "bge-large-en"})),
	}

	out, apiErr := DispatchEmbeddings(context.Background(), c, in, body)
	if apiErr != nil {
		t.Fatalf("DispatchEmbeddings returned error: %v", apiErr)
	}
	if out == nil || out.Model != "bge-large-en" {
		t.Fatalf("unexpected outcome: %#v", out)
	}
	if gotPath != "/v2/embeddings" {
		t.Fatalf("expected /v2/embeddings upstream path, got %q", gotPath)
	}
	if !bytes.Contains(gotBody, []byte("hello embedding")) {
		t.Fatalf("upstream body missing input text: %q", gotBody)
	}
	if !strings.Contains(w.Body.String(), `"object":"embedding"`) {
		t.Fatalf("response body missing OpenAI embedding object: %q", w.Body.Bytes())
	}
	if out.Usage == nil || out.Usage.PromptTokens <= 0 {
		t.Fatalf("expected positive prompt token usage, got %#v", out.Usage)
	}
}

func mustJSONQianfanEmbeddingTest(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}
