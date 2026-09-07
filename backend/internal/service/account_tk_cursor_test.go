//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cursorbridge "github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/stretchr/testify/require"
)

type cursorAdminStub struct {
	group    *Group
	existing *Account
	created  *CreateAccountInput
	updated  *UpdateAccountInput
	saveErr  error
}

func (s *cursorAdminStub) GetGroup(context.Context, int64) (*Group, error) { return s.group, nil }
func (s *cursorAdminStub) GetAccount(context.Context, int64) (*Account, error) {
	return s.existing, nil
}
func (s *cursorAdminStub) SaveCursorAccount(_ context.Context, c *CreateAccountInput, u *UpdateAccountInput, id int64) (*Account, error) {
	s.created, s.updated = c, u
	return &Account{ID: id + 1, Name: "Cursor"}, s.saveErr
}

func TestCursorImportClaimsOnlyValidGroupAndSettlesAfterPersistence(t *testing.T) {
	for _, scenario := range []string{"create", "reconnect", "save_failure", "expired", "wrong_group", "wrong_account"} {
		t.Run(scenario, func(t *testing.T) {
			admin := &cursorAdminStub{group: &Group{Name: "Cursor", Platform: PlatformNewAPI}}
			input := CursorAccountInput{SessionID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "Cursor", GroupIDs: []int64{2}}
			claim := cursorbridge.CredentialClaim{APIKey: "private-test-key", Claim: "claim-test", Authorization: cursorbridge.Authorization{
				KeyExpiresAt: time.Now().Add(time.Hour), Models: []cursorbridge.Model{{ID: "auto"}, {ID: "composer-2.5", Variants: []cursorbridge.Variant{{IsDefault: true, Params: []cursorbridge.Parameter{{ID: "fast", Value: "true"}}}, {Params: []cursorbridge.Parameter{{ID: "fast", Value: "false"}}}}}},
			}}
			if scenario == "reconnect" || scenario == "wrong_account" {
				input.AccountID = 42
				admin.existing = cursorTestAccount()
				admin.existing.Credentials["custom_setting"] = "preserved"
				if scenario == "wrong_account" {
					admin.existing.Extra = nil
				}
			}
			if scenario == "save_failure" {
				admin.saveErr = errors.New("database unavailable")
			}
			if scenario == "expired" {
				claim.KeyExpiresAt = time.Now().Add(-time.Minute)
			}
			if scenario == "wrong_group" {
				admin.group.Platform = PlatformOpenAI
			}
			claims, settlements := 0, 0
			settledSuccess := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "admin:7", r.Header.Get(cursorbridge.TenantHeader))
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims++
					_ = json.NewEncoder(w).Encode(claim)
				} else {
					settlements++
					var body struct {
						Success bool   `json:"success"`
						Claim   string `json:"claim"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, claim.Claim, body.Claim)
					settledSuccess = body.Success
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			client, err := cursorbridge.NewClient(server.URL, strings.Repeat("s", 32))
			require.NoError(t, err)
			_, err = ImportCursorAccount(context.Background(), admin, client, "admin:7", input)
			success := scenario == "create" || scenario == "reconnect"
			if success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if scenario == "wrong_group" || scenario == "wrong_account" {
				require.Zero(t, claims)
				require.Zero(t, settlements)
				return
			}
			require.Equal(t, 1, claims)
			require.Equal(t, 1, settlements)
			require.Equal(t, success, settledSuccess)
			if scenario == "create" {
				require.Equal(t, PlatformNewAPI, admin.created.Platform)
				require.Equal(t, AccountTypeAPIKey, admin.created.Type)
				require.Equal(t, 14, admin.created.ChannelType)
				require.NotContains(t, admin.created.Credentials["model_mapping"], "auto")
				require.True(t, *admin.created.AutoPauseOnExpired)
			}
			if scenario == "reconnect" {
				require.Equal(t, "preserved", admin.updated.Credentials["custom_setting"])
				require.Equal(t, "private-test-key", admin.updated.Credentials["api_key"])
				require.Nil(t, admin.created)
			}
		})
	}
}
