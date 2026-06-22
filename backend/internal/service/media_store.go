package service

import (
	"context"
	"time"
)

// MediaStore is the storage contract for generated-media offload to an
// S3-compatible bucket. Images use it to replace inline base64 with short-lived
// presigned URLs; video generation no longer uploads fresh results by default
// and only uses PresignURL for legacy records that already carry an S3 key.
// The interface lives in `service` so callers depend on behaviour, not the AWS
// SDK; repository.NewMediaStore returns nil when offload is not configured.
type MediaStore interface {
	// Upload puts body at key. contentType is the stored object's MIME (e.g.
	// "image/png"). Implementations read the whole body; callers should only use
	// this for bounded payloads that are already materialized.
	Upload(ctx context.Context, key string, body []byte, contentType string) error

	// PresignURL returns a time-limited GET URL for key. The effective lifetime
	// is min(expiry, signing-credential lifetime) — on prod the signer is the EC2
	// instance role, so links are intentionally short-lived and re-minted on
	// demand for stored image objects and legacy video records.
	PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error)
}
