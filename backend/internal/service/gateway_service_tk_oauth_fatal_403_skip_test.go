//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayService_tkIsAccountFatal403(t *testing.T) {
	gw := &GatewayService{}
	cases := []struct {
		name      string
		account   *Account
		body      string
		expect    bool
		rationale string
	}{
		{
			name:      "org_ban_phrase",
			account:   &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			body:      `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`,
			expect:    true,
			rationale: "结构化 org-ban → #810 会永久禁用，原地重试无意义",
		},
		{
			name:      "empty_body",
			account:   &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			body:      "",
			expect:    true,
			rationale: "空 body 403 = 逃过短语匹配的 org-ban 形态 (Gap-A 终局升级)",
		},
		{
			name:      "html_body_unstructured",
			account:   &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			body:      `<html><body>403 Forbidden — cloudflare</body></html>`,
			expect:    true,
			rationale: "非结构化 body 同样按 account-fatal 处理",
		},
		{
			name:      "structured_model_level_denial",
			account:   &Account{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			body:      `{"type":"error","error":{"type":"permission_error","message":"you do not have access to this model"}}`,
			expect:    false,
			rationale: "model-level 拒绝带结构化 body → 不是 account-fatal，保留原地重试",
		},
		{
			name:      "apikey_account_not_fatal",
			account:   &Account{ID: 5, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			body:      "",
			expect:    false,
			rationale: "仅 OAuth 账号在此 scope 内（API key 403 语义不同）",
		},
		{
			name:      "non_anthropic_oauth_not_fatal",
			account:   &Account{ID: 6, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			body:      "",
			expect:    false,
			rationale: "org-ban / 空 body 终局信号是 anthropic 专属，不改其它平台 OAuth 重试语义",
		},
		{
			name:      "nil_account",
			account:   nil,
			body:      "",
			expect:    false,
			rationale: "防御性：nil 账号不致命",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gw.tkIsAccountFatal403(tc.account, []byte(tc.body))
			require.Equal(t, tc.expect, got, tc.rationale)
		})
	}
}
