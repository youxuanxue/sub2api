package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/ent/group"
)

// ListGroupCapacityRPMLimits returns rpm_limit for already-scoped group IDs, so
// restricted capacity summaries keep TK's group RPM gate without listing every
// active group first.
func (r *groupRepository) ListGroupCapacityRPMLimits(ctx context.Context, groupIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(groupIDs))
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.client.Group.Query().
		Where(group.IDIn(groupIDs...)).
		Select(group.FieldID, group.FieldRpmLimit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row.RpmLimit
	}
	return out, nil
}
