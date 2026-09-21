//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestGeminiWebMaintenanceOnlyReturnsDueAccounts() {
	now := time.Now().Unix()
	states := []struct {
		name  string
		state map[string]any
		lease map[string]any
		due   bool
	}{
		{"new", map[string]any{}, nil, true},
		{"expired", map[string]any{"last_refresh": now - 601}, nil, true},
		{"fresh", map[string]any{"last_refresh": now}, nil, false},
		{"blocked", map[string]any{"blocked": true}, nil, false},
		{"uncertain", map[string]any{"generation_pending": true}, nil, false},
		{"cooldown", map[string]any{"cooldown_until": now + 300}, nil, false},
		{"leased", map[string]any{}, map[string]any{"owner": "peer", "expires_at": now + 600}, false},
	}
	want := []int64{}
	for _, tc := range states {
		web := map[string]any{"runtime": map[string]any{"version": 1, "state": tc.state}}
		if tc.lease != nil {
			web["lease"] = tc.lease
		}
		account := mustCreateAccount(s.T(), s.client, &service.Account{Name: tc.name,
			Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"gemini_web": web}})
		if tc.due {
			want = append(want, account.ID)
		}
	}
	plain := mustCreateAccount(s.T(), s.client, &service.Account{Name: "ordinary-gemini",
		Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "ordinary-key"}})
	plain.Name = "ordinary edited"
	s.Require().NoError(s.repo.Update(s.ctx, plain))
	s.Require().Equal(map[string]any{"api_key": "ordinary-key"}, plain.Credentials)
	for i := 0; i < 3; i++ {
		ids, err := s.repo.ListDueGeminiWebAccounts(s.ctx)
		s.Require().NoError(err)
		s.Require().ElementsMatch(want, ids)
	}
	// Repeated polling does not create a lease or advance runtime versions.
	for _, id := range want {
		account, err := s.repo.GetByID(s.ctx, id)
		s.Require().NoError(err)
		web := account.Credentials["gemini_web"].(map[string]any)
		s.Require().NotContains(web, "lease")
		s.Require().Equal(float64(1), web["runtime"].(map[string]any)["version"])
	}
}

func (s *AccountRepoSuite) TestGeminiWebImportSurvivesPreBindingEditor() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "pre-binding-editor",
		Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "worker-key"}})
	s.Require().NoError(s.repo.UpdateCredentials(s.ctx, account.ID, map[string]any{
		"api_key": "worker-key", "gemini_web": map[string]any{
			"runtime": map[string]any{"version": 1, "user_agent": "imported"}}}))
	account.Name = "stale editor"
	s.Require().NoError(s.repo.Update(s.ctx, account))
	loaded, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().Equal("stale editor", loaded.Name)
	s.Require().Equal("imported", loaded.Credentials["gemini_web"].(map[string]any)["runtime"].(map[string]any)["user_agent"])
}

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
