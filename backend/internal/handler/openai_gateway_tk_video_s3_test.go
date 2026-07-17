//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

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

var _ service.MediaStore = (*fakeMediaStore)(nil)

func newRecord(taskID, s3key string) *service.VideoTaskRecord {
	return &service.VideoTaskRecord{PublicTaskID: taskID, UpstreamTaskID: "u-" + taskID, UserID: 1, ChannelType: 41, MediaS3Key: s3key}
}

func TestVideoFastPathFromS3(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func() (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/vt_x", nil)
		return c, w
	}

	t.Run("hits legacy offloaded record when key present", func(t *testing.T) {
		h := &OpenAIGatewayHandler{}
		h.SetMediaStore(newFakeMediaStore())
		c, w := newCtx()
		if !h.tkVideoFastPathFromS3(c, newRecord("vt_x", "media/videos/vt_x.mp4")) {
			t.Fatal("expected fast path to handle")
		}
		r := gjson.ParseBytes(w.Body.Bytes())
		if !r.Get("done").Bool() || r.Get("video_url").String() != "https://s3.example.test/media/videos/vt_x.mp4" {
			t.Fatalf("fast-path body wrong: %s", w.Body.String())
		}
	})

	t.Run("skips when no key or no store", func(t *testing.T) {
		c, _ := newCtx()
		hNoStore := &OpenAIGatewayHandler{}
		if hNoStore.tkVideoFastPathFromS3(c, newRecord("vt_x", "media/videos/vt_x.mp4")) {
			t.Fatal("no store must not handle")
		}
		hNoKey := &OpenAIGatewayHandler{}
		hNoKey.SetMediaStore(newFakeMediaStore())
		if hNoKey.tkVideoFastPathFromS3(c, newRecord("vt_x", "")) {
			t.Fatal("empty key must not handle")
		}
	})

	t.Run("presign error falls back to upstream path", func(t *testing.T) {
		fs := newFakeMediaStore()
		fs.presignErr = context.DeadlineExceeded
		h := &OpenAIGatewayHandler{}
		h.SetMediaStore(fs)
		c, _ := newCtx()
		if h.tkVideoFastPathFromS3(c, newRecord("vt_x", "media/videos/vt_x.mp4")) {
			t.Fatal("presign error must not claim handled")
		}
	})
}
