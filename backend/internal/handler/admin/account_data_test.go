package admin

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestValidateDataAccount_ChannelTypeMustBeNonNegative(t *testing.T) {
	item := DataAccount{
		Name:        "acct-1",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: -1,
		Credentials: map[string]any{"api_key": "sk-test"},
	}

	err := validateDataAccount(item)
	if err == nil {
		t.Fatal("expected error when channel_type < 0")
	}
	if !strings.Contains(err.Error(), "channel_type must be >= 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}
