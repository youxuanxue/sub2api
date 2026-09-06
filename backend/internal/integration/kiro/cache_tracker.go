package kiro

import (
	"context"
	"sync"
	"time"
)

// CacheFingerprintStore persists per-session prefix fingerprints so a later
// request in the same conversation can attribute matching prompt tokens as
// cache_read for billing.
//
// Implementations must be safe for concurrent use. Get returns ok=false for
// missing or expired entries without treating that as an error.
type CacheFingerprintStore interface {
	Get(ctx context.Context, sessionKey, fingerprintHex string) (tokens int, ok bool, err error)
	Put(ctx context.Context, sessionKey, fingerprintHex string, tokens int, ttl time.Duration) error
}

type memoryFingerprintEntry struct {
	tokens    int
	expiresAt time.Time
}

// MemoryCacheFingerprintStore is a process-local store used for unit tests and
// as the default when Redis is unavailable. Expired entries are pruned lazily.
type MemoryCacheFingerprintStore struct {
	mu   sync.Mutex
	data map[string]map[string]memoryFingerprintEntry // session -> fp -> entry
}

// NewMemoryCacheFingerprintStore constructs an empty in-process store.
func NewMemoryCacheFingerprintStore() *MemoryCacheFingerprintStore {
	return &MemoryCacheFingerprintStore{
		data: make(map[string]map[string]memoryFingerprintEntry),
	}
}

// Get returns a non-expired fingerprint token count for the session.
func (s *MemoryCacheFingerprintStore) Get(_ context.Context, sessionKey, fingerprintHex string) (int, bool, error) {
	if s == nil {
		return 0, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(sessionKey, time.Now())
	byFP, ok := s.data[sessionKey]
	if !ok {
		return 0, false, nil
	}
	entry, ok := byFP[fingerprintHex]
	if !ok {
		return 0, false, nil
	}
	return entry.tokens, true, nil
}

// Put records a fingerprint. A longer TTL replaces a shorter one for the same key.
func (s *MemoryCacheFingerprintStore) Put(_ context.Context, sessionKey, fingerprintHex string, tokens int, ttl time.Duration) error {
	if s == nil || sessionKey == "" || fingerprintHex == "" || tokens <= 0 || ttl <= 0 {
		return nil
	}
	now := time.Now()
	expires := now.Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	byFP := s.data[sessionKey]
	if byFP == nil {
		byFP = make(map[string]memoryFingerprintEntry)
		s.data[sessionKey] = byFP
	}
	if existing, ok := byFP[fingerprintHex]; ok && existing.expiresAt.After(expires) {
		// Keep the longer TTL; refresh tokens to the latest estimate.
		byFP[fingerprintHex] = memoryFingerprintEntry{tokens: tokens, expiresAt: existing.expiresAt}
		return nil
	}
	byFP[fingerprintHex] = memoryFingerprintEntry{tokens: tokens, expiresAt: expires}
	return nil
}

func (s *MemoryCacheFingerprintStore) pruneLocked(sessionKey string, now time.Time) {
	byFP, ok := s.data[sessionKey]
	if !ok {
		return
	}
	for fp, entry := range byFP {
		if !entry.expiresAt.After(now) {
			delete(byFP, fp)
		}
	}
	if len(byFP) == 0 {
		delete(s.data, sessionKey)
	}
}
