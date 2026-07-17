//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// websearchUnsetSettingRepo implements SettingRepository and always reports the
// websearch key as unset (ErrSettingNotFound) — the fresh-deployment state.
type websearchUnsetSettingRepo struct{}

func (websearchUnsetSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (websearchUnsetSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", ErrSettingNotFound
}
func (websearchUnsetSettingRepo) Set(context.Context, string, string) error { return nil }
func (websearchUnsetSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (websearchUnsetSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (websearchUnsetSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (websearchUnsetSettingRepo) Delete(context.Context, string) error { return nil }

// An unset websearch-emulation setting must read as an empty (disabled) config
// with NO error — otherwise the GET handler maps the error to 404 and every
// account create/edit modal + channels/settings page 404s on a fresh deploy.
func TestGetWebSearchEmulationConfig_UnsetReturnsEmptyNoError(t *testing.T) {
	// Defensively expire any process cache a prior test may have populated.
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{
		config:    &WebSearchEmulationConfig{},
		expiresAt: 0,
	})

	svc := &SettingService{settingRepo: websearchUnsetSettingRepo{}}
	cfg, err := svc.GetWebSearchEmulationConfig(context.Background())

	require.NoError(t, err, "unset setting must not surface as an error (would become a 404)")
	require.NotNil(t, cfg)
	require.False(t, cfg.Enabled, "unset config is disabled")
	require.Empty(t, cfg.Providers, "unset config has no providers")
}

// A real DB error (not ErrSettingNotFound) must still propagate.
func TestGetWebSearchEmulationConfig_RealErrorPropagates(t *testing.T) {
	webSearchEmulationCache.Store(&cachedWebSearchEmulationConfig{
		config:    &WebSearchEmulationConfig{},
		expiresAt: 0,
	})

	svc := &SettingService{settingRepo: websearchErrSettingRepo{err: errors.New("redis down")}}
	_, err := svc.GetWebSearchEmulationConfig(context.Background())
	require.Error(t, err, "non-not-found errors must still propagate")
}

type websearchErrSettingRepo struct{ err error }

func (r websearchErrSettingRepo) Get(context.Context, string) (*Setting, error) { return nil, r.err }
func (r websearchErrSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", r.err
}
func (websearchErrSettingRepo) Set(context.Context, string, string) error { return nil }
func (websearchErrSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (websearchErrSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (websearchErrSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (websearchErrSettingRepo) Delete(context.Context, string) error { return nil }
