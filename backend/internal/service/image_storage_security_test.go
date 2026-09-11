package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageUploaderRejectsPrivateURLAndNonImageContent(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("SYNTHETIC_INTERNAL_TEXT"))
	}))
	defer server.Close()
	storage := &fakeImageStorage{}
	raw, err := json.Marshal(map[string]any{"data": []any{map[string]string{"url": server.URL}}})
	require.NoError(t, err)
	uploader := NewImageResultUploader(storage, "images/", 1024, nil)
	_, err = uploader.Rewrite(context.Background(), "test", raw)
	require.ErrorContains(t, err, "non-public")
	require.Zero(t, hits)
	require.Empty(t, storage.saved)
	// An explicitly injected test client permits loopback, but must still reject
	// text even when the server falsely declares an image Content-Type.
	uploader = NewImageResultUploader(storage, "images/", 1024, server.Client())
	_, err = uploader.Rewrite(context.Background(), "test", raw)
	require.ErrorContains(t, err, "not a supported image")
	require.Equal(t, 1, hits)
	require.Empty(t, storage.saved)
}
