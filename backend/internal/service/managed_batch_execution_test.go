//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
)

func TestManagedBatchExecutionContextRequiresFrozenSelection(t *testing.T) {
	if _, ok := ManagedBatchExecutionFromContext(context.Background()); ok {
		t.Fatal("unbound context must not be executable")
	}
	account := &Account{ID: 42}
	key := &APIKey{ID: 9, UserID: 7}
	ctx := WithManagedBatchExecution(context.Background(), ManagedBatchExecution{
		APIKey: key, Selection: &AccountSelectionResult{Account: account},
	})
	got, ok := ManagedBatchExecutionFromContext(ctx)
	if !ok || got.APIKey != key || got.Selection.Account != account {
		t.Fatalf("execution context = %+v, ok=%v", got, ok)
	}
}

func TestManagedResponsesRunnerRejectsMissingFrozenPlan(t *testing.T) {
	runner := &ManagedResponsesRunner{Router: NewProtocolRouter(), Gateway: &OpenAIGatewayService{}}
	_, err := runner.Run(context.Background(), batchadapter.ProviderRequest{AccountID: 42, Body: []byte(`{"model":"gpt-5"}`)})
	if err != ErrManagedBatchExecutionUnavailable {
		t.Fatalf("missing plan error = %v", err)
	}
}
