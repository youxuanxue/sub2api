package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type canonicalIngressStrictSettingRepoStub struct{ values map[string]string }

func (s *canonicalIngressStrictSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unused")
}
func (s *canonicalIngressStrictSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (s *canonicalIngressStrictSettingRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unused")
}
func (s *canonicalIngressStrictSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s *canonicalIngressStrictSettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unused")
}
func (s *canonicalIngressStrictSettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unused")
}
func (s *canonicalIngressStrictSettingRepoStub) Delete(ctx context.Context, key string) error {
	panic("unused")
}

func TestSettingService_IsAnthropicCanonicalIngressStrictEnabled(t *testing.T) {
	// The value rides the shared package-level gatewayForwardingCache; reset it
	// (and the singleflight) per subtest so each case reads through its own stub.
	resetCache := func() {
		gatewayForwardingSF.Forget("gateway_forwarding")
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	}
	t.Cleanup(resetCache)
	t.Run("默认关闭（设置缺失=零回归）", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
	t.Run("值为 true 时开启", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "true",
		}}, &config.Config{})
		require.True(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
	t.Run("值为 false 时关闭", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "false",
		}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
	t.Run("值为非法字符串时关闭", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "yes",
		}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
}

func TestSettingService_IsAnthropicCanonicalHaikuMimicryEnabled(t *testing.T) {
	resetCache := func() {
		gatewayForwardingSF.Forget("gateway_forwarding")
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	}
	t.Cleanup(resetCache)
	t.Run("默认关闭（设置缺失=零回归）", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalHaikuMimicryEnabled(context.Background()))
	})
	t.Run("值为 true 时开启", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalHaikuMimicryEnabled: "true",
		}}, &config.Config{})
		require.True(t, svc.IsAnthropicCanonicalHaikuMimicryEnabled(context.Background()))
	})
}

// TestSettingService_CanonicalToggles_Orthogonal pins the core design contract of
// the PR #691 direction revision: the two canonical toggles are INDEPENDENT.
// Specifically the "admit and launder" config (relax cc_only, route non-CC to a
// canonical fallback) needs HaikuMimicry=ON while IngressStrict stays OFF — a
// single switch could not express this, which is why they were split.
func TestSettingService_CanonicalToggles_Orthogonal(t *testing.T) {
	resetCache := func() {
		gatewayForwardingSF.Forget("gateway_forwarding")
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	}
	t.Cleanup(resetCache)

	t.Run("haiku 补全开、入口拒绝关（admit-and-launder 配置）", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalHaikuMimicryEnabled:  "true",
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "false",
		}}, &config.Config{})
		require.True(t, svc.IsAnthropicCanonicalHaikuMimicryEnabled(context.Background()), "haiku launder must be ON")
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()), "ingress reject must stay OFF so non-CC clients are admitted")
	})

	t.Run("入口拒绝开、haiku 补全关（reject-at-door 配置）", func(t *testing.T) {
		resetCache()
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "true",
			SettingKeyAnthropicCanonicalHaikuMimicryEnabled:  "false",
		}}, &config.Config{})
		require.True(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
		require.False(t, svc.IsAnthropicCanonicalHaikuMimicryEnabled(context.Background()))
	})
}
