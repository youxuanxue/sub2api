package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/model"
)

// GetByName returns the TLS fingerprint profile whose Name matches exactly, or
// nil if none exists. Linear scan over List() — the profile table is tiny (a
// handful of canonical profiles) so this is cheap and avoids a new repo method.
func (s *TLSFingerprintProfileService) GetByName(ctx context.Context, name string) (*model.TLSFingerprintProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("tls profile name is empty")
	}
	profiles, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		if p != nil && p.Name == name {
			return p, nil
		}
	}
	return nil, nil
}

// GetOrCreateByName ensures a profile row with desired.Name EXISTS and returns
// it with a valid ID. If the row already exists it is returned AS-IS — its
// content is deliberately NOT rewritten from the caller's (embedded-baseline)
// copy. Content ownership is split:
//
//   - Row EXISTENCE + account binding: this service / ApplyTier / the account
//     baseline reconciler. Creating a missing row is what prevents the silent
//     fallback to the built-in default ClientHello after a tier apply.
//   - Row CONTENT (the actual ClientHello fields): the ops pipeline
//     (manage-anthropic-config apply / remediate-guard-drift), which upserts the
//     fleet to the latest captured canonical fingerprint.
//
// The previous update-in-place semantics let a node running an OLDER binary
// (older embedded TLS baseline) silently roll the canonical row BACK on any
// ApplyTier click or account-infra-drift tick after the ops pipeline had pushed
// a newer fingerprint fleet-wide — the same stale-embedded-baseline failure
// mode as the mimicry UA ratchet (see EnsureClaudeCodeMimicryBaseline). Going
// through the service (not the repo directly) keeps the in-process local cache
// invalidated on create so ResolveTLSProfile sees the new row immediately.
func (s *TLSFingerprintProfileService) GetOrCreateByName(ctx context.Context, desired *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	if desired == nil {
		return nil, fmt.Errorf("tls profile is nil")
	}
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetByName(ctx, desired.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	return s.Create(ctx, desired)
}
