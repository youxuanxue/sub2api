//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TestListSchedulableByGroupIDs_EqualsPerGroup pins the batched
// ListSchedulableByGroupIDs SQL to the per-group ListSchedulableByGroupID it
// replaces in GroupCapacityService. The batch method hand-mirrors
// queryAccountsByGroup's schedulable predicate set, so this guards against that
// copy drifting (e.g. a future upstream change to the predicate) by asserting,
// on a real Postgres, that for every requested group the batch returns exactly
// the per-group schedulable account set — including a group whose account is
// shared with another group, and a group with no schedulable accounts (which
// must be absent from the result map, the contract GetAllGroupCapacity relies on
// to emit a zero-capacity summary).
func (s *AccountRepoSuite) TestListSchedulableByGroupIDs_EqualsPerGroup() {
	now := time.Now()
	g1 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "gb-1"})
	g2 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "gb-2"})
	g3 := mustCreateGroup(s.T(), s.client, &service.Group{Name: "gb-3-empty"})

	// g1: one schedulable + one overloaded (must be filtered out).
	ok1 := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-ok1", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, ok1.ID, g1.ID, 1)
	future := now.Add(10 * time.Minute)
	over := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-over", Schedulable: true, OverloadUntil: &future})
	mustBindAccountToGroup(s.T(), s.client, over.ID, g1.ID, 2)

	// shared account bound to BOTH g1 and g2 (exercises the union de-dup).
	shared := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-shared", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, shared.ID, g1.ID, 3)
	mustBindAccountToGroup(s.T(), s.client, shared.ID, g2.ID, 1)

	// g2: shared + one manually non-schedulable (must be filtered out).
	// (mustCreateAccount forces Schedulable=true, so flip it off explicitly.)
	noSched := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-nosched", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, noSched.ID, g2.ID, 2)
	s.Require().NoError(s.repo.SetSchedulable(s.ctx, noSched.ID, false), "SetSchedulable false")

	// g3: only a rate-limited account -> empty schedulable set.
	rl := mustCreateAccount(s.T(), s.client, &service.Account{Name: "b-rl", Schedulable: true})
	mustBindAccountToGroup(s.T(), s.client, rl.ID, g3.ID, 1)
	s.Require().NoError(s.repo.SetRateLimited(s.ctx, rl.ID, now.Add(10*time.Minute)), "SetRateLimited")

	groupIDs := []int64{g1.ID, g2.ID, g3.ID}
	batch, err := s.repo.ListSchedulableByGroupIDs(s.ctx, groupIDs)
	s.Require().NoError(err, "ListSchedulableByGroupIDs")

	// Equivalence: every requested group's batch set == the per-group set.
	for _, gid := range groupIDs {
		want, perr := s.repo.ListSchedulableByGroupID(s.ctx, gid)
		s.Require().NoError(perr, "ListSchedulableByGroupID(%d)", gid)
		s.Require().ElementsMatch(idsOfAccounts(want), idsOfAccounts(batch[gid]),
			"group %d: batch schedulable set must equal per-group", gid)
	}

	// Spot-check the expected filtering.
	s.Require().ElementsMatch([]int64{ok1.ID, shared.ID}, idsOfAccounts(batch[g1.ID]),
		"g1 schedulable = ok1 + shared (overloaded excluded)")
	s.Require().ElementsMatch([]int64{shared.ID}, idsOfAccounts(batch[g2.ID]),
		"g2 schedulable = shared (non-schedulable excluded)")

	// A group with no schedulable accounts is absent from the map (zero-capacity contract).
	_, present := batch[g3.ID]
	s.Require().False(present, "group with no schedulable accounts must be absent from the batch map")
}
