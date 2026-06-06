//go:build unit

package service

import (
	"errors"
	"strings"
	"testing"
)

func TestTkSelectionFailedDueToUnsupportedModel(t *testing.T) {
	cases := []struct {
		name  string
		stats selectionFailureStats
		want  bool
	}{
		{
			name:  "pure unsupported model -> true",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 5},
			want:  true,
		},
		{
			name:  "unsupported plus a model-rate-limited candidate -> false (capacity)",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 4, ModelRateLimited: 1},
			want:  false,
		},
		{
			name:  "unsupported plus an unschedulable candidate -> false (may support once recovered)",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 4, Unschedulable: 1},
			want:  false,
		},
		{
			name:  "an eligible candidate exists -> false",
			stats: selectionFailureStats{Total: 5, ModelUnsupported: 4, Eligible: 1},
			want:  false,
		},
		{
			name:  "no model-unsupported at all -> false",
			stats: selectionFailureStats{Total: 5, Unschedulable: 5},
			want:  false,
		},
		{
			name:  "empty stats -> false",
			stats: selectionFailureStats{},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkSelectionFailedDueToUnsupportedModel(tc.stats); got != tc.want {
				t.Fatalf("tkSelectionFailedDueToUnsupportedModel(%+v) = %v, want %v", tc.stats, got, tc.want)
			}
		})
	}
}

func TestTkWrapSelectionFailure(t *testing.T) {
	t.Run("empty model returns bare ErrNoAvailableAccounts", func(t *testing.T) {
		err := tkWrapSelectionFailure("", selectionFailureStats{Total: 3, ModelUnsupported: 3})
		if !errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("want ErrNoAvailableAccounts, got %v", err)
		}
		if errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("empty model must not be classified as unsupported model: %v", err)
		}
	})

	t.Run("pure unsupported model returns ErrUnsupportedModel with model name", func(t *testing.T) {
		err := tkWrapSelectionFailure("opus", selectionFailureStats{Total: 5, ModelUnsupported: 5})
		if !errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("want ErrUnsupportedModel, got %v", err)
		}
		if errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("unsupported model must be distinct from ErrNoAvailableAccounts: %v", err)
		}
		if !strings.Contains(err.Error(), "opus") {
			t.Fatalf("error should carry the model name, got %q", err.Error())
		}
		// Must NOT contain the "no available accounts" phrase, or
		// handler.isOpsNoAvailableAccountError would mislabel it as routing-capacity.
		if strings.Contains(strings.ToLower(err.Error()), "no available accounts") {
			t.Fatalf("unsupported-model message must not contain 'no available accounts': %q", err.Error())
		}
	})

	t.Run("capacity failure returns ErrNoAvailableAccounts (not unsupported)", func(t *testing.T) {
		err := tkWrapSelectionFailure("claude-opus-4-8", selectionFailureStats{Total: 5, ModelUnsupported: 4, ModelRateLimited: 1})
		if !errors.Is(err, ErrNoAvailableAccounts) {
			t.Fatalf("want ErrNoAvailableAccounts, got %v", err)
		}
		if errors.Is(err, ErrUnsupportedModel) {
			t.Fatalf("capacity failure must not be classified as unsupported model: %v", err)
		}
	})
}
