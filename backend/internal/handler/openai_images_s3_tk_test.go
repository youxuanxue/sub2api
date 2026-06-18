//go:build unit

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// callImagesPresign drives the handler with a JSON body and returns the recorder.
func callImagesPresign(h *OpenAIGatewayHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/presign", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ImagesPresign(c)
	return w
}

func TestImagesPresign_DisabledWhenNoStore(t *testing.T) {
	h := &OpenAIGatewayHandler{} // no SetMediaStore → nil
	w := callImagesPresign(h, `{"key":"media/images/abc.png"}`)
	// 503 (feature off), NOT 404 — the route exists, so it must stay distinct from
	// gin's route-not-found 404 (TestGatewayRoutesImagePresignPathsAreRegistered).
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store should 503, got %d", w.Code)
	}
}

func TestImagesPresign_RemintsValidKey(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	h.SetMediaStore(newFakeMediaStore())
	w := callImagesPresign(h, `{"key":"media/images/abc.png"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("valid key should 200, got %d (%s)", w.Code, w.Body.String())
	}
	if got := gjson.GetBytes(w.Body.Bytes(), "url").String(); got != "https://s3.example.test/media/images/abc.png" {
		t.Errorf("unexpected presigned url: %q", got)
	}
}

func TestImagesPresign_RejectsOutOfScopeKeys(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	h.SetMediaStore(newFakeMediaStore())
	for _, body := range []string{
		`{"key":""}`,                         // empty
		`{"key":"media/videos/x.mp4"}`,       // wrong prefix (no cross-namespace presign)
		`{"key":"secrets/db.sql"}`,           // arbitrary object
		`{"key":"media/images/../secret"}`,   // traversal
		`not json`,                           // malformed body
	} {
		w := callImagesPresign(h, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q should 400, got %d", body, w.Code)
		}
	}
}

func TestImagesPresign_PresignErrorIsBadGateway(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	fs := newFakeMediaStore()
	fs.presignErr = errors.New("sts down")
	h.SetMediaStore(fs)
	w := callImagesPresign(h, `{"key":"media/images/abc.png"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("presign error should 502, got %d", w.Code)
	}
}
