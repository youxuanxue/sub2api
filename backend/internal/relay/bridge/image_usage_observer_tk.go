package bridge

import (
	"bytes"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// maxImageUsageCaptureBytes bounds what imageUsageObserver keeps in memory.
// Images API responses carry base64 payloads that can reach many MB; usage
// lives in the same JSON object, so the observer retains only a bounded
// window instead of the full body.
const maxImageUsageCaptureBytes = 64 << 10

// ImageUsageDetails is the subset of the OpenAI Images usage shape that New
// API's dto.Usage drops: dto.Usage has no output_tokens_details, so
// output_tokens_details.image_tokens never reaches TokenKey billing. Without
// it, token-priced image owners (gpt-image-2 / 2.5 / 2.6) fail closed and the
// request is served unbilled.
type ImageUsageDetails struct {
	ImageInputTokens  int
	ImageOutputTokens int
	TextOutputTokens  int
}

// imageUsageObserver tees the adaptor's client-facing writes for
// /v1/images/generations and recovers the image token breakdown from the
// upstream usage object. It never alters the bytes sent to the client.
//
// Non-streaming JSON: usage is normally the last top-level key, so the
// observer keeps the head and a rolling tail of the body. Streaming
// (image_generation.completed events): each event carries its own usage; the
// observer parses complete SSE data lines and keeps the latest usage seen.
type imageUsageObserver struct {
	gin.ResponseWriter
	mu      sync.Mutex
	head    []byte
	tail    []byte
	total   int
	lineBuf bytes.Buffer
	sse     *ImageUsageDetails
}

func (w *imageUsageObserver) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.observe(p)
	w.mu.Unlock()
	return w.ResponseWriter.Write(p)
}

func (w *imageUsageObserver) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *imageUsageObserver) observe(p []byte) {
	if len(p) == 0 {
		return
	}
	w.total += len(p)
	if room := maxImageUsageCaptureBytes - len(w.head); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		w.head = append(w.head, p[:room]...)
	}
	w.tail = append(w.tail, p...)
	if len(w.tail) > maxImageUsageCaptureBytes {
		w.tail = append(w.tail[:0], w.tail[len(w.tail)-maxImageUsageCaptureBytes:]...)
	}
	w.observeSSE(p)
}

// observeSSE scans complete "data:" lines. Lines longer than the capture bound
// (base64 image payloads) are discarded rather than buffered.
func (w *imageUsageObserver) observeSSE(p []byte) {
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			if w.lineBuf.Len()+len(p) <= maxImageUsageCaptureBytes {
				_, _ = w.lineBuf.Write(p)
			} else {
				w.lineBuf.Reset()
			}
			return
		}
		if w.lineBuf.Len()+idx <= maxImageUsageCaptureBytes {
			_, _ = w.lineBuf.Write(p[:idx])
			w.processSSELine(w.lineBuf.Bytes())
		}
		w.lineBuf.Reset()
		p = p[idx+1:]
	}
}

func (w *imageUsageObserver) processSSELine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return
	}
	if d, ok := imageUsageDetailsFromUsage(gjson.GetBytes(payload, "usage")); ok {
		w.sse = &d
	}
}

// Details returns the image token breakdown observed on the client-facing
// response, or ok=false when no usage object carried image tokens.
func (w *imageUsageObserver) Details() (ImageUsageDetails, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lineBuf.Len() > 0 {
		w.processSSELine(w.lineBuf.Bytes())
		w.lineBuf.Reset()
	}
	if w.sse != nil {
		return *w.sse, true
	}
	var body []byte
	if w.total <= maxImageUsageCaptureBytes {
		body = w.head
	} else {
		body = w.tail
	}
	return imageUsageDetailsFromJSONFragment(body)
}

// imageUsageDetailsFromJSONFragment reads usage from a complete JSON body, or
// from the tail fragment of one when the body exceeded the capture window.
func imageUsageDetailsFromJSONFragment(body []byte) (ImageUsageDetails, bool) {
	if len(body) == 0 {
		return ImageUsageDetails{}, false
	}
	if gjson.ValidBytes(body) {
		return imageUsageDetailsFromUsage(gjson.GetBytes(body, "usage"))
	}
	idx := bytes.LastIndex(body, []byte(`"usage"`))
	if idx < 0 {
		return ImageUsageDetails{}, false
	}
	rest := body[idx+len(`"usage"`):]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return ImageUsageDetails{}, false
	}
	value := gjson.ParseBytes(bytes.TrimSpace(rest[colon+1:]))
	return imageUsageDetailsFromUsage(value)
}

func imageUsageDetailsFromUsage(usage gjson.Result) (ImageUsageDetails, bool) {
	if !usage.Exists() || !usage.IsObject() {
		return ImageUsageDetails{}, false
	}
	d := ImageUsageDetails{
		ImageInputTokens:  int(firstPositive(usage.Get("input_tokens_details.image_tokens"), usage.Get("prompt_tokens_details.image_tokens"))),
		ImageOutputTokens: int(firstPositive(usage.Get("output_tokens_details.image_tokens"), usage.Get("completion_tokens_details.image_tokens"))),
		TextOutputTokens:  int(firstPositive(usage.Get("output_tokens_details.text_tokens"), usage.Get("completion_tokens_details.text_tokens"))),
	}
	if d.ImageOutputTokens <= 0 && d.ImageInputTokens <= 0 {
		return ImageUsageDetails{}, false
	}
	return d, true
}

func firstPositive(values ...gjson.Result) int64 {
	for _, v := range values {
		if n := v.Int(); n > 0 {
			return n
		}
	}
	return 0
}
