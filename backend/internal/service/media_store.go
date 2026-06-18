package service

import (
	"context"
	"time"
)

// MediaStore is the storage contract for offloading GENERATED media (video now;
// image in a follow-up) to an S3(-compatible) bucket and handing the client a
// short-lived presigned URL — instead of streaming a 10-20 MB inline-base64 body
// through the gateway. The interface lives in `service` so the handler depends on
// a behaviour, not on the AWS SDK; the concrete impl is in the repository package
// (repository.NewMediaStore), which returns nil when media offload is not
// configured (driver/bucket empty) — callers MUST treat a nil MediaStore as
// "disabled" and pass media through unchanged (inline base64).
type MediaStore interface {
	// Upload puts body at key. contentType is the stored object's MIME (e.g.
	// "video/mp4"). Implementations read the whole body; media is already fully
	// in memory by the time we offload it.
	Upload(ctx context.Context, key string, body []byte, contentType string) error

	// PresignURL returns a time-limited GET URL for key. The effective lifetime
	// is min(expiry, signing-credential lifetime) — on prod the signer is the EC2
	// instance role, so links are intentionally short-lived and re-minted on
	// demand (VideoFetch re-presigns from the stored S3 key).
	PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error)
}
