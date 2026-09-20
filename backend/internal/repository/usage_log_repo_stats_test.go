package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountUsageCalendarDaysUsesCalendarDatesAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	start := time.Date(2025, 3, 8, 0, 0, 0, 0, loc)
	end := time.Date(2025, 3, 11, 0, 0, 0, 0, loc)

	require.Equal(t, 3, accountUsageCalendarDays(start, end))
	require.Equal(t, "2025-03-10", accountUsageEndDate(start, end))
}
