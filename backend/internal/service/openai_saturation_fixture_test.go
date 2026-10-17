//go:build unit

package service

import (
	"context"
)

func openAIEdgeStub(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://upstream.example.test",
		},
	}
}

func grokEdgeStub(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":   "edge-grok-key",
			"base_url":  "https://upstream.example.test",
			"pool_mode": true,
		},
	}
}

type fakeOpenAISaturationCounterRL struct {
	incrementIDs []int64
	count        int64
	lastWindow   int
	batch        map[int64]int64
}

func (f *fakeOpenAISaturationCounterRL) IncrementSaturation(_ context.Context, accountID int64, windowSeconds int) (int64, error) {
	f.incrementIDs = append(f.incrementIDs, accountID)
	f.lastWindow = windowSeconds
	f.count++
	return f.count, nil
}

func (f *fakeOpenAISaturationCounterRL) GetSaturationBatch(_ context.Context, _ []int64, windowSeconds int) (map[int64]int64, error) {
	f.lastWindow = windowSeconds
	if f.batch != nil {
		return f.batch, nil
	}
	return nil, nil
}
