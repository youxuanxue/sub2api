//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOpenAITrainingDisabled(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		body         string
		wantDisabled bool
		wantOK       bool
	}{
		{name: "training_allowed false -> disabled", body: `{"training_allowed":false}`, wantDisabled: true, wantOK: true},
		{name: "training_allowed true -> not disabled", body: `{"training_allowed":true}`, wantDisabled: false, wantOK: true},
		{
			name:   "field absent (other training flags only) -> inconclusive",
			body:   `{"codex_training_allowed":false,"video_training_allowed":false}`,
			wantOK: false,
		},
		{name: "empty object -> inconclusive", body: `{}`, wantOK: false},
		{name: "not json -> inconclusive", body: `<!doctype html><html>just a moment</html>`, wantOK: false},
		{name: "empty body -> inconclusive", body: ``, wantOK: false},
		{
			name:         "full settings payload, training off",
			body:         `{"training_allowed":false,"codex_training_allowed":false,"precise_location_allowed":true}`,
			wantDisabled: true,
			wantOK:       true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disabled, ok := parseOpenAITrainingDisabled([]byte(tc.body))
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantDisabled, disabled)
		})
	}
}
