//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Claude Code serializes the 1M-context alias suffix verbatim into the API
// `model` field (anthropics/claude-code#60913). TokenKey's strip
// (tkStripContextWindowModelAlias) used to run only on the forward hop, which is
// AFTER account selection — so the per-account model-support predicate judged the
// bracketed id. Kiro accounts (exact catalog lookup) and accounts with an
// explicit model_mapping were excluded from the candidate set and the request
// died before anything could strip the suffix.
//
// These tests drive the real GatewayHandler.Messages path end to end through
// SelectAccountWithLoadAwareness. They use the warmup-intercept account flag so
// the handler returns a mock 200 after selection without an upstream, which makes
// "an account was selected for this model" directly observable.
//
// Both directions are covered: the bracketed model must now select (reverting the
// ingress strip in gateway_handler.go turns the first two subtests red), and bare
// ids plus genuinely unsupported models must behave exactly as before.

func newContextWindowAliasRequest(t *testing.T, group *service.Group, apiKey *service.APIKey, model string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte(`{
		"model": ` + jsonQuote(model) + `,
		"max_tokens": 256,
		"messages": [{"role":"user","content":[{"type":"text","text":"Warmup"}]}]
	}`)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req

	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
	return c, rec
}

func jsonQuote(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(out)
}

// TestGatewayHandlerMessages_ContextWindowAlias_SelectsKiroAccount is the
// regression for the Kiro class: kiroSupportsModel is an exact catalog lookup, so
// "claude-sonnet-5[1m]" was a miss and the only account in the group was filtered
// out of the candidate set.
func TestGatewayHandlerMessages_ContextWindowAlias_SelectsKiroAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(9101)
	accountID := int64(9201)

	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic, // Kiro rides the anthropic group via mixed scheduling
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:       accountID,
		Name:     "kiro-1m",
		Platform: service.PlatformKiro,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":              "test-only",
			"intercept_warmup_requests": true,
		},
		Extra:         map[string]any{"mixed_scheduling": true},
		Concurrency:   1,
		Priority:      1,
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}

	for _, tc := range []struct{ name, model, wantModel string }{
		// The defect: with the alias suffix this returned 400/429 instead of selecting.
		{"1m alias is stripped before selection", "claude-sonnet-5[1m]", "claude-sonnet-5"},
		{"opus 1m alias", "claude-opus-5[1m]", "claude-opus-5"},
		// No-regression control: a bare id behaved correctly before and still does.
		{"bare id unchanged", "claude-sonnet-5", "claude-sonnet-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			defer cleanup()

			apiKey := &service.APIKey{
				ID: 9301, UserID: 9401, GroupID: &groupID, Status: service.StatusActive,
				User:  &service.User{ID: 9401, Concurrency: 10, Balance: 100},
				Group: group,
			}
			c, rec := newContextWindowAliasRequest(t, group, apiKey, tc.model)

			h.Messages(c)

			require.Equal(t, 200, rec.Code, "body=%s", rec.Body.String())

			selected, ok := c.Get(opsAccountIDKey)
			require.True(t, ok, "no account was selected for model %q", tc.model)
			require.Equal(t, accountID, selected)

			// The model carried downstream (ops, mock response, billing key) is bare.
			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, tc.wantModel, resp["model"])
			require.NotContains(t, resp["model"], "[")
		})
	}
}

// TestGatewayHandlerMessages_ContextWindowAlias_SelectsExplicitMappingAccount is
// the regression for the second broken class: an account whose model_mapping
// claims only the bare id. mappingSupportsRequestedModel never matched the
// bracketed key, so the account was excluded.
func TestGatewayHandlerMessages_ContextWindowAlias_SelectsExplicitMappingAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(9102)
	accountID := int64(9202)

	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:       accountID,
		Name:     "anthropic-mapped",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":                   "test-only",
			"base_url":                  "https://example.invalid",
			"intercept_warmup_requests": true,
			"model_mapping":             map[string]any{"claude-sonnet-5": "claude-sonnet-5"},
		},
		Concurrency:   1,
		Priority:      1,
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}

	for _, tc := range []struct{ name, model, wantModel string }{
		{"1m alias is stripped before selection", "claude-sonnet-5[1m]", "claude-sonnet-5"},
		{"bare id unchanged", "claude-sonnet-5", "claude-sonnet-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			defer cleanup()

			apiKey := &service.APIKey{
				ID: 9302, UserID: 9402, GroupID: &groupID, Status: service.StatusActive,
				User:  &service.User{ID: 9402, Concurrency: 10, Balance: 100},
				Group: group,
			}
			c, rec := newContextWindowAliasRequest(t, group, apiKey, tc.model)

			h.Messages(c)

			require.Equal(t, 200, rec.Code, "body=%s", rec.Body.String())
			selected, ok := c.Get(opsAccountIDKey)
			require.True(t, ok, "no account was selected for model %q", tc.model)
			require.Equal(t, accountID, selected)

			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, tc.wantModel, resp["model"])
		})
	}
}

// The count_tokens path has its own throat (gateway_handler.go CountTokens) and
// its own selection call (SelectAccountForModelWithExclusions). A Kiro account
// estimates count_tokens locally, so a successful 200 here proves selection
// admitted the account for the bracketed model.
func TestGatewayHandlerCountTokens_ContextWindowAlias_SelectsKiroAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(9104)
	accountID := int64(9204)

	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:            accountID,
		Name:          "kiro-count-tokens",
		Platform:      service.PlatformKiro,
		Type:          service.AccountTypeOAuth,
		Credentials:   map[string]any{"access_token": "test-only"},
		Extra:         map[string]any{"mixed_scheduling": true},
		Concurrency:   1,
		Priority:      1,
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}

	for _, tc := range []struct{ name, model string }{
		{"1m alias is stripped before selection", "claude-sonnet-5[1m]"},
		{"bare id unchanged", "claude-sonnet-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			defer cleanup()

			apiKey := &service.APIKey{
				ID: 9304, UserID: 9404, GroupID: &groupID, Status: service.StatusActive,
				User:  &service.User{ID: 9404, Concurrency: 10, Balance: 100},
				Group: group,
			}
			c, rec := newContextWindowAliasRequest(t, group, apiKey, tc.model)
			c.Request.URL.Path = "/v1/messages/count_tokens"

			h.CountTokens(c)

			require.Equal(t, 200, rec.Code, "body=%s", rec.Body.String())
			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Greater(t, resp["input_tokens"], float64(0))
		})
	}
}

// No collateral damage: stripping the suffix must not turn a genuinely
// unsupported model into a selectable one. A model outside the account's
// catalog still fails selection with or without the alias — the fix repairs the
// id, it does not widen admission.
func TestGatewayHandlerMessages_ContextWindowAlias_UnsupportedModelStillRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(9103)
	accountID := int64(9203)

	group := &service.Group{
		ID:       groupID,
		Hydrated: true,
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
	}
	account := &service.Account{
		ID:       accountID,
		Name:     "anthropic-narrow",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":                   "test-only",
			"base_url":                  "https://example.invalid",
			"intercept_warmup_requests": true,
			"model_mapping":             map[string]any{"claude-sonnet-5": "claude-sonnet-5"},
		},
		Concurrency:   1,
		Priority:      1,
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}

	for _, tc := range []struct{ name, model string }{
		{"not in mapping, bare", "claude-opus-4-1"},
		{"not in mapping, with 1m alias", "claude-opus-4-1[1m]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			defer cleanup()

			apiKey := &service.APIKey{
				ID: 9303, UserID: 9403, GroupID: &groupID, Status: service.StatusActive,
				User:  &service.User{ID: 9403, Concurrency: 10, Balance: 100},
				Group: group,
			}
			c, rec := newContextWindowAliasRequest(t, group, apiKey, tc.model)

			h.Messages(c)

			require.NotEqual(t, 200, rec.Code,
				"unsupported model must not become selectable: body=%s", rec.Body.String())
			// The client-facing message names the bare model, never the bracketed id.
			require.NotContains(t, rec.Body.String(), "[1m]")
		})
	}
}
