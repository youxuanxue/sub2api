package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUS066_RetiredSupplierDoesNotBlockProtocolReadiness(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{{
		ID: 1, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{"upstream_provider": "cursor"},
	}}}
	report, err := MigrateProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter())
	require.NoError(t, err)
	require.True(t, report.CutoverReady)
	require.Zero(t, report.ProbeAttempts)
	require.Zero(t, repo.ensureCalls)
}

func TestUS066_RetiredSupplierCannotScheduleOrRestore(t *testing.T) {
	for name, account := range map[string]*Account{
		"marker on edited platform":      {Platform: PlatformOpenAI, Extra: map[string]any{"upstream_provider": "cursor"}},
		"native endpoint without marker": {Credentials: map[string]any{"base_url": "https://agentn.global.api5.cursor.sh"}},
		"per protocol endpoint":          {Credentials: map[string]any{"api_base_urls": map[string]any{"anthropic": "https://api2.cursor.sh/v1"}}},
		"old relay credentials":          {Credentials: map[string]any{"cursor_model_parameters": map[string]any{}}},
	} {
		t.Run(name, func(t *testing.T) {
			account.Status, account.Schedulable = StatusActive, true
			require.False(t, account.IsSchedulable())
			require.False(t, account.IsCredentialUsableForShadow())
			_, governed, err := BuildProtocolEndpointIdentity(account)
			require.True(t, governed)
			require.ErrorIs(t, err, ErrSupplierRetired)
			_, err = buildAccountForCreate(&CreateAccountInput{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Credentials: account.Credentials}, account.Extra)
			require.ErrorIs(t, err, ErrSupplierRetired)
			account.Status = StatusDisabled
			link, err := BuildProtocolEndpointCapabilityLinkInput(account)
			require.NoError(t, err, "retirement must not prevent disabling an old row")
			require.False(t, link.Governed, "disabled retired rows must lose their capability link")
		})
	}
}

func TestUS066_RetiredSupplierPreservesOrdinaryAPIKeys(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"base_url": "https://cursor.sh.example.test", "api_key": "synthetic"}}
	// A hostname label must not accidentally retire an unrelated supplier.
	if account.IsRetiredSupplier() {
		t.Fatal("unrelated API key was retired")
	}
	require.True(t, account.IsSchedulable())
	created, err := buildAccountForCreate(&CreateAccountInput{Platform: account.Platform, Type: account.Type, Credentials: account.Credentials}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, account.Credentials, created.Credentials)
}
