package trajectory

import (
	"fmt"
	"strings"
	"time"
)

func BlobKey(createdAtYear int, createdAtMonth int, createdAtDay int, requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = "unknown"
	}
	return fmt.Sprintf("%04d/%02d/%02d/%s/%s.json.zst",
		createdAtYear,
		createdAtMonth,
		createdAtDay,
		RequestIDPrefix(requestID),
		requestID,
	)
}

// HourlyBlobKey returns YYYY/MM/DD/HH/<prefix>/<request-id>.json.zst under the blob store root.
func HourlyBlobKey(hourStart time.Time, requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = "unknown"
	}
	h := hourStart.UTC()
	return fmt.Sprintf("%04d/%02d/%02d/%02d/%s/%s.json.zst",
		h.Year(), int(h.Month()), h.Day(), h.Hour(), RequestIDPrefix(requestID), requestID)
}

func RequestIDPrefix(requestID string) string {
	if len(requestID) < 2 {
		return "00"
	}
	return requestID[:2]
}
