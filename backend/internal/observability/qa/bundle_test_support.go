//go:build unit

package qa

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/bundle"
)

func (s *Service) ConfigureBundleForTest(
	store bundle.Store,
	queue bundle.JobQueue,
	watermark func(context.Context) (time.Time, error),
	authorize func(context.Context, int64, int64) (bool, error),
	signer BlobStore,
) {
	s.bundleStore = store
	s.bundleQueue = queue
	s.bundleWatermark = watermark
	s.bundleAuthorize = authorize
	s.exportStore = signer
}
