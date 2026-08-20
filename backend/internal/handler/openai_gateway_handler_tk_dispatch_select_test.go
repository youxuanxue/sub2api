//go:build unit

package handler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestTkOpenAIDispatchSelectionFallbackModel(t *testing.T) {
	emptyPool := fmt.Errorf("%w supporting model: claude-opus-4-8 (total=4 eligible=0 excluded=1 model_unsupported=3)", service.ErrNoAvailableAccounts)
	unsupported := fmt.Errorf("%w: claude-opus-4-8 (total=3 eligible=0 model_unsupported=3)", service.ErrUnsupportedModel)

	cases := []struct {
		name         string
		mapped       string
		primary      string
		err          error
		wantModel    string
		wantFallback bool
	}{
		{
			name:         "empty pool after excluding claude relay falls back to opus mapped model",
			mapped:       "gpt-5.6-sol",
			primary:      "claude-opus-4-8",
			err:          emptyPool,
			wantModel:    "gpt-5.6-sol",
			wantFallback: true,
		},
		{
			name:         "unsupported original name still falls back when group has dispatch mapping",
			mapped:       "gpt-5.6-sol",
			primary:      "claude-opus-4-8",
			err:          unsupported,
			wantModel:    "gpt-5.6-sol",
			wantFallback: true,
		},
		{
			name:    "success does not fall back",
			mapped:  "gpt-5.6-sol",
			primary: "claude-opus-4-8",
			err:     nil,
		},
		{
			name:    "blank mapped model does not fall back",
			primary: "claude-opus-4-8",
			err:     emptyPool,
		},
		{
			name:    "same mapped and primary does not fall back",
			mapped:  "gpt-5.6-sol",
			primary: "gpt-5.6-sol",
			err:     emptyPool,
		},
		{
			name:    "compact-only exhaustion does not fall back",
			mapped:  "gpt-5.6-sol",
			primary: "claude-opus-4-8",
			err:     service.ErrNoAvailableCompactAccounts,
		},
		{
			name:    "unrelated scheduler error does not fall back",
			mapped:  "gpt-5.6-sol",
			primary: "claude-opus-4-8",
			err:     errors.New("database unavailable"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tkOpenAIDispatchSelectionFallbackModel(tc.mapped, tc.primary, tc.err)
			if ok != tc.wantFallback || got != tc.wantModel {
				t.Fatalf("fallback(%q,%q,%v) = (%q,%v), want (%q,%v)",
					tc.mapped, tc.primary, tc.err, got, ok, tc.wantModel, tc.wantFallback)
			}
		})
	}
}
