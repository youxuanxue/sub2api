//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// fakeSettingPubSub is an in-memory SettingPubSub for service-layer tests
// (the redis round-trip is covered in repository/setting_pubsub_test.go). It
// records Publish calls and synchronously invokes the subscriber handler when
// Fire() is called, so tests can drive a "remote refresh" deterministically.
type fakeSettingPubSub struct {
	mu           sync.Mutex
	handler      func()
	publishCalls atomic.Int64
}

func (f *fakeSettingPubSub) Publish(context.Context) error {
	f.publishCalls.Add(1)
	return nil
}

func (f *fakeSettingPubSub) Subscribe(_ context.Context, handler func()) {
	f.mu.Lock()
	f.handler = handler
	f.mu.Unlock()
}

func (f *fakeSettingPubSub) Fire() {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	if h != nil {
		h()
	}
}

// pubsubSettingRepoStub counts GetAll calls so a test can assert a refresh
// drove a DB reload. All other methods are unused.
type pubsubSettingRepoStub struct {
	getAllCalls atomic.Int64
}

func (s *pubsubSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}
func (s *pubsubSettingRepoStub) GetValue(context.Context, string) (string, error) {
	return "", ErrSettingNotFound
}
func (s *pubsubSettingRepoStub) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}
func (s *pubsubSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *pubsubSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}
func (s *pubsubSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	s.getAllCalls.Add(1)
	return map[string]string{}, nil
}
func (s *pubsubSettingRepoStub) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func newPubSubTestService() (*SettingService, *pubsubSettingRepoStub, *fakeSettingPubSub) {
	repo := &pubsubSettingRepoStub{}
	bus := &fakeSettingPubSub{}
	svc := NewSettingService(repo, &config.Config{})
	return svc, repo, bus
}

// A refresh signal must drive a DB reload on this replica so a UA (or any
// SystemSettings) edit on a peer is reflected within seconds, not the 60s TTL.
func TestSettingsPubSub_RemoteRefreshReloadsFromDB(t *testing.T) {
	svc, repo, bus := newPubSubTestService()
	svc.EnableSettingsPubSub(context.Background(), bus)

	before := repo.getAllCalls.Load()
	bus.Fire()
	require.Greater(t, repo.getAllCalls.Load(), before, "remote refresh did not trigger a DB reload")
}

// A local settings write (refreshCachedSettings) must publish a refresh so peer
// replicas reload.
func TestSettingsPubSub_LocalWritePublishes(t *testing.T) {
	svc, _, bus := newPubSubTestService()
	svc.EnableSettingsPubSub(context.Background(), bus)

	svc.refreshCachedSettings(&SystemSettings{})
	require.Equal(t, int64(1), bus.publishCalls.Load(), "local write did not publish exactly one refresh")
}

// applyRemoteSettingsRefresh must NOT re-publish (suppress guard), otherwise a
// single edit would loop forever across replicas.
func TestSettingsPubSub_RemoteApplyDoesNotRepublish(t *testing.T) {
	svc, _, bus := newPubSubTestService()
	svc.EnableSettingsPubSub(context.Background(), bus)

	bus.Fire() // simulate a received remote refresh → applyRemoteSettingsRefresh
	require.Equal(t, int64(0), bus.publishCalls.Load(), "remote apply re-published (loop)")
}

// Disabled pub/sub (nil bus) must be a no-op, not a panic.
func TestSettingsPubSub_DisabledIsNoop(t *testing.T) {
	svc := NewSettingService(&pubsubSettingRepoStub{}, &config.Config{})
	svc.EnableSettingsPubSub(context.Background(), nil)
	require.Nil(t, svc.settingsPubSub)
	svc.notifySettingsPubSub() // must not panic
}
