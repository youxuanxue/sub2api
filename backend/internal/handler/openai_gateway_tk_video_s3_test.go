//go:build unit

package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// fakeMediaStore records uploads and returns deterministic presigned URLs.
type fakeMediaStore struct {
	uploads    map[string][]byte
	presignErr error
	uploadErr  error
}

func newFakeMediaStore() *fakeMediaStore { return &fakeMediaStore{uploads: map[string][]byte{}} }

func (f *fakeMediaStore) Upload(_ context.Context, key string, body []byte, _ string) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	f.uploads[key] = append([]byte(nil), body...)
	return nil
}

func (f *fakeMediaStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	if f.presignErr != nil {
		return "", f.presignErr
	}
	return "https://s3.example.test/" + key, nil
}

// compile-time: fakeMediaStore satisfies the real interface.
var _ service.MediaStore = (*fakeMediaStore)(nil)

func veoSuccessBody(b64, mime string) []byte {
	return []byte(`{"done":true,"response":{"videos":[{"bytesBase64Encoded":"` + b64 + `","mimeType":"` + mime + `"}]}}`)
}

func TestExtractInlineVideoBase64(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("FAKEMP4"))
	got, mime := extractInlineVideoBase64(veoSuccessBody(b64, "video/mp4"))
	if got != b64 || mime != "video/mp4" {
		t.Fatalf("veo shape: got (%q,%q) want (%q,video/mp4)", got, mime, b64)
	}
	got, _ = extractInlineVideoBase64([]byte(`{"done":true,"response":{"bytesBase64Encoded":"` + b64 + `"}}`))
	if got != b64 {
		t.Fatalf("flat shape: got %q want %q", got, b64)
	}
	if got, _ := extractInlineVideoBase64([]byte(`{"status":"succeeded","content":{"video_url":"https://x/y.mp4"}}`)); got != "" {
		t.Fatalf("url shape must yield empty base64, got %q", got)
	}
}

func TestRewriteVideoBodyWithURL(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("FAKEMP4"))
	url := "https://s3.example.test/media/videos/vt_x.mp4"
	t.Run("inline base64 is stripped and top-level URL is set", func(t *testing.T) {
		r := gjson.ParseBytes(rewriteVideoBodyWithURL(veoSuccessBody(b64, "video/mp4"), url, "media/videos/vt_x.mp4"))
		// base64 must be GONE (else extractVideoUrl returns a now-empty data: URI).
		if r.Get("response.videos").Exists() {
			t.Fatalf("response.videos not stripped: %s", r.Raw)
		}
		if r.Get("video_url").String() != url {
			t.Fatalf("video_url not set: %s", r.Raw)
		}
		if !r.Get("done").Bool() {
			t.Fatalf("done flag lost (videoStateFromFetch would misclassify): %s", r.Raw)
		}
		if r.Get("s3_key").String() != "media/videos/vt_x.mp4" {
			t.Fatalf("s3_key not set: %s", r.Raw)
		}
	})

	t.Run("known nested URL fields are replaced", func(t *testing.T) {
		body := rewriteVideoBodyWithURL([]byte(`{"done":true,"content":{"video_url":"https://provider.example/a.mp4"},"data":{"video_url":"https://provider.example/b.mp4","url":"https://provider.example/c.mp4"}}`), url, "media/videos/vt_x.mp4")
		r := gjson.ParseBytes(body)
		for _, p := range []string{"content.video_url", "data.video_url", "data.url", "video_url"} {
			if r.Get(p).String() != url {
				t.Fatalf("%s not rewritten to TokenKey URL: %s", p, r.Raw)
			}
		}
		if r.Get("s3_key").String() != "media/videos/vt_x.mp4" {
			t.Fatalf("s3_key not set: %s", r.Raw)
		}
	})
}

func TestVideoExtAndContentType(t *testing.T) {
	cases := map[string][2]string{ // mime -> {ext, contentType}
		"video/mp4":       {".mp4", "video/mp4"},
		"video/webm":      {".webm", "video/webm"},
		"video/quicktime": {".mov", "video/quicktime"},
		"":                {".mp4", "video/mp4"},
		"text/html":       {".mp4", "video/mp4"}, // non-video falls back
	}
	for mime, want := range cases {
		if e := videoExtForMime(mime); e != want[0] {
			t.Errorf("videoExtForMime(%q)=%q want %q", mime, e, want[0])
		}
		if ct := mediaContentType(mime); ct != want[1] {
			t.Errorf("mediaContentType(%q)=%q want %q", mime, ct, want[1])
		}
	}
}

func newRecord(taskID, s3key string) *service.VideoTaskRecord {
	return &service.VideoTaskRecord{PublicTaskID: taskID, UpstreamTaskID: "u-" + taskID, UserID: 1, ChannelType: 41, MediaS3Key: s3key}
}

func TestMaybeOffloadVideoToS3(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("FAKEMP4"))
	mk := func(store service.MediaStore) *OpenAIGatewayHandler {
		h := &OpenAIGatewayHandler{}
		h.SetVideoTaskCache(repository.NewVideoTaskCache(nil))
		h.SetMediaStore(store)
		return h
	}
	out := func(status string, body []byte) *bridge.VideoFetchOutcome {
		return &bridge.VideoFetchOutcome{Status: status, RawResponse: body}
	}

	t.Run("nil store passes through", func(t *testing.T) {
		h := &OpenAIGatewayHandler{}
		h.SetVideoTaskCache(repository.NewVideoTaskCache(nil))
		if _, ok := h.tkMaybeOffloadVideoToS3(context.Background(), newRecord("vt_a", ""), out("success", veoSuccessBody(b64, "video/mp4"))); ok {
			t.Fatal("nil store must not offload")
		}
	})

	t.Run("success uploads, rewrites, stores key", func(t *testing.T) {
		fs := newFakeMediaStore()
		h := mk(fs)
		rec := newRecord("vt_b", "")
		body, ok := h.tkMaybeOffloadVideoToS3(context.Background(), rec, out("success", veoSuccessBody(b64, "video/mp4")))
		if !ok {
			t.Fatal("expected offload")
		}
		wantKey := "media/videos/vt_b.mp4"
		if rec.MediaS3Key != wantKey {
			t.Fatalf("rec.MediaS3Key=%q want %q", rec.MediaS3Key, wantKey)
		}
		if string(fs.uploads[wantKey]) != "FAKEMP4" {
			t.Fatalf("uploaded bytes=%q want FAKEMP4", fs.uploads[wantKey])
		}
		if gjson.GetBytes(body, "video_url").String() != "https://s3.example.test/"+wantKey {
			t.Fatalf("rewritten body missing presigned url: %s", body)
		}
	})

	t.Run("non-success does not offload", func(t *testing.T) {
		fs := newFakeMediaStore()
		if _, ok := mk(fs).tkMaybeOffloadVideoToS3(context.Background(), newRecord("vt_c", ""), out("processing", veoSuccessBody(b64, "video/mp4"))); ok {
			t.Fatal("processing must not offload")
		}
		if _, ok := mk(fs).tkMaybeOffloadVideoToS3(context.Background(), newRecord("vt_d", ""), out("failure", veoSuccessBody(b64, "video/mp4"))); ok {
			t.Fatal("failure must not offload")
		}
		if len(fs.uploads) != 0 {
			t.Fatalf("no upload expected, got %d", len(fs.uploads))
		}
	})

	t.Run("upstream-url body downloads and offloads", func(t *testing.T) {
		prev := downloadPublicVideoURL
		downloadPublicVideoURL = func(_ context.Context, raw string, _ int64, _ time.Duration) (*pkghttputil.PublicURLDownload, error) {
			if raw != "https://x/y.mp4" {
				t.Fatalf("download url=%q want https://x/y.mp4", raw)
			}
			return &pkghttputil.PublicURLDownload{Body: []byte("URLMP4"), ContentType: "video/mp4"}, nil
		}
		defer func() { downloadPublicVideoURL = prev }()

		fs := newFakeMediaStore()
		rec := newRecord("vt_e", "")
		body, ok := mk(fs).tkMaybeOffloadVideoToS3(context.Background(), rec, out("success", []byte(`{"status":"succeeded","content":{"video_url":"https://x/y.mp4"}}`)))
		if !ok {
			t.Fatal("url-shaped body should offload while upstream URL is fresh")
		}
		wantKey := "media/videos/vt_e.mp4"
		if rec.MediaS3Key != wantKey {
			t.Fatalf("rec.MediaS3Key=%q want %q", rec.MediaS3Key, wantKey)
		}
		if string(fs.uploads[wantKey]) != "URLMP4" {
			t.Fatalf("uploaded bytes=%q want URLMP4", fs.uploads[wantKey])
		}
		if gjson.GetBytes(body, "content.video_url").String() != "https://s3.example.test/"+wantKey {
			t.Fatalf("nested provider URL should be replaced with TokenKey URL: %s", body)
		}
		if gjson.GetBytes(body, "video_url").String() != "https://s3.example.test/"+wantKey {
			t.Fatalf("rewritten body missing TokenKey URL: %s", body)
		}
		if gjson.GetBytes(body, "s3_key").String() != wantKey {
			t.Fatalf("rewritten body missing s3_key: %s", body)
		}
	})

	t.Run("upstream-url download failure degrades to passthrough", func(t *testing.T) {
		prev := downloadPublicVideoURL
		downloadPublicVideoURL = func(context.Context, string, int64, time.Duration) (*pkghttputil.PublicURLDownload, error) {
			return nil, errors.New("expired")
		}
		defer func() { downloadPublicVideoURL = prev }()

		fs := newFakeMediaStore()
		if _, ok := mk(fs).tkMaybeOffloadVideoToS3(context.Background(), newRecord("vt_url_fail", ""), out("success", []byte(`{"status":"succeeded","content":{"video_url":"https://x/y.mp4"}}`))); ok {
			t.Fatal("download error must degrade to passthrough (ok=false)")
		}
		if len(fs.uploads) != 0 {
			t.Fatalf("no upload expected, got %d", len(fs.uploads))
		}
	})

	t.Run("upload failure degrades to passthrough", func(t *testing.T) {
		fs := newFakeMediaStore()
		fs.uploadErr = context.DeadlineExceeded
		if _, ok := mk(fs).tkMaybeOffloadVideoToS3(context.Background(), newRecord("vt_f", ""), out("success", veoSuccessBody(b64, "video/mp4"))); ok {
			t.Fatal("upload error must degrade to passthrough (ok=false)")
		}
	})
}

func TestVideoFastPathFromS3(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func() (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/vt_x", nil)
		return c, w
	}

	t.Run("hits when key present", func(t *testing.T) {
		h := &OpenAIGatewayHandler{}
		h.SetMediaStore(newFakeMediaStore())
		c, w := newCtx()
		if !h.tkVideoFastPathFromS3(c, newRecord("vt_x", "media/videos/vt_x.mp4")) {
			t.Fatal("expected fast path to handle")
		}
		r := gjson.ParseBytes(w.Body.Bytes())
		if !r.Get("done").Bool() || r.Get("video_url").String() != "https://s3.example.test/media/videos/vt_x.mp4" || r.Get("s3_key").String() != "media/videos/vt_x.mp4" {
			t.Fatalf("fast-path body wrong: %s", w.Body.String())
		}
	})

	t.Run("skips when no key or no store", func(t *testing.T) {
		c, _ := newCtx()
		hNoStore := &OpenAIGatewayHandler{}
		if hNoStore.tkVideoFastPathFromS3(c, newRecord("vt_x", "media/videos/vt_x.mp4")) {
			t.Fatal("no store → must not handle")
		}
		hNoKey := &OpenAIGatewayHandler{}
		hNoKey.SetMediaStore(newFakeMediaStore())
		if hNoKey.tkVideoFastPathFromS3(c, newRecord("vt_x", "")) {
			t.Fatal("empty key → must not handle")
		}
	})

	t.Run("presign error falls back to upstream path", func(t *testing.T) {
		fs := newFakeMediaStore()
		fs.presignErr = context.DeadlineExceeded
		h := &OpenAIGatewayHandler{}
		h.SetMediaStore(fs)
		c, _ := newCtx()
		if h.tkVideoFastPathFromS3(c, newRecord("vt_x", "media/videos/vt_x.mp4")) {
			t.Fatal("presign error → must not claim handled")
		}
	})
}
