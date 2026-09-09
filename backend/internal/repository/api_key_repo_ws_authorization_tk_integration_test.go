//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *APIKeyRepoSuite) TestGetByID_RefreshesExclusiveGroupAuthorization() {
	user := s.mustCreateUser("ws-exclusive-refresh@test.com")
	group := s.mustCreateGroup("ws-exclusive-refresh")
	_, err := s.client.Group.UpdateOneID(group.ID).SetIsExclusive(true).Save(s.ctx)
	s.Require().NoError(err)
	key := &service.APIKey{
		UserID: user.ID, Key: "sk-ws-exclusive-refresh", Name: "WS refresh",
		GroupID: &group.ID, Status: service.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, key))

	_, err = s.client.User.UpdateOneID(user.ID).AddAllowedGroupIDs(group.ID).Save(s.ctx)
	s.Require().NoError(err)
	opening, err := s.repo.GetByKeyForAuth(s.ctx, key.Key)
	s.Require().NoError(err)
	s.Require().True(opening.User.CanBindGroup(group.ID, opening.Group.IsExclusive))

	refreshed, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().NotNil(refreshed.User)
	s.Require().NotNil(refreshed.Group)
	s.Require().True(refreshed.Group.IsExclusive)
	s.Require().True(refreshed.User.CanBindGroup(group.ID, refreshed.Group.IsExclusive),
		"a fresh WS authorization read must preserve a granted exclusive group")

	_, err = s.client.User.UpdateOneID(user.ID).RemoveAllowedGroupIDs(group.ID).Save(s.ctx)
	s.Require().NoError(err)
	revoked, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().False(revoked.User.CanBindGroup(group.ID, revoked.Group.IsExclusive),
		"the next WS turn must observe an exclusive group revocation")
}
