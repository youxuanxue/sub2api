package handler

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

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
	writeVideoURLSuccess(c, url)
	return true
}

// tkMaybeOffloadVideoToS3 transforms a TERMINAL-SUCCESS fetch response whose body
// carries inline base64 video (Veo operation shape) into one carrying a presigned
// S3 URL: decode → upload → store the key on the record → strip the base64 and set
// top-level `video_url`. Returns the rewritten body and true when it offloaded;
// (nil, false) means "pass the upstream body through unchanged" — store disabled,
// not a success, no inline base64 (upstream already returned a URL), or any
// best-effort failure (we never fail a generated+billed video over offload).
func (h *OpenAIGatewayHandler) tkMaybeOffloadVideoToS3(ctx context.Context, rec *service.VideoTaskRecord, out *bridge.VideoFetchOutcome) ([]byte, bool) {
	if h.mediaStore == nil || rec == nil || out == nil || len(out.RawResponse) == 0 {
		return nil, false
	}
	if terminal, failed := videoTerminalOutcome(out.Status); !terminal || failed {
		return nil, false
	}
	b64, mime := extractInlineVideoBase64(out.RawResponse)
	if b64 == "" {
		return nil, false // upstream already returns a URL (doubao/volc) → passthrough
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		logger.L().Warn("openai_video_fetch.s3_b64_decode_failed",
			zap.String("public_task_id", rec.PublicTaskID), zap.Error(err))
		return nil, false
	}
	key := "media/videos/" + rec.PublicTaskID + videoExtForMime(mime)
	if err := h.mediaStore.Upload(ctx, key, data, mediaContentType(mime)); err != nil {
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
	return rewriteVideoBodyWithURL(out.RawResponse, url), true
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
// (so it can't return a now-empty data: URI) and sets top-level `video_url` to the
// presigned URL — which is exactly the field extractVideoUrl's URL branch reads.
// All other upstream fields (done/status/error) are preserved so videoStateFromFetch
// still classifies the task as succeeded.
func rewriteVideoBodyWithURL(body []byte, url string) []byte {
	out := body
	for _, p := range []string{"response.videos", "response.bytesBase64Encoded", "response.video"} {
		if b, err := sjson.DeleteBytes(out, p); err == nil {
			out = b
		}
	}
	if b, err := sjson.SetBytes(out, "video_url", url); err == nil {
		out = b
	}
	return out
}

// writeVideoURLSuccess writes the minimal success body the frontend needs: done=true
// (videoStateFromFetch ⇒ succeeded) + video_url (extractVideoUrl ⇒ the URL).
func writeVideoURLSuccess(c *gin.Context, url string) {
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	body, _ := sjson.SetBytes([]byte(`{"done":true}`), "video_url", url)
	_, _ = c.Writer.Write(body)
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
