//go:build unit

package partitionmaintenance

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

func TestResultStringIncludesPartialRehomeReceipt(t *testing.T) {
	result := Result{
		Tables: []TableResult{
			{
				Table: "qa_records",
				DefaultRehome: &pgpartition.RehomeDefaultResult{
					RemainingRows:    10,
					StagingRows:      3,
					RowsMoved:        3,
					PendingFinalize:  true,
					BudgetExhausted:  true,
					DefaultPartition: "qa_records_default",
				},
			},
		},
	}
	got := result.String()
	for _, want := range []string{
		"qa_records:rehome_remaining=10",
		"staging=3",
		"moved=3",
		"pending=true",
		"budget=true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("result string %q missing %q", got, want)
		}
	}
}
