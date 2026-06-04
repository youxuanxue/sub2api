//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/baseline"

	"github.com/stretchr/testify/require"
)

// mimicrySettingRepoStub implements just GetValue/Set (embeds the interface so
// the unused methods panic if unexpectedly called).
type mimicrySettingRepoStub struct {
	SettingRepository
	values map[string]string
	sets   int
}

func (s *mimicrySettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (s *mimicrySettingRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	s.sets++
	return nil
}

func TestEnsureClaudeCodeMimicryBaseline_WritesWhenUnsetThenIdempotent(t *testing.T) {
	doc, err := baseline.LoadHTTPMimicryBaseline()
	require.NoError(t, err)
	wantUA := NormalizeClaudeCodeUserAgentVersion(doc.CCVersion)

	repo := &mimicrySettingRepoStub{values: map[string]string{}}
	svc := &SettingService{settingRepo: repo}

	// 1) unset → both keys written.
	changed, err := svc.EnsureClaudeCodeMimicryBaseline(context.Background())
	require.NoError(t, err)
	require.True(t, changed, "unset settings must be self-healed")
	require.Equal(t, wantUA, repo.values[SettingKeyClaudeCodeUserAgentVersion])
	m := ParseClaudeCodeHTTPMimicryManifest(repo.values[SettingKeyClaudeCodeHTTPMimicryManifest])
	require.NotNil(t, m, "written manifest must parse")
	require.Equal(t, wantUA, m.CCVersion)
	require.NotEmpty(t, m.SonnetOpus)
	require.NotEmpty(t, m.Haiku)

	// 2) idempotent: aligned → no further writes (guards the every-tick-write loop).
	setsAfterFirst := repo.sets
	changed2, err := svc.EnsureClaudeCodeMimicryBaseline(context.Background())
	require.NoError(t, err)
	require.False(t, changed2, "aligned settings must NOT be rewritten")
	require.Equal(t, setsAfterFirst, repo.sets, "no extra Set calls on aligned state")
}

func TestEnsureClaudeCodeMimicryBaseline_HealsStaleUA(t *testing.T) {
	repo := &mimicrySettingRepoStub{values: map[string]string{
		SettingKeyClaudeCodeUserAgentVersion: "1.0.0", // stale
	}}
	svc := &SettingService{settingRepo: repo}
	changed, err := svc.EnsureClaudeCodeMimicryBaseline(context.Background())
	require.NoError(t, err)
	require.True(t, changed)

	doc, _ := baseline.LoadHTTPMimicryBaseline()
	require.Equal(t, NormalizeClaudeCodeUserAgentVersion(doc.CCVersion), repo.values[SettingKeyClaudeCodeUserAgentVersion])
}
