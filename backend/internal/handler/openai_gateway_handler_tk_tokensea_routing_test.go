//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
)

func Test_tkShouldApplyMessagesDispatchBodyMapping(t *testing.T) {
	tokenseaExtra := map[string]any{
		openai_compat.ExtraKeyNativeMessagesSupported: true,
		openai_compat.ExtraKeyResponsesSupported:      false,
	}
	ccOnlyExtra := map[string]any{
		openai_compat.ExtraKeyResponsesSupported: false,
	}

	cases := []struct {
		name    string
		account *service.Account
		want    bool
	}{
		{
			name:    "nil account keeps dispatch mapping",
			account: nil,
			want:    true,
		},
		{
			name: "oauth account keeps dispatch mapping",
			account: &service.Account{
				Type: service.AccountTypeOAuth,
			},
			want: true,
		},
		{
			name: "tokensea native messages relay skips dispatch mapping",
			account: &service.Account{
				Type:  service.AccountTypeAPIKey,
				Extra: tokenseaExtra,
			},
			want: false,
		},
		{
			name: "api key cc-only relay skips dispatch mapping",
			account: &service.Account{
				Type:  service.AccountTypeAPIKey,
				Extra: ccOnlyExtra,
			},
			want: false,
		},
		{
			name: "generic api key relay keeps dispatch mapping",
			account: &service.Account{
				Type: service.AccountTypeAPIKey,
				Extra: map[string]any{
					openai_compat.ExtraKeyResponsesSupported: true,
				},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkShouldApplyMessagesDispatchBodyMapping(tc.account); got != tc.want {
				t.Fatalf("tkShouldApplyMessagesDispatchBodyMapping() = %v, want %v", got, tc.want)
			}
		})
	}
}

func Test_tkApplyResponsesDispatchModelMapping_skippedForTokenseaRelay(t *testing.T) {
	openaiDefaults, ok := service.TkMessagesDispatchPlatformDefaults(service.PlatformOpenAI)
	if !ok {
		t.Fatal("openai platform_defaults missing from tk_messages_dispatch_family_registry.json")
	}
	apiKey := &service.APIKey{Group: &service.Group{
		ID:       2,
		Platform: service.PlatformOpenAI,
	}}
	body := []byte(`{"model":"claude-haiku-4-5-20251001","input":[]}`)
	replace := func(body []byte, newModel string) []byte {
		return service.ReplaceModelInBody(body, newModel)
	}

	tokensea := &service.Account{
		Type: service.AccountTypeAPIKey,
		Extra: map[string]any{
			openai_compat.ExtraKeyNativeMessagesSupported: true,
			openai_compat.ExtraKeyResponsesSupported:      false,
		},
	}

	dispatchBody := body
	if tkShouldApplyMessagesDispatchBodyMapping(tokensea) {
		dispatchBody = tkApplyResponsesDispatchModelMapping(apiKey, body, replace)
	}
	if got := gjson.GetBytes(dispatchBody, "model").String(); got != "claude-haiku-4-5-20251001" {
		t.Fatalf("tokensea forward model = %q, want claude-haiku-4-5-20251001", got)
	}

	oauth := &service.Account{Type: service.AccountTypeOAuth}
	dispatchBody = body
	if tkShouldApplyMessagesDispatchBodyMapping(oauth) {
		dispatchBody = tkApplyResponsesDispatchModelMapping(apiKey, body, replace)
	}
	if got := gjson.GetBytes(dispatchBody, "model").String(); got != openaiDefaults.HaikuMappedModel {
		t.Fatalf("oauth forward model = %q, want %q", got, openaiDefaults.HaikuMappedModel)
	}
}
