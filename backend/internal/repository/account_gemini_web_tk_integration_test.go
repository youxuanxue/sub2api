//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *AccountRepoSuite) TestGeminiWebExplicitImportConflictsAndPreservesAccount() {
	a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "import-cas", Platform: service.PlatformGemini,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "key", "model_mapping": map[string]any{"public": "upstream"},
			"gemini_web": map[string]any{"runtime": map[string]any{"version": 1, "user_agent": "old"}}}})
	lease, err := s.repo.AcquireGeminiWebLease(s.ctx, a.ID, "worker")
	s.Require().NoError(err)
	s.Require().True(lease)
	apply := func(version int64, ua string, want bool) {
		ok, err := s.repo.ImportGeminiWebSession(s.ctx, a.ID, version, map[string]any{"user_agent": ua})
		s.Require().NoError(err)
		s.Require().Equal(want, ok)
	}
	apply(1, "busy-import", false)
	ok, err := s.repo.CompareAndSwapGeminiWebRuntime(s.ctx, a.ID, 1, "worker", map[string]any{"user_agent": "worker-refresh"})
	s.Require().NoError(err)
	s.Require().True(ok)
	s.Require().NoError(s.repo.ReleaseGeminiWebLease(s.ctx, a.ID, "worker"))
	apply(1, "stale-import", false)
	// A pause and credential rotation after the import's read must survive.
	_, err = s.client.ExecContext(s.ctx, `UPDATE accounts SET schedulable=false, status='error',
		credentials=jsonb_set(credentials,'{api_key}','"rotated"') WHERE id=$1`, a.ID)
	s.Require().NoError(err)
	apply(2, "imported", true)
	apply(2, "competing-import", false)
	loaded, err := s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	s.Require().False(loaded.Schedulable)
	s.Require().Equal("error", loaded.Status)
	s.Require().Equal("rotated", loaded.Credentials["api_key"])
	s.Require().Equal(a.Credentials["model_mapping"], loaded.Credentials["model_mapping"])
	web := loaded.Credentials["gemini_web"].(map[string]any)
	s.Require().Equal("imported", web["runtime"].(map[string]any)["user_agent"])
	s.Require().Equal(float64(3), web["runtime"].(map[string]any)["version"])
	// An old ordinary editor must not resurrect its earlier runtime.
	loaded.Credentials["gemini_web"] = a.Credentials["gemini_web"]
	loaded.Name = "edited"
	s.Require().NoError(s.repo.Update(s.ctx, loaded))
	loaded, err = s.repo.GetByID(s.ctx, a.ID)
	s.Require().NoError(err)
	s.Require().Equal("imported", loaded.Credentials["gemini_web"].(map[string]any)["runtime"].(map[string]any)["user_agent"])
	// Repeat the target boundary in SQL, including a relay changed after handler read.
	for _, credentials := range []map[string]any{
		{"api_key": "plain"},
		{"gemini_web": map[string]any{}},
		{"gemini_web_relay": true, "gemini_web": web},
	} {
		b := mustCreateAccount(s.T(), s.client, &service.Account{Name: "non-worker", Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Credentials: credentials})
		version := int64(0)
		if credentials["gemini_web_relay"] == true {
			version = 3
		}
		ok, err := s.repo.ImportGeminiWebSession(s.ctx, b.ID, version, map[string]any{"user_agent": "reject"})
		s.Require().NoError(err)
		s.Require().False(ok)
	}
}

func TestGeminiWebConcurrentImportsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	a := mustCreateAccount(t, client, &service.Account{Name: "concurrent-import", Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"gemini_web": map[string]any{"runtime": map[string]any{"version": 1}}}})
	t.Cleanup(func() { require.NoError(t, client.Account.DeleteOneID(a.ID).Exec(ctx)) })
	r := newAccountRepositoryWithSQL(client, integrationDB, nil, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	won := make([]bool, 2)
	errs := make([]error, 2)
	for i := range won {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			won[i], errs[i] = r.ImportGeminiWebSession(ctx, a.ID, 1, map[string]any{"user_agent": []string{"first", "second"}[i]})
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.NotEqual(t, won[0], won[1], "exactly one import can commit expected version 1")
	loaded, err := r.GetByID(ctx, a.ID)
	require.NoError(t, err)
	runtime := loaded.Credentials["gemini_web"].(map[string]any)["runtime"].(map[string]any)
	winner := "second"
	if won[0] {
		winner = "first"
	}
	require.Equal(t, winner, runtime["user_agent"])
	require.Equal(t, float64(2), runtime["version"])
}

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
