package service

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type cachedClaudeCodeUserAgentVersion struct {
	version   string
	expiresAt int64
}

const claudeCodeUserAgentVersionCacheTTL = 60 * time.Second
const claudeCodeUserAgentVersionErrorTTL = 5 * time.Second
const claudeCodeUserAgentVersionDBTimeout = 5 * time.Second

type cachedClaudeCodeHTTPMimicryManifest struct {
	manifest  *ClaudeCodeHTTPMimicryManifest
	expiresAt int64
}

const claudeCodeMimicryManifestCacheTTL = 60 * time.Second
const claudeCodeMimicryManifestErrorTTL = 5 * time.Second
const claudeCodeMimicryManifestDBTimeout = 5 * time.Second

// GetClaudeCodeUserAgentVersion returns the Claude Code canonical OAuth CLI
// version used by the runtime UA resolver.
func (s *SettingService) GetClaudeCodeUserAgentVersion(ctx context.Context) string {
	fallback := GetDefaultClaudeCodeUserAgentVersion()
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.claudeCodeUAVersionCache.Load().(*cachedClaudeCodeUserAgentVersion); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.version
		}
	}

	result, _, _ := s.claudeCodeUAVersionSF.Do("claude_code_user_agent_version", func() (any, error) {
		if cached, ok := s.claudeCodeUAVersionCache.Load().(*cachedClaudeCodeUserAgentVersion); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.version, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeCodeUserAgentVersionDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyClaudeCodeUserAgentVersion)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("failed to get claude code user agent version setting", "error", err)
			s.claudeCodeUAVersionCache.Store(&cachedClaudeCodeUserAgentVersion{
				version:   fallback,
				expiresAt: time.Now().Add(claudeCodeUserAgentVersionErrorTTL).UnixNano(),
			})
			return fallback, nil
		}
		version := NormalizeClaudeCodeUserAgentVersion(value)
		if version == "" {
			version = fallback
		}
		s.claudeCodeUAVersionCache.Store(&cachedClaudeCodeUserAgentVersion{
			version:   version,
			expiresAt: time.Now().Add(claudeCodeUserAgentVersionCacheTTL).UnixNano(),
		})
		return version, nil
	})
	if version, ok := result.(string); ok && version != "" {
		return version
	}
	return fallback
}

// GetClaudeCodeMimicryBetas returns runtime OAuth mimicry beta lists from
// settings.claude_code_http_mimicry_manifest. ok=false when unset or invalid.
func (s *SettingService) GetClaudeCodeMimicryBetas(ctx context.Context) (sonnetOpus, haiku []string, ok bool) {
	if s == nil || s.settingRepo == nil {
		return nil, nil, false
	}
	if cached, hit := s.claudeCodeMimicryManifestCache.Load().(*cachedClaudeCodeHTTPMimicryManifest); hit && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			if cached.manifest != nil {
				return cached.manifest.SonnetOpus, cached.manifest.Haiku, true
			}
			return nil, nil, false
		}
	}

	result, _, _ := s.claudeCodeMimicryManifestSF.Do("claude_code_http_mimicry_manifest", func() (any, error) {
		if cached, hit := s.claudeCodeMimicryManifestCache.Load().(*cachedClaudeCodeHTTPMimicryManifest); hit && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				if cached.manifest != nil {
					return cached.manifest, nil
				}
				return (*ClaudeCodeHTTPMimicryManifest)(nil), nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeCodeMimicryManifestDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyClaudeCodeHTTPMimicryManifest)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("failed to get claude code mimicry manifest setting", "error", err)
			s.claudeCodeMimicryManifestCache.Store(&cachedClaudeCodeHTTPMimicryManifest{
				manifest:  nil,
				expiresAt: time.Now().Add(claudeCodeMimicryManifestErrorTTL).UnixNano(),
			})
			return (*ClaudeCodeHTTPMimicryManifest)(nil), nil
		}
		manifest := ParseClaudeCodeHTTPMimicryManifest(value)
		s.claudeCodeMimicryManifestCache.Store(&cachedClaudeCodeHTTPMimicryManifest{
			manifest:  manifest,
			expiresAt: time.Now().Add(claudeCodeMimicryManifestCacheTTL).UnixNano(),
		})
		return manifest, nil
	})
	manifest, _ := result.(*ClaudeCodeHTTPMimicryManifest)
	if manifest == nil {
		return nil, nil, false
	}
	return manifest.SonnetOpus, manifest.Haiku, true
}
