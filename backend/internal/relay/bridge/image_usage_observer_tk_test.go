package bridge

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newImageUsageTestWriter(t *testing.T) (*httptest.ResponseRecorder, *imageUsageObserver) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	return rec, &imageUsageObserver{ResponseWriter: c.Writer}
}

func TestImageUsageObserver_NonStreamingJSON(t *testing.T) {
	body := `{"created":1,"data":[{"b64_json":"AAAA"}],"usage":{"input_tokens":12,` +
		`"input_tokens_details":{"image_tokens":0,"text_tokens":12},"output_tokens":196,` +
		`"output_tokens_details":{"image_tokens":196,"text_tokens":0},"total_tokens":208}}`
	rec, obs := newImageUsageTestWriter(t)

	n, err := obs.Write([]byte(body))
	require.NoError(t, err)
	require.Equal(t, len(body), n)
	require.Equal(t, body, rec.Body.String(), "observer must not alter delivered bytes")

	details, ok := obs.Details()
	require.True(t, ok)
	require.Equal(t, 196, details.ImageOutputTokens)
	require.Equal(t, 0, details.ImageInputTokens)
	require.Equal(t, 0, details.TextOutputTokens)
}

func TestImageUsageObserver_NonStreamingChunked(t *testing.T) {
	body := []byte(`{"data":[{"b64_json":"AAAA"}],"usage":{"input_tokens":5,` +
		`"input_tokens_details":{"image_tokens":7},"output_tokens":300,` +
		`"output_tokens_details":{"image_tokens":293,"text_tokens":7},"total_tokens":305}}`)
	rec, obs := newImageUsageTestWriter(t)

	for i := 0; i < len(body); i += 7 {
		end := i + 7
		if end > len(body) {
			end = len(body)
		}
		_, err := obs.Write(body[i:end])
		require.NoError(t, err)
	}
	require.Equal(t, string(body), rec.Body.String())

	details, ok := obs.Details()
	require.True(t, ok)
	require.Equal(t, 293, details.ImageOutputTokens)
	require.Equal(t, 7, details.ImageInputTokens)
	require.Equal(t, 7, details.TextOutputTokens)
}

func TestImageUsageObserver_StreamingEvents(t *testing.T) {
	events := "" +
		"event: image_generation.partial_image\n" +
		"data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"AAAA\"}\n\n" +
		"event: image_generation.completed\n" +
		"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"BBBB\",\"usage\":{\"input_tokens\":12,\"output_tokens\":196,\"output_tokens_details\":{\"image_tokens\":196,\"text_tokens\":0}}}\n\n" +
		"data: [DONE]\n\n"
	rec, obs := newImageUsageTestWriter(t)

	_, err := obs.Write([]byte(events))
	require.NoError(t, err)
	require.Equal(t, events, rec.Body.String())

	details, ok := obs.Details()
	require.True(t, ok)
	require.Equal(t, 196, details.ImageOutputTokens)
}

// Large base64 payloads must not blow up observer memory, and the usage object
// (which follows them in the same body) must still be recovered.
func TestImageUsageObserver_LargeBodyKeepsBoundedMemory(t *testing.T) {
	payload := bytes.Repeat([]byte("Q"), 5<<20)
	var body bytes.Buffer
	_, _ = body.WriteString(`{"data":[{"b64_json":"`)
	_, _ = body.Write(payload)
	_, _ = body.WriteString(`"}],"usage":{"input_tokens":12,"output_tokens":196,`)
	_, _ = body.WriteString(`"output_tokens_details":{"image_tokens":196}}}`)

	rec, obs := newImageUsageTestWriter(t)
	_, err := obs.Write(body.Bytes())
	require.NoError(t, err)
	require.Equal(t, body.Len(), rec.Body.Len())

	require.LessOrEqual(t, len(obs.head), maxImageUsageCaptureBytes)
	require.LessOrEqual(t, len(obs.tail), maxImageUsageCaptureBytes)

	details, ok := obs.Details()
	require.True(t, ok)
	require.Equal(t, 196, details.ImageOutputTokens)
}

func TestImageUsageObserver_NoUsage(t *testing.T) {
	_, obs := newImageUsageTestWriter(t)
	_, err := obs.Write([]byte(`{"data":[{"b64_json":"AAAA"}]}`))
	require.NoError(t, err)

	_, ok := obs.Details()
	require.False(t, ok)
}

func TestImageUsageObserver_ErrorBodyIsNotUsage(t *testing.T) {
	_, obs := newImageUsageTestWriter(t)
	_, err := obs.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
	require.NoError(t, err)

	_, ok := obs.Details()
	require.False(t, ok)
}
