package qa

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQALazyRequestCapturePreservesDownstreamAndBoundsDecompression(t *testing.T) {
	payload := bytes.Repeat([]byte("private input "), 8192)
	r := &requestCaptureReader{ReadCloser: io.NopCloser(bytes.NewReader(payload)), limit: 32}
	require.Zero(t, r.body.Len(), "wrapping a rejected request must not read it")
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, payload, got)
	require.Equal(t, payload[:32], r.body.Bytes())

	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	_, err = w.Write(payload)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Content-Encoding", "gzip")
	require.Equal(t, payload[:32], qaRequestCaptureBytes(req, compressed.Bytes(), 32))
}

func TestQACaptureBoundsAllStreamBytesWithoutTruncatingForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		t.Run(contentType, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Header("Content-Type", contentType)
			w := newTeeResponseWriter(c.Writer, 32)
			input := bytes.Repeat([]byte("data: x\n\n"), 8192)
			for _, part := range [][]byte{input[:7], input[7:19], input[19:]} {
				n, err := w.Write(part)
				require.NoError(t, err)
				require.Equal(t, len(part), n)
			}
			body, chunks, truncated := w.snapshot()
			require.Len(t, body, 32)
			require.True(t, truncated)
			require.Equal(t, input, recorder.Body.Bytes())
			retained := 0
			for _, chunk := range chunks {
				retained += len(chunk.Bytes)
			}
			require.LessOrEqual(t, retained, 32)
			if contentType == "application/json" {
				require.Empty(t, chunks)
			} else {
				require.NotEmpty(t, chunks)
				require.Equal(t, []byte("data: x\n\n"), chunks[0].Bytes)
			}
		})
	}
}

func TestQACaptureBoundsIncompleteAndTinyFrames(t *testing.T) {
	for _, data := range []string{strings.Repeat("x", 1<<18), strings.Repeat("\n\n", 10000)} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Header("Content-Type", "text/event-stream")
		w := newTeeResponseWriter(c.Writer, 16384)
		w.capture([]byte(data))
		body, chunks, truncated := w.snapshot()
		require.Len(t, body, 16384)
		require.LessOrEqual(t, len(chunks), maxCapturedSSEChunks)
		require.True(t, truncated)
	}
}

func TestQABlobStoreConfinesReadsAndDeletes(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "blobs")
	require.NoError(t, os.Mkdir(root, 0o700))
	outside := filepath.Join(outer, "evidence")
	require.NoError(t, os.WriteFile(outside, []byte("other tenant"), 0o600))
	require.NoError(t, os.Symlink(outer, filepath.Join(root, "link")))
	store := newLocalFSBlobStore(root)
	for _, key := range []string{"../evidence", "link/evidence", outside} {
		_, err := store.Get(context.Background(), key)
		require.Error(t, err)
		require.Error(t, store.Delete(context.Background(), key))
	}
	body, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "other tenant", string(body))
	_, err = store.PutReader(context.Background(), "safe", strings.NewReader("ok"), "text/plain")
	require.NoError(t, err)
	require.NoError(t, store.Delete(context.Background(), filepath.Join(root, "safe")))
}
