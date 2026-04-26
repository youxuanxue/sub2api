package trajectory

import (
	"fmt"
	"strings"
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

func RequestIDPrefix(requestID string) string {
	if len(requestID) < 2 {
		return "00"
	}
	return requestID[:2]
}
