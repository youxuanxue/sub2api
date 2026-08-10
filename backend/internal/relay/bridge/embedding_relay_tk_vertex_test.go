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
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/channel/vertex"
	"github.com/gin-gonic/gin"
)

func TestVertexEmbeddingPromptTokens_SingleAndBatch(t *testing.T) {
	t.Parallel()

	single := vertexEmbeddingPromptTokens([]string{"hello embedding"}, "gemini-embedding-001")
	if single <= 0 {
		t.Fatalf("single prompt tokens = %d, want > 0", single)
	}

	batch := vertexEmbeddingPromptTokens([]string{"hello", "world"}, "gemini-embedding-001")
	if batch <= single {
		t.Fatalf("batch prompt tokens = %d, want > single %d", batch, single)
	}
}

func TestBuildVertexEmbeddingPredictPayload_SingleAndBatch(t *testing.T) {
	t.Parallel()

	single, err := buildVertexEmbeddingPredictPayload([]string{"hello"}, nil)
	if err != nil {
		t.Fatalf("build single payload: %v", err)
	}
	instances, ok := single["instances"].([]map[string]any)
	if !ok {
		t.Fatalf("instances type = %T", single["instances"])
	}
	if len(instances) != 1 {
		t.Fatalf("instances len = %d, want 1", len(instances))
	}
	if instances[0]["content"] != "hello" || instances[0]["task_type"] != "RETRIEVAL_QUERY" {
		t.Fatalf("unexpected instance: %#v", instances[0])
	}
	if _, hasParams := single["parameters"]; hasParams {
		t.Fatal("expected no parameters without dimensions")
	}

	dims := 768
	batch, err := buildVertexEmbeddingPredictPayload([]string{"a", "b"}, &dims)
	if err != nil {
		t.Fatalf("build batch payload: %v", err)
	}
	batchInstances, ok := batch["instances"].([]map[string]any)
	if !ok || len(batchInstances) != 2 {
		t.Fatalf("batch instances = %#v", batch["instances"])
	}
	params, ok := batch["parameters"].(map[string]any)
	if !ok || params["outputDimensionality"] != 768 {
		t.Fatalf("parameters = %#v", batch["parameters"])
	}
}

func TestBuildVertexEmbeddingPredictPayload_RejectsEmptyInput(t *testing.T) {
	t.Parallel()
	if _, err := buildVertexEmbeddingPredictPayload(nil, nil); err == nil {
		t.Fatal("expected error for empty inputs")
	}
	if _, err := buildVertexEmbeddingPredictPayload([]string{" "}, nil); err == nil {
		t.Fatal("expected error for blank input string")
	}
}

func TestVertexEmbeddingPredictResponseToOpenAI_OK(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2,0.3]}},{"embeddings":{"values":[0.4,0.5]}}]}`)
	resp, usage, err := vertexEmbeddingPredictResponseToOpenAI(c, "gemini-embedding-001", body, 12)
	if err != nil {
		t.Fatalf("convert response: %v", err)
	}
	if len(resp.Data) != 2 || resp.Model != "gemini-embedding-001" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if usage == nil || usage.PromptTokens == 0 {
		t.Fatalf("expected usage with prompt tokens, got %#v", usage)
	}
}

func TestVertexEmbeddingPredictResponseToOpenAI_EmptyPredictions(t *testing.T) {
	t.Parallel()
	_, _, err := vertexEmbeddingPredictResponseToOpenAI(nil, "gemini-embedding-001", []byte(`{"predictions":[]}`), 1)
	if err == nil {
		t.Fatal("expected error for empty predictions")
	}
}

func TestRunVertexEmbeddingRelay_InvalidCredentials(t *testing.T) {
	t.Parallel()
	ensureNewAPIDeps()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"gemini-embedding-001","input":"hi"}`))

	info := newVertexEmbeddingRelayInfo(`{not-json`, "gemini-embedding-001")

	_, apiErr := runVertexEmbeddingRelay(c, info, info.Request.(*dto.EmbeddingRequest))
	if apiErr == nil {
		t.Fatal("expected invalid credentials error")
	}
}

func TestDispatchEmbeddings_VertexPredict_OK(t *testing.T) {
	ensureNewAPIDeps()

	const saJSON = `{
		"project_id":"proj-test",
		"private_key_id":"key-id",
		"private_key":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\n-----END PRIVATE KEY-----\n",
		"client_email":"vertex@test.iam.gserviceaccount.com",
		"client_id":"123"
	}`

	var gotPredictBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":predict") {
			http.NotFound(w, r)
			return
		}
		gotPredictBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"embeddings":{"values":[0.1,0.2,0.3]}}]}`))
	}))
	defer srv.Close()

	oldTokenFn := vertexEmbeddingAcquireAccessToken
	vertexEmbeddingAcquireAccessToken = func(_ vertex.Credentials, _ string) (string, error) {
		return "tok-test", nil
	}
	defer func() { vertexEmbeddingAcquireAccessToken = oldTokenFn }()

	body := mustJSONVertexTest(t, map[string]any{
		"model": "gemini-embedding-001",
		"input": "hello embedding",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	in := ChannelContextInput{
		ChannelType:      newapiconstant.ChannelTypeVertexAi,
		ChannelID:        47,
		BaseURL:          srv.URL,
		APIKey:           saJSON,
		VertexKeyType:    "json",
		VertexLocation:   "us-central1",
		ModelMappingJSON: string(mustJSONVertexTest(t, map[string]string{"gemini-embedding-001": "gemini-embedding-001"})),
	}

	out, apiErr := DispatchEmbeddings(context.Background(), c, in, body)
	if apiErr != nil {
		t.Fatalf("DispatchEmbeddings returned error: %v", apiErr)
	}
	if out == nil || out.Model != "gemini-embedding-001" {
		t.Fatalf("unexpected outcome: %#v", out)
	}
	if !bytes.Contains(gotPredictBody, []byte("hello embedding")) {
		t.Fatalf("predict body missing input text: %q", gotPredictBody)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"object":"embedding"`)) {
		t.Fatalf("response body missing OpenAI embedding object: %q", w.Body.Bytes())
	}
	if out.Usage == nil || out.Usage.PromptTokens <= 0 {
		t.Fatalf("expected positive prompt token usage, got %#v", out.Usage)
	}
}

func mustJSONVertexTest(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}

func newVertexEmbeddingRelayInfo(saJSON, model string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		Request: &dto.EmbeddingRequest{Model: model, Input: "hi"},
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       newapiconstant.ChannelTypeVertexAi,
			ApiKey:            saJSON,
			UpstreamModelName: model,
			ApiVersion:        "us-central1",
		},
	}
}
