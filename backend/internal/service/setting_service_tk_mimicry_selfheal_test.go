//go:build unit

package service

import (
	"context"
	"encoding/json"
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

// The ratchet: a UA + manifest hot-updated NEWER than this binary's embedded
// baseline (via ops sync-runtime, ahead of the release train) must SURVIVE the
// tick. This is the production failure mode from the 2.1.163→2.1.165 bump where
// the directionless `!= wantUA` overwrite rolled the live fleet back every tick.
func TestEnsureClaudeCodeMimicryBaseline_PreservesNewerUA(t *testing.T) {
	doc, err := baseline.LoadHTTPMimicryBaseline()
	require.NoError(t, err)

	// A version strictly newer than the embedded baseline (major bump is safe
	// regardless of what the baseline cc_version happens to be).
	newerUA := "99.0.0"
	require.Equal(t, 1, CompareVersions(newerUA, doc.CCVersion),
		"test fixture must be newer than embedded baseline")

	// Build a manifest carrying the newer cc_version but otherwise the baseline betas.
	newerManifest := &ClaudeCodeHTTPMimicryManifest{
		SchemaVersion: doc.SchemaVersion,
		CCVersion:     newerUA,
		SonnetOpus:    doc.SonnetOpus,
		Haiku:         doc.Haiku,
	}
	encoded, err := json.Marshal(newerManifest)
	require.NoError(t, err)

	repo := &mimicrySettingRepoStub{values: map[string]string{
		SettingKeyClaudeCodeUserAgentVersion:    newerUA,
		SettingKeyClaudeCodeHTTPMimicryManifest: string(encoded),
	}}
	svc := &SettingService{settingRepo: repo}

	changed, err := svc.EnsureClaudeCodeMimicryBaseline(context.Background())
	require.NoError(t, err)
	require.False(t, changed, "newer-than-baseline values must NOT be clobbered")
	require.Equal(t, newerUA, repo.values[SettingKeyClaudeCodeUserAgentVersion],
		"hot-updated newer UA must survive the reconciler tick")
	require.Equal(t, 0, repo.sets, "no Set call when current values are newer")
}

// Mixed state: UA newer (preserve) but manifest still at the older baseline
// cc_version (heal). Each key ratchets on its own cc_version independently.
func TestEnsureClaudeCodeMimicryBaseline_PerKeyRatchet(t *testing.T) {
	doc, err := baseline.LoadHTTPMimicryBaseline()
	require.NoError(t, err)
	baseUA := NormalizeClaudeCodeUserAgentVersion(doc.CCVersion)
	newerUA := "99.0.0"

	// UA newer; manifest carries an OLDER cc_version → manifest should heal up
	// to baseline, UA should be left alone.
	olderManifest := &ClaudeCodeHTTPMimicryManifest{
		SchemaVersion: doc.SchemaVersion,
		CCVersion:     "1.0.0",
		SonnetOpus:    doc.SonnetOpus,
		Haiku:         doc.Haiku,
	}
	encoded, err := json.Marshal(olderManifest)
	require.NoError(t, err)

	repo := &mimicrySettingRepoStub{values: map[string]string{
		SettingKeyClaudeCodeUserAgentVersion:    newerUA,
		SettingKeyClaudeCodeHTTPMimicryManifest: string(encoded),
	}}
	svc := &SettingService{settingRepo: repo}

	changed, err := svc.EnsureClaudeCodeMimicryBaseline(context.Background())
	require.NoError(t, err)
	require.True(t, changed, "older manifest must heal even when UA is newer")
	require.Equal(t, newerUA, repo.values[SettingKeyClaudeCodeUserAgentVersion],
		"newer UA preserved independently of manifest healing")
	healed := ParseClaudeCodeHTTPMimicryManifest(repo.values[SettingKeyClaudeCodeHTTPMimicryManifest])
	require.NotNil(t, healed)
	require.Equal(t, baseUA, healed.CCVersion, "manifest healed up to baseline cc_version")
}
