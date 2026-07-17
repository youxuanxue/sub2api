package handler

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// mediaPresignTTL bounds presigned GET URLs. Kept short: on prod the signer is
// the EC2 instance role, whose session credentials cap the effective lifetime.
const mediaPresignTTL = time.Hour

// SetMediaStore wires the media store post-construction. Video generation no
// longer uploads fresh results to S3; the store remains here only so legacy
// video records carrying MediaS3Key can be re-presigned, and so image offload
// can share the same handler wiring.
func (h *OpenAIGatewayHandler) SetMediaStore(store service.MediaStore) {
	h.mediaStore = store
}

// tkVideoFastPathFromS3 serves legacy already-offloaded clips without touching
// upstream: if an old record carries an S3 key, re-presign and return a small
// success JSON. New video results do not enter this path because TokenKey no
// longer rehosts generated videos by default.
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

// writeVideoURLSuccess writes the minimal success body the frontend needs: done=true
// (videoStateFromFetch ⇒ succeeded) + video_url (extractVideoUrl ⇒ the URL).
func writeVideoURLSuccess(c *gin.Context, url string) {
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	body, _ := sjson.SetBytes([]byte(`{"done":true}`), "video_url", url)
	_, _ = c.Writer.Write(body)
}
