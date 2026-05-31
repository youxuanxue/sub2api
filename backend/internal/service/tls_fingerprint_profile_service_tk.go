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

// GetOrUpsertByName upserts a profile keyed by Name (the equivalent of the
// orchestrator's `ON CONFLICT (name) DO UPDATE`): if a row with desired.Name
// exists it is updated in place (preserving its ID), otherwise it is created.
// Returns the persisted profile WITH a valid ID. Going through the service (not
// the repo directly) ensures the in-process local cache is invalidated so
// ResolveTLSProfile sees the new/updated row immediately — this is what prevents
// the silent fallback to the built-in default ClientHello after a tier apply.
func (s *TLSFingerprintProfileService) GetOrUpsertByName(ctx context.Context, desired *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
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
		desired.ID = existing.ID
		return s.Update(ctx, desired)
	}
	return s.Create(ctx, desired)
}
