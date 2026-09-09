package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/audio"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAudioTranscriptionMultipart(t *testing.T) {
	file, err := os.ReadFile("../pkg/audio/testdata/tts-default-24k.mp3")
	require.NoError(t, err)
	for _, format := range []string{"json", "text"} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", service.VolcEnginePlanASRModel))
		require.NoError(t, writer.WriteField("response_format", format))
		part, err := writer.CreateFormFile("file", "../../recording.mp3")
		require.NoError(t, err)
		_, err = part.Write(file)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		request, recording, err := parseAudioTranscription(body.Bytes(), writer.FormDataContentType())
		require.NoError(t, err)
		require.Equal(t, format, request.ResponseFormat)
		require.Equal(t, file, recording)
		pcm, err := audio.Normalize(context.Background(), recording)
		require.NoError(t, err)
		require.NotEmpty(t, pcm)
	}
}

func TestAudioTranscriptionRejectsInvalidRequests(t *testing.T) {
	for _, fields := range [][][2]string{
		{{"model", service.VolcEnginePlanASRModel}},
		{{"model", "wrong"}, {"file", "audio"}},
		{{"model", service.VolcEnginePlanASRModel}, {"file", "audio"}, {"response_format", "srt"}},
		{{"model", service.VolcEnginePlanASRModel}, {"file", "audio"}, {"stream", "true"}},
		{{"model", service.VolcEnginePlanASRModel}, {"file", "audio"}, {"file", "again"}},
	} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, field := range fields {
			require.NoError(t, writer.WriteField(field[0], field[1]))
		}
		require.NoError(t, writer.Close())
		_, _, err := parseAudioTranscription(body.Bytes(), writer.FormDataContentType())
		require.Error(t, err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	h := &OpenAIGatewayHandler{}
	h.AudioTranscriptions(c)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAudioTranscriptionDeadlineIsNotEmptySuccess(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil).WithContext(ctx)
	require.True(t, (&OpenAIGatewayHandler{}).audioRequestContextDone(c))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
}
