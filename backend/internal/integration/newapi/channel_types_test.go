//go:build unit

package newapi

import "testing"

func TestListChannelTypes(t *testing.T) {
	types := ListChannelTypes()
	if len(types) == 0 {
		t.Fatalf("expected non-empty channel type catalog")
	}

	last := 0
	foundDeepSeek := false
	for i, item := range types {
		if item.ChannelType <= 0 {
			t.Fatalf("expected positive channel_type at index %d, got %d", i, item.ChannelType)
		}
		if i > 0 && item.ChannelType < last {
			t.Fatalf("expected sorted channel types, got %d then %d", last, item.ChannelType)
		}
		if item.Name == "" {
			t.Fatalf("expected non-empty name for channel_type=%d", item.ChannelType)
		}
		if item.Name == "DeepSeek" {
			foundDeepSeek = true
		}
		last = item.ChannelType
	}
	if !foundDeepSeek {
		t.Fatalf("expected DeepSeek channel type in catalog")
	}
}
