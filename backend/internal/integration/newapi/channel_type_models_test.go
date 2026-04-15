package newapi

import "testing"

// Ensures adaptor wiring returns the same non-empty catalog shape as new-api's channelId2Models init.
func TestChannelTypeModels_HasDefaults(t *testing.T) {
	t.Parallel()
	m := ChannelTypeModels()
	if len(m) == 0 {
		t.Fatal("expected non-empty channel type → models map")
	}
	var total int
	for _, list := range m {
		total += len(list)
	}
	if total == 0 {
		t.Fatal("expected at least one default model id across channel types")
	}
}
