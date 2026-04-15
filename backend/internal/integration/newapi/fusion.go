package newapi

import (
	"sync/atomic"

	newapiaffinity "github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

var (
	affinityLookupTotal atomic.Int64
	affinityHitTotal    atomic.Int64
)

// GetPreferredAccountByAffinity returns an affinity-preferred account ID.
// It wraps New API channel-affinity cache/matcher with Sub2API account semantics.
func GetPreferredAccountByAffinity(c *gin.Context, modelName string, groupName string) (int64, bool) {
	affinityLookupTotal.Add(1)
	if c == nil {
		return 0, false
	}
	id, ok := newapiaffinity.GetPreferredChannelByAffinity(c, modelName, groupName)
	if !ok || id <= 0 {
		return 0, false
	}
	affinityHitTotal.Add(1)
	return int64(id), true
}

func AffinityHitStats() (lookups int64, hits int64) {
	return affinityLookupTotal.Load(), affinityHitTotal.Load()
}

// MarkAffinitySelected records selected account for affinity diagnostics.
func MarkAffinitySelected(c *gin.Context, selectedGroup string, accountID int64) {
	if c == nil || accountID <= 0 {
		return
	}
	newapiaffinity.MarkChannelAffinityUsed(c, selectedGroup, int(accountID))
}

// RecordAffinitySuccess stores affinity mapping after successful relay.
func RecordAffinitySuccess(c *gin.Context, accountID int64) {
	if c == nil || accountID <= 0 {
		return
	}
	newapiaffinity.RecordChannelAffinity(c, int(accountID))
}
