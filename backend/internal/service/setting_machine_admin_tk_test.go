//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type machineKeyRepo struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	err    error
}

func (r *machineKeyRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.values[key] = value
	return nil
}
func (r *machineKeyRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (r *machineKeyRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	out := map[string]string{}
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}
func (r *machineKeyRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	delete(r.values, key)
	return nil
}

func TestUS054_MachineKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := &machineKeyRepo{values: map[string]string{SettingKeyAdminAPIKey: "legacy-canary"}}
	svc := NewSettingService(repo, &config.Config{})
	meta, token, err := svc.CreateMachineAdminKey(ctx, 7, "nightly export", []string{"accounts:export", "proxies:export"}, 24)
	require.NoError(t, err)
	require.Equal(t, int64(7), meta.OwnerUserID)
	require.WithinDuration(t, meta.CreatedAt.Add(24*time.Hour), meta.ExpiresAt, time.Second)
	require.NotContains(t, repo.values[machineAdminSettingPrefix+meta.ID], token)
	require.NotContains(t, repo.values[machineAdminSettingPrefix+meta.ID], strings.Split(token, "_")[1])
	got, err := svc.AuthenticateMachineAdminKey(ctx, token)
	require.NoError(t, err)
	require.Equal(t, meta, got)
	// A fresh service represents another app process, with no process-local key cache.
	other := NewSettingService(repo, &config.Config{})
	listed, err := other.ListMachineAdminKeys(ctx)
	require.NoError(t, err)
	require.Equal(t, []MachineAdminKey{*meta}, listed)
	encoded, err := json.Marshal(listed)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "digest")
	require.NotContains(t, string(encoded), token)
	require.NotContains(t, string(encoded), "legacy-canary")
	_, err = other.AuthenticateMachineAdminKey(ctx, token+"x")
	require.ErrorIs(t, err, ErrInvalidMachineAdminKey)
	_, err = other.AuthenticateMachineAdminKey(ctx, token[:len(token)-1]+"!")
	require.ErrorIs(t, err, ErrInvalidMachineAdminKey)
	require.NoError(t, svc.RevokeMachineAdminKey(ctx, meta.ID))
	_, err = other.AuthenticateMachineAdminKey(ctx, token)
	require.ErrorIs(t, err, ErrInvalidMachineAdminKey)
	legacy, err := svc.GetAdminAPIKey(ctx)
	require.NoError(t, err)
	require.Equal(t, "legacy-canary", legacy)
}

func TestUS054_MachineKeyFailsClosed(t *testing.T) {
	ctx := context.Background()
	repo := &machineKeyRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	for _, scopes := range [][]string{nil, {"*"}, {"settings:write"}, {"accounts:read", "unknown"}} {
		_, token, err := svc.CreateMachineAdminKey(ctx, 1, "test", scopes, 1)
		require.Error(t, err)
		require.Empty(t, token)
	}
	for _, hours := range []int{0, -1, 2161} {
		_, _, err := svc.CreateMachineAdminKey(ctx, 1, "test", []string{"accounts:read"}, hours)
		require.Error(t, err)
	}
	for _, name := range []string{"", " \n", "log\ninjection", strings.Repeat("a", 81)} {
		_, _, err := svc.CreateMachineAdminKey(ctx, 1, name, []string{"accounts:read"}, 1)
		require.Error(t, err)
	}
	meta, token, err := svc.CreateMachineAdminKey(ctx, 1, "test", []string{"accounts:read"}, 1)
	require.NoError(t, err)
	key := machineAdminSettingPrefix + meta.ID
	original := repo.values[key]
	var record machineAdminKeyRecord
	require.NoError(t, json.Unmarshal([]byte(original), &record))
	record.ExpiresAt = time.Now().Add(-time.Second)
	expired, err := json.Marshal(record)
	require.NoError(t, err)
	for _, bad := range []string{"{", "{}", string(expired)} {
		repo.values[key] = bad
		_, err := svc.AuthenticateMachineAdminKey(ctx, token)
		require.ErrorIs(t, err, ErrInvalidMachineAdminKey)
	}
	repo.values[key] = original
	repo.err = errors.New("database offline")
	_, err = svc.AuthenticateMachineAdminKey(ctx, token)
	require.Equal(t, repo.err, err)
	_, issued, err := svc.CreateMachineAdminKey(ctx, 1, "failed", []string{"accounts:read"}, 1)
	require.Error(t, err)
	require.Empty(t, issued)
	require.Error(t, svc.RevokeMachineAdminKey(ctx, meta.ID))
}

func TestUS054_MachineKeysConcurrentIndependentRows(t *testing.T) {
	ctx := context.Background()
	repo := &machineKeyRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	old, _, err := svc.CreateMachineAdminKey(ctx, 1, "old", []string{"accounts:read"}, 1)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			_, _, err := svc.CreateMachineAdminKey(ctx, 1, "new", []string{"accounts:read"}, 1)
			errs <- err
		})
	}
	wg.Go(func() { errs <- svc.RevokeMachineAdminKey(ctx, old.ID) })
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	keys, err := svc.ListMachineAdminKeys(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 10)
	for _, key := range keys {
		require.NotEqual(t, old.ID, key.ID)
	}
}

func TestUS054_MachinePermissionMatrix(t *testing.T) {
	permissions := MachineAdminPermissions()
	var all []string
	for _, p := range permissions {
		all = append(all, p.Scope)
	}
	for _, p := range permissions {
		for _, route := range p.Routes {
			method, path, _ := strings.Cut(route, " ")
			require.True(t, MachineAdminAllows(all, method, path), route)
			var without []string
			for _, scope := range all {
				if scope != p.Scope {
					without = append(without, scope)
				}
			}
			require.False(t, MachineAdminAllows(without, method, path), "required scope %s for %s", p.Scope, route)
			require.False(t, MachineAdminAllows(all, method, path+"/unknown"), route)
		}
	}
	for _, path := range []string{"/api/v1/admin/settings", "/api/v1/admin/payment/config", "/api/v1/admin/users", "/api/v1/admin/settings/machine-admin-keys", "/api/v1/admin/backups/1/restore"} {
		require.False(t, MachineAdminAllows(all, "POST", path))
		require.False(t, MachineAdminAllows(all, "GET", path))
		require.False(t, MachineAdminAllowsStepUp(all, "GET", path))
	}
	// The metadata endpoint returns copies, not a writable policy registry.
	permissions[0].Routes[0] = "GET /api/v1/admin/settings"
	require.False(t, MachineAdminAllows(all, "GET", "/api/v1/admin/settings"))
}
