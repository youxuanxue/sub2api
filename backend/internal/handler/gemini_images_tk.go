package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GeminiImageGenerations is a non-streaming OpenAI Images facade over the same
// native handler, authorization, Plan, scheduling and billing lifecycle.
func (h *GatewayHandler) GeminiImageGenerations(c *gin.Context) {
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Unable to read image request"}})
		return
	}
	model, native, err := service.PrepareGeminiImagesRequest(body)
	if err != nil || !service.UsesGeminiImagesAdapter(service.ShapeOpenAIImages, model) {
		message := "model is not a Gemini image model"
		if err != nil {
			message = err.Error()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(native))
	c.Request.ContentLength = int64(len(native))
	c.Request.Header.Del("Content-Encoding")
	c.Request.Header.Del("Content-Length")
	params := c.Params
	c.Params = append(append(gin.Params(nil), params...), gin.Param{Key: "modelAction", Value: model + ":generateContent"})
	original := c.Writer
	buffered := &geminiImagesWriter{ResponseWriter: original, headers: original.Header().Clone(), status: http.StatusOK}
	c.Writer = buffered
	defer func() { c.Writer = original; c.Params = params }()
	h.GeminiV1BetaModels(c)
	c.Writer = original
	writeGeminiImagesResponse(c, buffered)
}

// Buffer only this non-streaming facade; Flush must not commit the native
// response before it has been converted to the Images envelope.
type geminiImagesWriter struct {
	gin.ResponseWriter
	headers   http.Header
	body      bytes.Buffer
	status    int
	written   bool
	overflow  bool
	converted []byte
}

const maxGeminiImagesResponseBytes = 32 << 20

func (w *geminiImagesWriter) Header() http.Header { return w.headers }
func (w *geminiImagesWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
	}
}
func (w *geminiImagesWriter) WriteHeaderNow() { w.written = true }
func (w *geminiImagesWriter) Write(b []byte) (int, error) {
	w.written = true
	if w.body.Len()+len(b) > maxGeminiImagesResponseBytes {
		w.overflow = true
		return 0, io.ErrShortBuffer
	}
	return w.body.Write(b)
}
func (w *geminiImagesWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *geminiImagesWriter) Flush()                            { w.WriteHeaderNow() }
func (w *geminiImagesWriter) Status() int                       { return w.status }
func (w *geminiImagesWriter) Size() int {
	if !w.written {
		return -1
	}
	return w.body.Len()
}
func (w *geminiImagesWriter) Written() bool { return w.written }

func writeGeminiImagesResponse(c *gin.Context, w *geminiImagesWriter) {
	if requestID := w.headers.Get("X-Request-Id"); requestID != "" {
		c.Header("X-Request-Id", requestID)
	}
	// Native errors can be Google-shaped; expose the OpenAI error contract.
	if w.status >= 400 {
		message := http.StatusText(w.status)
		var upstream struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(w.body.Bytes(), &upstream) == nil && upstream.Error.Message != "" {
			message = upstream.Error.Message
		}
		if retry := w.headers.Get("Retry-After"); retry != "" {
			c.Header("Retry-After", retry)
		}
		errorType := "api_error"
		switch w.status {
		case http.StatusBadRequest:
			errorType = "invalid_request_error"
		case http.StatusUnauthorized:
			errorType = "authentication_error"
		case http.StatusForbidden:
			errorType = "permission_error"
		case http.StatusTooManyRequests:
			errorType = "rate_limit_error"
		}
		c.JSON(w.status, gin.H{"error": gin.H{"type": errorType, "message": message}})
		return
	}
	if err := w.prepareSuccess(); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "Upstream did not return a complete image"}})
		return
	}
	c.Header("Content-Length", "")
	c.Data(http.StatusOK, "application/json", w.converted)
}

// Called before native usage settlement. A buffered response which cannot be
// delivered in the requested protocol must not settle or trigger another image.
func (w *geminiImagesWriter) prepareSuccess() error {
	if w.overflow {
		return io.ErrShortBuffer
	}
	if w.converted != nil {
		return nil
	}
	result, err := apicompat.GeminiGenerationToImages(w.body.Bytes())
	if err != nil {
		return err
	}
	w.converted = result
	return nil
}
