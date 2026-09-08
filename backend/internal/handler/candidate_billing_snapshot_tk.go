package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Snapshot before enqueueing: candidate reselection and later WebSocket turns
// may mutate the request key and its selected subscription.
func snapshotCandidateBilling(ctx context.Context, key *service.APIKey, subscription *service.UserSubscription) (*service.APIKey, *service.UserSubscription) {
	subscription = service.CandidateSubscription(ctx, subscription)
	if key != nil {
		copy := *key
		if key.GroupID != nil {
			groupID := *key.GroupID
			copy.GroupID = &groupID
		}
		if key.Group != nil {
			group := *key.Group
			copy.Group = &group
		}
		key = &copy
	}
	if subscription != nil {
		copy := *subscription
		subscription = &copy
	}
	return key, subscription
}
