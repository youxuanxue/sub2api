package qa

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/replaycapture"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

const replayIngressKey = "qa_release_replay_ingress"

type replayIngress struct {
	method, path string
	headers      map[string]string
	request      *http.Request
	reader       *requestCaptureReader
	observer     *replaycapture.ResponseObserver
}

// Keep only understood protocol queries. Credential query parameters are
// deliberately reconstructed from APIKeyID; unknown business queries are gaps.
func replayRequestPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	for k := range q {
		switch strings.ToLower(k) {
		case "key", "api_key", "access_token":
			q.Del(k)
		case "alt", "beta":
		default:
			return ""
		}
	}
	path := u.EscapedPath()
	if len(path) > 2048 {
		return ""
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}
func replayHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"Content-Type", "Accept", "User-Agent", "Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta"} {
		if v := h.Get(k); v != "" && len(v) <= 1024 && !strings.ContainsAny(v, "\r\n") {
			out[k] = v
		}
	}
	return out
}
func (s *Service) captureReplay(c *gin.Context, input *CaptureInput) {
	if s.replay == nil {
		return
	}
	value, ok := c.Get(replayIngressKey)
	if !ok {
		return
	}
	in, ok := value.(*replayIngress)
	if !ok || in == nil {
		return
	}
	if in.path == "" || !in.observer.Successful(input.StatusCode) {
		return
	}
	var body []byte
	mediaType, params, mediaErr := mime.ParseMediaType(in.headers["Content-Type"])
	multipartAudio := mediaErr == nil && mediaType == "multipart/form-data" && replayAudioPath(in.path)
	if in.reader != nil {
		if in.reader.total > replaycapture.MaxBodyBytes || (!in.reader.eof && (in.request.ContentLength < 0 || in.reader.total != in.request.ContentLength)) {
			return
		}
		if multipartAudio {
			if encoding := in.request.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
				return
			}
			body = in.reader.body.Bytes()
		} else {
			body = qaRequestCaptureBytes(in.request, in.reader.body.Bytes(), replaycapture.MaxBodyBytes+1)
		}
	}
	if len(body) > replaycapture.MaxBodyBytes {
		return
	}
	if in.method == http.MethodPost && multipartAudio {
		model, ok := replayMultipartModel(body, params["boundary"])
		if !ok {
			return
		}
		if model != "" {
			input.RequestedModel = model
		}
		input.MultimodalPresent = true
	} else if in.method == http.MethodPost {
		var v map[string]any
		if json.Unmarshal(body, &v) != nil || len(v) == 0 || v["_qa_body_omitted"] == true {
			return
		}
	} else if in.method != http.MethodGet || len(body) != 0 {
		return
	}
	req := replaycapture.Request{RequestID: input.RequestID, UserID: input.UserID, APIKeyID: input.APIKeyID, Model: input.RequestedModel,
		Endpoint: input.InboundEndpoint, Stream: input.Stream, Tools: input.ToolCallsPresent, Multimodal: input.MultimodalPresent,
		Method: in.method, Path: in.path, Headers: in.headers, Body: body, CapturedAt: input.CreatedAt}
	if !s.replay.Enqueue(req) {
		logger.L().Warn("release replay evidence unavailable")
	}
}

// Gemini places the model in its business URL, not in the JSON body.
func captureRequestModel(c *gin.Context, body []byte) string {
	if model := captureRequestedModel(body); model != "" {
		return model
	}
	path := c.Request.URL.Path
	if original := c.GetString("qa_ingress_path"); original != "" {
		if u, err := url.Parse(original); err == nil {
			path = u.Path
		}
	}
	if value, ok := c.Get(replayIngressKey); ok {
		if in, ok := value.(*replayIngress); ok && in != nil {
			if u, err := url.Parse(in.path); err == nil {
				path = u.Path
			}
		}
	}
	if strings.HasPrefix(path, "/v1beta/models/") {
		name, action, found := strings.Cut(strings.TrimPrefix(path, "/v1beta/models/"), ":")
		if found && name != "" && !strings.Contains(name, "/") && (action == "generateContent" || action == "streamGenerateContent" || action == "countTokens") {
			return name
		}
	}
	return ""
}

func replayAudioPath(path string) bool {
	u, err := url.Parse(path)
	return err == nil && (u.Path == "/v1/audio/transcriptions" || u.Path == "/audio/transcriptions")
}

// Parse only the bounded model field; file bytes remain encrypted and never
// enter the ordinary QA body or export. Duplicate models are ambiguous evidence.
func replayMultipartModel(body []byte, boundary string) (string, bool) {
	if boundary == "" {
		return "", false
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	model, seenModel, seenFile := "", false, false
	for n := 0; n < 64; n++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			return model, seenFile
		}
		if err != nil {
			return "", false
		}
		if part.FormName() == "model" {
			if seenModel {
				return "", false
			}
			seenModel = true
			raw, err := io.ReadAll(io.LimitReader(part, 513))
			if err != nil || len(raw) > 512 {
				return "", false
			}
			model = string(raw)
		}
		if part.FormName() == "file" && part.FileName() != "" {
			seenFile = true
		}
		if err := part.Close(); err != nil {
			return "", false
		}
	}
	return "", false
}
