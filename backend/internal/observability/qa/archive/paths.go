package archive

import (
	"fmt"
	"time"
)

const RawV1Prefix = "raw/v1"

// ShardRelativePrefix returns the object key suffix under the configured raw/v1 prefix.
func ShardRelativePrefix(windowStart time.Time) string {
	windowStart = windowStart.UTC()
	return fmt.Sprintf(
		"date=%s/hour=%02d",
		windowStart.Format("2006-01-02"),
		windowStart.Hour(),
	)
}

// ShardPrefix returns the committed shard prefix for a UTC hour window start.
func ShardPrefix(windowStart time.Time) string {
	windowStart = windowStart.UTC()
	return fmt.Sprintf(
		"%s/date=%s/hour=%02d",
		RawV1Prefix,
		windowStart.Format("2006-01-02"),
		windowStart.Hour(),
	)
}

// PartialPrefix returns the incomplete shard staging prefix for a segment id.
func PartialPrefix(segmentID string) string {
	return fmt.Sprintf("raw/partial/%s", segmentID)
}
