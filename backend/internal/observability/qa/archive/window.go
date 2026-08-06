package archive

import "time"

// PreviousSealedHour returns the UTC hour [start, end) archived when maintenance
// runs at runAt (scheduled *:15 UTC with seal delay for the just-finished hour).
func PreviousSealedHour(runAt time.Time) (start, end time.Time) {
	runAt = runAt.UTC()
	end = runAt.Truncate(time.Hour)
	start = end.Add(-time.Hour)
	return start, end
}
