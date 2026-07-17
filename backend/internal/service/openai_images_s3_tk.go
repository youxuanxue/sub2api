package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// imagePresignTTL bounds the presigned GET URL handed back in an offloaded image
// response. Kept short for the same reason as video (mediaPresignTTL): on prod the
// signer is the EC2 instance role, whose session credentials cap the effective
// lifetime anyway, and the Studio re-mints from the stored S3 key on reload via
// POST /v1/images/presign (handler.ImagesPresign) — no re-generation, no re-bill.
const imagePresignTTL = time.Hour

// imageOffloadUploadTimeout bounds the synchronous S3 PutObject so a slow (not
// dead) bucket can't pin a gateway goroutine on the image request path. On
// timeout the offload falls back to inline-base64 passthrough (best-effort), so
// the image still returns — just not offloaded. A dead bucket already errors fast
// via best-effort; this covers the slow-but-alive case.
const imageOffloadUploadTimeout = 15 * time.Second

// MediaImageKeyPrefix is the S3 key namespace for offloaded generated images. It
// is the SINGLE source of truth shared by both sides of the offload contract: the
// upload side (this file stamps every s3_key under it) and the re-presign endpoint
// (handler.ImagesPresign validates incoming keys against it). Exported so the
// handler references this exact value rather than a second copy that could drift.
const MediaImageKeyPrefix = "media/images/"

// SetMediaStore wires the media-offload store post-construction. Mirrors the
// handler's SetMediaStore for video (CLAUDE.md §5 — keep the upstream-shape
// constructor stable). A nil store is the valid "offload disabled" state: image
// responses then pass their inline base64 through unchanged. Note the store can be
// wired (legacy video re-presign) while image offload itself stays OFF — image
// rehosting is additionally gated on MediaStorage.ImageOffloadEnabled (default off).
func (s *OpenAIGatewayService) SetMediaStore(store MediaStore) {
	s.mediaStore = store
}

// tkMaybeOffloadImagesToS3 transforms a non-streaming /v1/images/generations success
// body whose data[i] carries an inline-base64 image (either a `b64_json` field or a
// `data:` URI in `url`) into one carrying a short-lived presigned S3 URL: decode →
// upload to media/images/<sha256>.<ext> → set data[i].url to the presigned URL, drop
// b64_json, and stamp data[i].s3_key so the Studio can re-presign on reload.
//
// OPT-IN since the #944 pass-through alignment: offload runs ONLY when a deployment
// explicitly sets MediaStorage.ImageOffloadEnabled (env MEDIA_STORAGE_IMAGE_OFFLOAD_ENABLED).
// By DEFAULT the inline base64 passes through to the client ONCE — the gateway does not
// rehost generated images, mirroring what #944 did for video (TokenKey is not an image
// CDN). When offload IS enabled, the additional exception is an explicit response_format
// = "b64_json": that client asked for raw bytes, so we honour the API contract and pass
// the body through untouched. Also returns the body UNCHANGED when the store is nil,
// there is no data array, an item already holds an http URL, or on ANY best-effort
// failure — we never fail a generated + billed image over offload.
func (s *OpenAIGatewayService) tkMaybeOffloadImagesToS3(ctx context.Context, body []byte, responseFormat string) []byte {
	// Default: do not rehost — pass the inline base64 through once (#944 parity with
	// video). Opt-in restores the old presigned-URL behavior for deployments that
	// want the gateway to keep image bytes off the client-facing response.
	if s.cfg == nil || !s.cfg.MediaStorage.ImageOffloadEnabled {
		return body
	}
	if s.mediaStore == nil || len(body) == 0 {
		return body
	}
	// Explicit b64_json => the client wants raw bytes; don't rewrite to a URL.
	if strings.EqualFold(strings.TrimSpace(responseFormat), "b64_json") {
		return body
	}
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return body
	}

	out := body
	offloaded := 0
	data.ForEach(func(idx, item gjson.Result) bool {
		b64, mime := tkInlineImageBytes(item)
		if b64 == "" {
			return true // http url already, or no inline bytes on this item → passthrough
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(decoded) == 0 {
			return true
		}
		sum := sha256.Sum256(decoded)
		key := MediaImageKeyPrefix + hex.EncodeToString(sum[:])[:32] + tkImageExtForMime(mime)
		upCtx, cancel := context.WithTimeout(ctx, imageOffloadUploadTimeout)
		uploadErr := s.mediaStore.Upload(upCtx, key, decoded, tkImageContentType(mime))
		cancel()
		if uploadErr != nil {
			logger.L().Warn("openai_images.s3_upload_failed",
				zap.Int64("index", idx.Int()), zap.Int("bytes", len(decoded)), zap.Error(uploadErr))
			return true
		}
		url, err := s.mediaStore.PresignURL(ctx, key, imagePresignTTL)
		if err != nil {
			logger.L().Warn("openai_images.s3_presign_failed",
				zap.Int64("index", idx.Int()), zap.String("key", key), zap.Error(err))
			return true
		}
		p := "data." + strconv.FormatInt(idx.Int(), 10)
		if b, derr := sjson.DeleteBytes(out, p+".b64_json"); derr == nil {
			out = b
		}
		if b, serr := sjson.SetBytes(out, p+".url", url); serr == nil {
			out = b
		}
		// Stamp the S3 key so a reloaded Studio session can re-presign a fresh
		// short-lived URL without re-generating (Studio history is localStorage-
		// persisted; the presigned URL is intentionally short-lived).
		if b, serr := sjson.SetBytes(out, p+".s3_key", key); serr == nil {
			out = b
		}
		offloaded++
		return true
	})

	if offloaded > 0 {
		logger.L().Info("openai_images.s3_offloaded", zap.Int("count", offloaded))
	}
	return out
}

// tkInlineImageBytes returns the offloadable base64 payload + mime of one data[] item.
// Order: a `data:` URI in `url` (carries its own mime) wins; an already-http `url` is
// left alone (returns ""); otherwise a bare `b64_json` field is offloadable (mime
// unknown → png default). This is what lets offload run regardless of response_format —
// both the url-shape (data: URI) and the b64_json-shape responses are covered.
func tkInlineImageBytes(item gjson.Result) (b64, mime string) {
	if u := item.Get("url").String(); u != "" {
		if strings.HasPrefix(u, "data:") {
			return tkInlineImageDataURI(u)
		}
		return "", "" // a real http(s) URL — already offloaded / hosted upstream
	}
	if b := item.Get("b64_json").String(); b != "" {
		return b, ""
	}
	return "", ""
}

// tkInlineImageDataURI returns the base64 payload + mime of a `data:` image URI, or
// ("", "") for anything else (a non-base64 data URI, or empty). Only base64 data URIs
// are offloadable — they are the inline-bytes case.
func tkInlineImageDataURI(u string) (b64, mime string) {
	const scheme = "data:"
	if !strings.HasPrefix(u, scheme) {
		return "", ""
	}
	rest := u[len(scheme):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", ""
	}
	meta := rest[:comma] // e.g. "image/png;base64"
	if !strings.Contains(meta, "base64") {
		return "", ""
	}
	mime = meta
	if semi := strings.IndexByte(meta, ';'); semi >= 0 {
		mime = meta[:semi]
	}
	return rest[comma+1:], mime
}

func tkImageExtForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

func tkImageContentType(mime string) string {
	if m := strings.ToLower(strings.TrimSpace(mime)); strings.HasPrefix(m, "image/") {
		return m
	}
	return "image/png"
}
