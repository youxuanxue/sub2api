//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

type orPolicyGroupRepoStub struct {
	groupRepoStub
	byID map[int64]*Group
}

func (s *orPolicyGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	if s.byID == nil {
		return nil, errors.New("group not found")
	}
	g, ok := s.byID[id]
	if !ok {
		return nil, errors.New("group not found")
	}
	return g, nil
}

type orPolicyAccountRepoStub struct {
	accountRepoStub
	byGroup map[int64][]Account
}

func (s *orPolicyAccountRepoStub) ListByGroup(_ context.Context, groupID int64) ([]Account, error) {
	if s.byGroup == nil {
		return nil, nil
	}
	return s.byGroup[groupID], nil
}

func TestCheckPublicGroupAggregatorChannelPolicy_RejectsForbiddenCreate(t *testing.T) {
	svc := &adminServiceImpl{
		groupRepo: &orPolicyGroupRepoStub{
			byID: map[int64]*Group{
				10: {ID: 10, Name: "public-newapi", IsExclusive: false},
			},
		},
		accountRepo: &orPolicyAccountRepoStub{byGroup: map[int64][]Account{}},
	}
	err := svc.checkPublicGroupAggregatorChannelPolicy(
		context.Background(),
		0,
		"or-upstream",
		PlatformNewAPI,
		newapiconstant.ChannelTypeOpenRouter,
		nil,
		[]int64{10},
	)
	policyErr, ok := err.(*PublicGroupAggregatorChannelError)
	if !ok {
		t.Fatalf("expected PublicGroupAggregatorChannelError, got %T: %v", err, err)
	}
	if policyErr.GroupID != 10 || policyErr.ChannelLabel != "OpenRouter" {
		t.Fatalf("policyErr=%+v", policyErr)
	}
}

func TestCheckPublicGroupAggregatorChannelPolicy_RejectsExistingAggregatorMember(t *testing.T) {
	svc := &adminServiceImpl{
		groupRepo: &orPolicyGroupRepoStub{
			byID: map[int64]*Group{
				10: {ID: 10, Name: "public-newapi", IsExclusive: false},
			},
		},
		accountRepo: &orPolicyAccountRepoStub{
			byGroup: map[int64][]Account{
				10: {{
					ID:          99,
					Name:        "existing-or",
					Platform:    PlatformNewAPI,
					ChannelType: newapiconstant.ChannelTypeOpenRouter,
				}},
			},
		},
	}
	err := svc.checkPublicGroupAggregatorChannelPolicy(
		context.Background(),
		0,
		"safe-direct",
		PlatformNewAPI,
		newapiconstant.ChannelTypeAli,
		nil,
		[]int64{10},
	)
	if _, ok := err.(*PublicGroupAggregatorChannelError); !ok {
		t.Fatalf("expected policy violation, got %v", err)
	}
}

func TestCheckPublicGroupAggregatorChannelPolicy_AllowsExclusiveGroup(t *testing.T) {
	svc := &adminServiceImpl{
		groupRepo: &orPolicyGroupRepoStub{
			byID: map[int64]*Group{
				20: {ID: 20, Name: "or-exclusive", IsExclusive: true},
			},
		},
		accountRepo: &orPolicyAccountRepoStub{byGroup: map[int64][]Account{}},
	}
	if err := svc.checkPublicGroupAggregatorChannelPolicy(
		context.Background(),
		0,
		"or-upstream",
		PlatformNewAPI,
		newapiconstant.ChannelTypeOpenRouter,
		nil,
		[]int64{20},
	); err != nil {
		t.Fatalf("exclusive group should allow aggregator upstream: %v", err)
	}
}
