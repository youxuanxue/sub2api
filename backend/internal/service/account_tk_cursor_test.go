//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
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
	for _, scenario := range []string{"create", "reconnect", "save_failure", "expired", "wrong_group", "multiple_groups", "wrong_account"} {
		t.Run(scenario, func(t *testing.T) {
			admin := &cursorAdminStub{group: &Group{Name: "Cursor", Platform: PlatformNewAPI}}
			input := CursorAccountInput{SessionID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "Cursor", GroupIDs: []int64{2}}
			claim := cursor.CredentialClaim{APIKey: "private-test-key", Claim: "claim-test", Authorization: cursor.Authorization{
				KeyExpiresAt: time.Now().Add(time.Hour), Models: []cursor.Model{{ID: "auto"}, {ID: "composer-2.5", Variants: []cursor.Variant{{IsDefault: true, Params: []cursor.Parameter{{ID: "fast", Value: "true"}}}, {LegacySlug: "composer-2.5", Params: []cursor.Parameter{{ID: "fast", Value: "false"}}}}}},
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
			if scenario == "multiple_groups" {
				input.GroupIDs = []int64{2, 3}
			}
			client := &cursorClaimStub{claim: claim}
			_, err := ImportCursorAccount(context.Background(), admin, client, "admin:7", input)
			success := scenario == "create" || scenario == "reconnect"
			if success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if scenario == "wrong_group" || scenario == "wrong_account" || scenario == "multiple_groups" {
				require.Zero(t, client.claims)
				require.Zero(t, client.settlements)
				return
			}
			require.Equal(t, 1, client.claims)
			require.Equal(t, 1, client.settlements)
			require.Equal(t, success, client.success)
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

type cursorClaimStub struct {
	claim               cursor.CredentialClaim
	claims, settlements int
	success             bool
}

func (s *cursorClaimStub) Claim(context.Context, string, string) (cursor.CredentialClaim, error) {
	s.claims++
	return s.claim, nil
}
func (s *cursorClaimStub) Settle(_ context.Context, _, _, claim string, success bool) error {
	if claim != s.claim.Claim {
		return errors.New("wrong claim")
	}
	s.settlements++
	s.success = success
	return nil
}
