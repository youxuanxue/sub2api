package lifecycle

import (
	"strings"
	"time"
)

// ParseHourlyCutoverUTC parses an RFC3339 UTC cutover timestamp.
func ParseHourlyCutoverUTC(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if strings.HasSuffix(raw, "Z") {
		raw = raw[:len(raw)-1] + "+00:00"
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
