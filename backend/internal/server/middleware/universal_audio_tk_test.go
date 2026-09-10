package middleware

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUniversalAudioTranscriptionPeeksMultipartAndPreservesUpload(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "recording.wav")
	require.NoError(t, err)
	_, err = part.Write([]byte("RIFF binary recording"))
	require.NoError(t, err)
	require.NoError(t, w.WriteField("model", service.VolcEnginePlanASRModel))
	require.NoError(t, w.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	require.Equal(t, service.VolcEnginePlanASRModel, peekUniversalModel(c, service.ShapeOpenAIAudioTranscription))
	remaining, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, body.Bytes(), remaining)
}
