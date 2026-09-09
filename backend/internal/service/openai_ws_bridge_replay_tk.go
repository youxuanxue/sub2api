package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const OpenAIWSBridgeReplayMaxBytes = 1 << 20

var errOpenAIWSBridgeReplayTooLarge = errors.New("websocket bridge replay exceeds size limit")

// OpenAIWSBridgeReplayCache retains bounded, expiring history for stateless
// Grok HTTP bridges. Ownership and current account authorization remain with
// ResolveCandidateContinuation and CandidateRequest.
type OpenAIWSBridgeReplayCache interface {
	SetOpenAIWSBridgeReplay(context.Context, string, []byte, time.Duration) error
	GetOpenAIWSBridgeReplay(context.Context, string) ([]byte, error)
}

type openAIWSBridgeReplay struct {
	Version   int               `json:"version"`
	AccountID int64             `json:"account_id"`
	Input     []json.RawMessage `json:"input"`
}

func openAIWSBridgeReplayKey(ctx context.Context, responseID string) string {
	identity, ok := candidateIdentityFromContext(ctx)
	if !ok || normalizeOpenAIWSResponseID(responseID) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(candidateResponseID(identity.userID, responseID)))
	return hex.EncodeToString(sum[:])
}

func (s *OpenAIGatewayService) saveOpenAIWSBridgeReplay(ctx context.Context, accountID int64, responseID string, input []json.RawMessage) error {
	key := openAIWSBridgeReplayKey(ctx, responseID)
	if key == "" {
		return nil
	}
	cache, ok := s.cache.(OpenAIWSBridgeReplayCache)
	if !ok {
		return errors.New("websocket bridge replay cache unavailable")
	}
	raw, err := json.Marshal(openAIWSBridgeReplay{Version: 1, AccountID: accountID, Input: input})
	if err != nil {
		return err
	}
	if len(raw) > OpenAIWSBridgeReplayMaxBytes {
		return errOpenAIWSBridgeReplayTooLarge
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return cache.SetOpenAIWSBridgeReplay(cacheCtx, key, raw, s.openAIWSResponseStickyTTL())
}

func (s *OpenAIGatewayService) loadOpenAIWSBridgeReplay(ctx context.Context, accountID int64, responseID string) ([]json.RawMessage, error) {
	key := openAIWSBridgeReplayKey(ctx, responseID)
	if key == "" {
		return nil, ErrCandidateContinuationUnavailable
	}
	cache, ok := s.cache.(OpenAIWSBridgeReplayCache)
	if !ok {
		return nil, errors.New("websocket bridge replay cache unavailable")
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	raw, err := cache.GetOpenAIWSBridgeReplay(cacheCtx, key)
	if err != nil {
		return nil, fmt.Errorf("read websocket bridge replay: %w", err)
	}
	if len(raw) == 0 {
		return nil, ErrCandidateContinuationUnavailable
	}
	var replay openAIWSBridgeReplay
	if len(raw) > OpenAIWSBridgeReplayMaxBytes || json.Unmarshal(raw, &replay) != nil || replay.Version != 1 || replay.AccountID != accountID || len(replay.Input) == 0 {
		return nil, ErrCandidateContinuationUnavailable
	}
	return replay.Input, nil
}
