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
	panic("unused")
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
	t.Run("默认关闭（设置缺失=零回归）", func(t *testing.T) {
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
	t.Run("值为 true 时开启", func(t *testing.T) {
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "true",
		}}, &config.Config{})
		require.True(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
	t.Run("值为 false 时关闭", func(t *testing.T) {
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "false",
		}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
	t.Run("值为非法字符串时关闭", func(t *testing.T) {
		svc := NewSettingService(&canonicalIngressStrictSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicCanonicalIngressStrictEnabled: "yes",
		}}, &config.Config{})
		require.False(t, svc.IsAnthropicCanonicalIngressStrictEnabled(context.Background()))
	})
}
