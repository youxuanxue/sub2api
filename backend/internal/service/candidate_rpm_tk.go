package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type candidateRPMAdmission struct {
	started bool
	groups  map[int64]error
	user    bool
	userErr error
}

func (s *BillingCacheService) candidateGroupRPMCount(ctx context.Context, userID, groupID int64, peek bool) (int, error) {
	if !peek {
		return s.userRPMCache.IncrementUserGroupRPM(ctx, userID, groupID)
	}
	count, err := s.userRPMCache.GetUserGroupRPM(ctx, userID, groupID)
	return count + 1, err
}

func (r *CandidateRequest) rpmGroupUser(user *User) *User {
	copy := *user
	copy.RPMLimit = 0
	if r.key.IsUniversal() {
		// The auth snapshot's override belongs to its original bound group.
		copy.UserGroupRPMOverride = nil
	}
	return &copy
}

func (r *CandidateRequest) peekGroupRPM(ctx context.Context, billing *BillingCacheService, user *User, group *Group) error {
	if user == nil || group == nil || billing.cfg.RunMode == config.RunModeSimple {
		return nil
	}
	if err, checked := r.rpm.groups[group.ID]; checked {
		return err
	}
	return billing.checkRPMCounters(ctx, r.rpmGroupUser(user), group, true)
}

// Count an origin only when selected for execution, and the user once per
// request/turn. Failed origins retain their result throughout reselection.
func (r *CandidateRequest) checkRPM(ctx context.Context, billing *BillingCacheService, user *User, group *Group) error {
	if user == nil || billing.cfg.RunMode == config.RunModeSimple {
		return nil
	}
	r.rpm.started = true
	if group != nil {
		err, checked := r.rpm.groups[group.ID]
		if !checked {
			err = billing.checkRPMCounters(ctx, r.rpmGroupUser(user), group, false)
			if r.rpm.groups == nil {
				r.rpm.groups = make(map[int64]error)
			}
			r.rpm.groups[group.ID] = err
		}
		if err != nil {
			return err
		}
	}
	if !r.rpm.user {
		r.rpm.userErr = billing.checkRPMCounters(ctx, user, nil, false)
		r.rpm.user = true
	}
	return r.rpm.userErr
}
