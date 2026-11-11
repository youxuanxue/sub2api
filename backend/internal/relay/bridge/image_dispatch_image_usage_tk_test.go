//go:build unit

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// End-to-end proof for the image billing handoff: DispatchImageGenerations
// against a tokensea-shaped upstream must surface the image output token count,
// which New API's dto.Usage cannot carry.
//
// The response body mirrors a real 2026-11-11 agent.tokensea.ai
// /v1/images/generations reply.
func TestDispatchImageGenerations_RecoversImageOutputTokens(t *testing.T) {
	ensureNewAPIDeps()

	const upstreamBody = `{"created":1794398461,"data":[{"b64_json":"iVBORw0KGgo="}],` +
		`"output_format":"png","quality":"low","size":"1024x1024",` +
		`"usage":{"input_tokens":12,"input_tokens_details":{"image_tokens":0,"text_tokens":12},` +
		`"output_tokens":196,"output_tokens_details":{"image_tokens":196,"text_tokens":0},` +
		`"total_tokens":208}}`

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer srv.Close()

	body, err := json.Marshal(map[string]any{
		"model":  "gpt-image-2.5-flare",
		"prompt": "a green leaf",
		"n":      1,
		"size":   "1024x1024",
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	mapping, err := json.Marshal(map[string]string{"gpt-image-2.5-flare": "gpt-image-2.5-flare"})
	require.NoError(t, err)

	out, apiErr := DispatchImageGenerations(context.Background(), c, ChannelContextInput{
		ChannelType:      newapiconstant.ChannelTypeOpenAI,
		ChannelID:        454,
		BaseURL:          srv.URL,
		APIKey:           "sk-test",
		ModelMappingJSON: string(mapping),
	}, body)
	require.Nil(t, apiErr)

	require.Equal(t, "/v1/images/generations", gotPath)
	require.NotNil(t, out)
	require.NotNil(t, out.Usage)
	require.Equal(t, 1, out.ImageCount)
	require.Contains(t, rec.Body.String(), "iVBORw0KGgo=", "client must receive the full image payload")

	require.NotNil(t, out.ImageUsage, "observed image usage must be attached to the outcome")
	require.Equal(t, 196, out.ImageUsage.ImageOutputTokens)
	require.Zero(t, out.ImageUsage.ImageInputTokens)
}
