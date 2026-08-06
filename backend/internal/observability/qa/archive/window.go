package archive

import "time"

// PreviousSealedHour returns the UTC hour [start, end) archived when maintenance
// runs at runAt. sealDelayMinutes enforces design §7: an hour is sealed only after
// window_end + seal delay (prod timer *:15 UTC with default delay 15).
func PreviousSealedHour(runAt time.Time, sealDelayMinutes int) (start, end time.Time) {
	runAt = runAt.UTC()
	if sealDelayMinutes < 0 {
		sealDelayMinutes = 0
	}
	sealCutoff := runAt.Add(-time.Duration(sealDelayMinutes) * time.Minute)
	end = sealCutoff.Truncate(time.Hour)
	start = end.Add(-time.Hour)
	return start, end
}
