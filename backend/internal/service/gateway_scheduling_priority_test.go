package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchedulingPriorityIgnoresAccountExtra(t *testing.T) {
	newAccounts := func() []*Account {
		return []*Account{
			{ID: 1, Priority: 300, Extra: map[string]any{"tk_source_class": "native"}},
			{ID: 2, Priority: 200, Extra: map[string]any{"tk_source_class": "supplier"}},
			{ID: 3, Priority: 100},
		}
	}

	for _, mode := range []string{"random", "last_used"} {
		t.Run(mode, func(t *testing.T) {
			accounts := newAccounts()
			(&GatewayService{}).sortCandidatesForFallback(accounts, false, mode)

			require.Equal(t, []int64{3, 2, 1}, []int64{accounts[0].ID, accounts[1].ID, accounts[2].ID})
		})
	}
}
