package service

import (
	"context"
	"time"
)

// VideoTaskCache is the storage contract for the video-task registry (the
// public-task-id → upstream-task routing pin). The interface lives in the
// service package so the dispatch / handler code can depend on a behaviour,
// not on a Redis client. The concrete implementation lives in the repository
// package (Redis-backed, in-memory fallback for unit tests); see
// repository.NewVideoTaskCache.
//
// This separation is the standing TokenKey rule: service code must NOT
// import `github.com/redis/go-redis/v9` (enforced by the depguard linter).
//
// TTL is owned by the implementation; callers do not pass an expiry. The
// Redis-backed impl uses a 24h fixed TTL — sufficient for any reasonable
// upstream task duration and bounded enough that long-tail tasks don't
// occupy storage forever.
type VideoTaskCache interface {
	// Save persists the record. Implementations MUST stamp CreatedAt when
	// zero. A non-nil error means the caller passed an invalid record
	// (nil pointer / empty PublicTaskID); transient backend errors are
	// the implementation's problem and MUST be logged + swallowed (the
	// upstream task has already been billed by the time Save is called,
	// so failing the submit would orphan it).
	Save(ctx context.Context, record *VideoTaskRecord) error

	// Lookup returns the record if present + owned by no-one-in-particular
	// (caller checks ownership). On miss / backend outage returns
	// (nil, false) — implementations MUST NOT silently fall back to a
	// stale local cache when the canonical store is unreachable.
	Lookup(ctx context.Context, publicTaskID string) (*VideoTaskRecord, bool)

	// Delete removes the record. Used when the upstream reports a terminal
	// status. No-op if the id is unknown.
	Delete(ctx context.Context, publicTaskID string)
}

// VideoTaskRecord is the minimum the bridge needs to call FetchTask later.
// AccountID + ChannelType pin the routing target; APIKey is captured at
// submit time because account credentials may rotate before the user polls.
type VideoTaskRecord struct {
	PublicTaskID   string    `json:"public_task_id"`
	UpstreamTaskID string    `json:"upstream_task_id"`
	AccountID      int64     `json:"account_id"`
	UserID         int64     `json:"user_id"`
	GroupID        int64     `json:"group_id"`
	APIKeyID       int64     `json:"api_key_id"`
	ChannelType    int       `json:"channel_type"`
	Platform       string    `json:"platform"`
	BaseURL        string    `json:"base_url"`
	APIKey         string    `json:"api_key"`
	OriginModel    string    `json:"origin_model"`
	UpstreamModel  string    `json:"upstream_model"`
	CreatedAt      time.Time `json:"created_at"`
}
