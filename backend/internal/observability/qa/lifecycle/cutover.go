package lifecycle

import (
	"fmt"
	"strings"
	"time"
)

const HourlyStorageCutoverSettingKey = "qa_hourly_storage_cutover_utc"

// ParseHourlyCutoverUTCStrict parses T0 and rejects malformed non-empty values.
func ParseHourlyCutoverUTCStrict(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(raw, "Z") {
		raw = raw[:len(raw)-1] + "+00:00"
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("lifecycle: invalid hourly_storage_cutover_utc %q: %w", raw, err)
	}
	hour := parsed.UTC()
	hourStart := time.Date(hour.Year(), hour.Month(), hour.Day(), hour.Hour(), 0, 0, 0, time.UTC)
	if !hour.Equal(hourStart) {
		return time.Time{}, fmt.Errorf("lifecycle: hourly_storage_cutover_utc must be an exact UTC hour")
	}
	return hourStart, nil
}
