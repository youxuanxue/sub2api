package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// mediaPresignTTL bounds the presigned GET URL. Kept short: on prod the signer
// is the EC2 instance role, whose session credentials cap the effective lifetime
// anyway, and VideoFetch re-mints from the stored S3 key on demand (fast path).
const mediaPresignTTL = time.Hour

const (
	videoURLDownloadMaxBytes = 128 << 20
	videoURLDownloadTimeout  = 90 * time.Second
	videoURLDeepScanMaxDepth = 4
)

var downloadPublicVideoURL = pkghttputil.DownloadPublicURL

// SetMediaStore wires the media-offload store post-construction. Mirrors
// SetVideoTaskCache (CLAUDE.md §5 — keep the upstream-shape constructor stable).
// A nil store is the valid "offload disabled" state: VideoFetch then passes the
// upstream's inline-base64 media through unchanged (current behaviour).
func (h *OpenAIGatewayHandler) SetMediaStore(store service.MediaStore) {
	h.mediaStore = store
}

// tkVideoFastPathFromS3 serves an already-offloaded clip without touching the
// upstream: if the record carries an S3 key, re-presign and return a small
// success JSON. Returns true when it handled (wrote) the response. This is what
// makes a reloaded Studio session work — a fresh short-lived URL for free.
func (h *OpenAIGatewayHandler) tkVideoFastPathFromS3(c *gin.Context, rec *service.VideoTaskRecord) bool {
	if h.mediaStore == nil || rec == nil || rec.MediaS3Key == "" {
		return false
	}
	url, err := h.mediaStore.PresignURL(c.Request.Context(), rec.MediaS3Key, mediaPresignTTL)
	if err != nil {
		// Presign failed — fall back to the normal upstream fetch path.
		logger.L().Warn("openai_video_fetch.s3_repesign_failed",
			zap.String("public_task_id", rec.PublicTaskID), zap.Error(err))
		return false
	}
	writeVideoURLSuccess(c, url, rec.MediaS3Key)
	return true
}

// tkMaybeOffloadVideoToS3 transforms a TERMINAL-SUCCESS fetch response carrying
// generated video bytes (inline base64) or a short-lived upstream video URL into
// one carrying a TokenKey-owned presigned S3 URL: download/decode → upload → store
// the key on the record → set top-level `video_url`. Returns the rewritten body and true when it offloaded;
// (nil, false) means "pass the upstream body through unchanged" — store disabled,
// not a success, no media URL/bytes, or any best-effort failure (we never fail a
// generated+billed video over offload).
func (h *OpenAIGatewayHandler) tkMaybeOffloadVideoToS3(ctx context.Context, rec *service.VideoTaskRecord, out *bridge.VideoFetchOutcome) ([]byte, bool) {
	if h.mediaStore == nil || rec == nil || out == nil || len(out.RawResponse) == 0 {
		return nil, false
	}
	if terminal, failed := videoTerminalOutcome(out.Status); !terminal || failed {
		return nil, false
	}
	b64, mime := extractInlineVideoBase64(out.RawResponse)
	if b64 != "" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(data) == 0 {
			logger.L().Warn("openai_video_fetch.s3_b64_decode_failed",
				zap.String("public_task_id", rec.PublicTaskID), zap.Error(err))
			return nil, false
		}
		return h.tkStoreVideoMedia(ctx, rec, out.RawResponse, data, mediaContentType(mime))
	}

	// Some upstreams return a short-lived hosted video_url instead of inline
	// bytes. If we pass that URL through, the Studio can persist only an upstream
	// preview link that later opens to "preview expired". Re-host it immediately
	// while the URL is fresh, then future opens re-presign from our MediaStore.
	videoURL := extractVideoURLFromBody(out.RawResponse)
	if videoURL == "" {
		return nil, false
	}
	download, err := downloadPublicVideoURL(ctx, videoURL, videoURLDownloadMaxBytes, videoURLDownloadTimeout)
	if err != nil {
		logger.L().Warn("openai_video_fetch.s3_url_download_failed",
			zap.String("public_task_id", rec.PublicTaskID), zap.Error(err))
		return nil, false
	}
	return h.tkStoreVideoMedia(ctx, rec, out.RawResponse, download.Body, mediaContentTypeForVideoURL(videoURL, download.ContentType))
}

func (h *OpenAIGatewayHandler) tkStoreVideoMedia(ctx context.Context, rec *service.VideoTaskRecord, rawBody []byte, data []byte, contentType string) ([]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	key := "media/videos/" + rec.PublicTaskID + videoExtForMime(contentType)
	if err := h.mediaStore.Upload(ctx, key, data, mediaContentType(contentType)); err != nil {
		logger.L().Warn("openai_video_fetch.s3_upload_failed",
			zap.String("public_task_id", rec.PublicTaskID), zap.Int("bytes", len(data)), zap.Error(err))
		return nil, false
	}
	url, err := h.mediaStore.PresignURL(ctx, key, mediaPresignTTL)
	if err != nil {
		logger.L().Warn("openai_video_fetch.s3_presign_failed",
			zap.String("public_task_id", rec.PublicTaskID), zap.Error(err))
		return nil, false
	}
	// Persist the key so subsequent fetches re-presign without re-pulling upstream
	// (best-effort — a Save miss only costs a future re-pull, never correctness).
	rec.MediaS3Key = key
	if h.videoTaskCache != nil {
		if err := h.videoTaskCache.Save(ctx, rec); err != nil {
			logger.L().Warn("openai_video_fetch.s3_key_save_failed",
				zap.String("public_task_id", rec.PublicTaskID), zap.Error(err))
		}
	}
	logger.L().Info("openai_video_fetch.s3_offloaded",
		zap.String("public_task_id", rec.PublicTaskID), zap.String("key", key), zap.Int("bytes", len(data)))
	return rewriteVideoBodyWithURL(rawBody, url, key), true
}

// extractInlineVideoBase64 pulls the inline base64 clip + its mime out of a Veo
// operation-shape body (response.videos[0].bytesBase64Encoded, or the flatter
// response.bytesBase64Encoded / response.video). Empty string ⇒ no inline video.
func extractInlineVideoBase64(body []byte) (b64, mime string) {
	root := gjson.ParseBytes(body)
	if v := root.Get("response.videos.0.bytesBase64Encoded"); v.Exists() && v.String() != "" {
		return v.String(), root.Get("response.videos.0.mimeType").String()
	}
	for _, p := range []string{"response.bytesBase64Encoded", "response.video"} {
		if v := root.Get(p); v.Exists() && v.String() != "" {
			return v.String(), root.Get("response.mimeType").String()
		}
	}
	return "", ""
}

// rewriteVideoBodyWithURL strips every inline-base64 path extractVideoUrl checks
// (so it can't return a now-empty data: URI) and replaces every known URL path
// extractVideoUrl prefers with the presigned URL. All other upstream fields
// (done/status/error) are preserved so videoStateFromFetch still classifies the
// task as succeeded.
func rewriteVideoBodyWithURL(body []byte, url string, s3Key string) []byte {
	out := body
	for _, p := range []string{"response.videos", "response.bytesBase64Encoded", "response.video"} {
		if b, err := sjson.DeleteBytes(out, p); err == nil {
			out = b
		}
	}
	for _, p := range []string{"content.video_url", "data.video_url", "video_url", "data.url"} {
		if p != "video_url" && !gjson.GetBytes(out, p).Exists() {
			continue
		}
		if b, err := sjson.SetBytes(out, p, url); err == nil {
			out = b
		}
	}
	if s3Key != "" {
		if b, err := sjson.SetBytes(out, "s3_key", s3Key); err == nil {
			out = b
		}
	}
	return out
}

// writeVideoURLSuccess writes the minimal success body the frontend needs: done=true
// (videoStateFromFetch ⇒ succeeded) + video_url (extractVideoUrl ⇒ the URL).
func writeVideoURLSuccess(c *gin.Context, url string, s3Key string) {
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	body, _ := sjson.SetBytes([]byte(`{"done":true}`), "video_url", url)
	if s3Key != "" {
		body, _ = sjson.SetBytes(body, "s3_key", s3Key)
	}
	_, _ = c.Writer.Write(body)
}

func extractVideoURLFromBody(body []byte) string {
	for _, p := range []string{"content.video_url", "data.video_url", "video_url", "data.url"} {
		if v := gjson.GetBytes(body, p).String(); isHTTPVideoURLCandidate(v) {
			return v
		}
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	return deepScanVideoURL(decoded, 0)
}

func deepScanVideoURL(node any, depth int) string {
	if depth > videoURLDeepScanMaxDepth {
		return ""
	}
	switch v := node.(type) {
	case []any:
		for _, item := range v {
			if hit := deepScanVideoURL(item, depth+1); hit != "" {
				return hit
			}
		}
	case map[string]any:
		for key, value := range v {
			s, ok := value.(string)
			if !ok || !isHTTPVideoURLCandidate(s) {
				continue
			}
			k := strings.ToLower(key)
			if strings.Contains(k, "video") || hasVideoFileExt(s) {
				return s
			}
		}
		for _, value := range v {
			if hit := deepScanVideoURL(value, depth+1); hit != "" {
				return hit
			}
		}
	}
	return ""
}

func isHTTPVideoURLCandidate(v string) bool {
	parsed, err := url.Parse(strings.TrimSpace(v))
	if err != nil || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "https" || scheme == "http"
}

func hasVideoFileExt(v string) bool {
	parsed, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(parsed.Path)) {
	case ".mp4", ".webm", ".mov", ".m4v":
		return true
	default:
		return false
	}
}

func videoExtForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		return ".mp4"
	}
}

func mediaContentType(mime string) string {
	if m := strings.ToLower(strings.TrimSpace(mime)); strings.HasPrefix(m, "video/") {
		return m
	}
	return "video/mp4"
}

func mediaContentTypeForVideoURL(videoURL string, contentType string) string {
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = contentType[:semi]
	}
	if m := mediaContentType(contentType); m != "video/mp4" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "video/") {
		return m
	}
	if parsed, err := url.Parse(strings.TrimSpace(videoURL)); err == nil {
		switch strings.ToLower(path.Ext(parsed.Path)) {
		case ".webm":
			return "video/webm"
		case ".mov":
			return "video/quicktime"
		}
	}
	return "video/mp4"
}
