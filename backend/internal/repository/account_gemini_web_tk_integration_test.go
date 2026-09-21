//go:build integration

package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

func (s *AccountRepoSuite) TestGeminiWebLeaseAndCAS() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "gemini-web-cas",
		Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Schedulable: true,
		Credentials: map[string]any{"api_key": "worker-key", "gemini_web": map[string]any{
			"runtime": map[string]any{"version": 1, "user_agent": "ua", "cookies": []any{}}}}})
	id := account.ID
	acquired, err := s.repo.AcquireGeminiWebLease(s.ctx, id, "one")
	s.Require().NoError(err)
	s.Require().True(acquired)
	acquired, err = s.repo.AcquireGeminiWebLease(s.ctx, id, "two")
	s.Require().NoError(err)
	s.Require().False(acquired)
	runtime := map[string]any{"user_agent": "rotated", "cookies": []any{}}
	updated, err := s.repo.CompareAndSwapGeminiWebRuntime(s.ctx, id, 1, "two", runtime)
	s.Require().NoError(err)
	s.Require().False(updated)
	updated, err = s.repo.CompareAndSwapGeminiWebRuntime(s.ctx, id, 1, "one", runtime)
	s.Require().NoError(err)
	s.Require().True(updated)
	// A settings editor loaded version 1 before the worker's save. Its ordinary
	// credentials update must preserve version 2 and the current lease.
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, id, account.Credentials))
	account.Name = "edited while worker owns lease"
	s.Require().NoError(s.repo.Update(s.ctx, account))
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, id, map[string]any{"api_key": "worker-key"}))
	newCredentials := map[string]any{"api_key": "worker-key", "gemini_web": map[string]any{
		"runtime": map[string]any{"version": 3, "user_agent": "import", "cookies": []any{}}}}
	s.Require().Error(s.repo.UpdateCredentials(s.ctx, id, newCredentials), "import cannot revoke a live lease")
	updated, err = s.repo.CompareAndSwapGeminiWebRuntime(s.ctx, id, 1, "one", runtime)
	s.Require().NoError(err)
	s.Require().False(updated, "old version must never overwrite a newer import or refresh")
	s.Require().NoError(s.repo.ReleaseGeminiWebLease(s.ctx, id, "two"))
	acquired, err = s.repo.AcquireGeminiWebLease(s.ctx, id, "two")
	s.Require().NoError(err)
	s.Require().False(acquired, "stale release cannot delete another owner's lease")
	_, err = s.repo.sql.ExecContext(s.ctx, `UPDATE accounts SET credentials = jsonb_set(credentials,
		'{gemini_web,lease,expires_at}', '0'::jsonb) WHERE id = $1`, id)
	s.Require().NoError(err)
	updated, err = s.repo.CompareAndSwapGeminiWebRuntime(s.ctx, id, 2, "one", runtime)
	s.Require().NoError(err)
	s.Require().False(updated, "expired owner cannot publish")
	acquired, err = s.repo.AcquireGeminiWebLease(s.ctx, id, "two")
	s.Require().NoError(err)
	s.Require().True(acquired)
	s.Require().NoError(s.repo.ReleaseGeminiWebLease(s.ctx, id, "two"))
	loaded, err := s.repo.GetByID(s.ctx, id)
	s.Require().NoError(err)
	s.Require().Equal("worker-key", loaded.Credentials["api_key"])
	s.Require().Equal("rotated", loaded.Credentials["gemini_web"].(map[string]any)["runtime"].(map[string]any)["user_agent"])
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, id, newCredentials))
	loaded, err = s.repo.GetByID(s.ctx, id)
	s.Require().NoError(err)
	s.Require().Equal("import", loaded.Credentials["gemini_web"].(map[string]any)["runtime"].(map[string]any)["user_agent"])
	s.Require().NoError(s.repo.SetSchedulable(s.ctx, id, false))
	acquired, err = s.repo.AcquireGeminiWebLease(s.ctx, id, "three")
	s.Require().NoError(err)
	s.Require().False(acquired)
}
