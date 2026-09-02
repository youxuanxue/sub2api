package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestUS048_FMGoSeedanceSyncProjectsOfficialClientsOnly(t *testing.T) {
	ratio := 0.5
	notes := "inventory: feimiao-v2.5-720p-15s feimiao-v2-431-720p-15s feimiao-v2-mini-720p-10s"
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 10, SupplierName: "feimiao", ChannelName: "feimiao-svip",
		ChannelType:         newapiconstant.ChannelTypeDoubaoVideo,
		Endpoint:            "https://www.fmgo.top",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Notes: notes,
		Models: []SupplierSourceModel{
			{ClientModelID: "doubao-seedance-2-0-260128", UpstreamModelID: "feimiao-v2-431-720p-15s", PurchaseRatio: &ratio},
			{ClientModelID: "doubao-seedance-2-0-fast-260128", UpstreamModelID: "feimiao-v2-431-fast-720p-15s", PurchaseRatio: &ratio},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{}
	probe := &supplierSyncProbeFake{}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 10)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, accounts.updated)
	require.Equal(t, map[string]string{
		"doubao-seedance-2-0-260128":      "feimiao-v2-431-720p-15s",
		"doubao-seedance-2-0-fast-260128": "feimiao-v2-431-fast-720p-15s",
	}, accounts.updated[0].ModelMapping)
	for clientID := range accounts.updated[0].ModelMapping {
		require.False(t, strings.HasPrefix(clientID, "feimiao-v2-"), "SKU %q leaked as client", clientID)
	}
	require.Len(t, accounts.updated[0].ModelMapping, 2)
}

func TestUS048_SupplierSyncGroupsSameBandModelsIntoOneAccount(t *testing.T) {
	ratio05, ratio055 := 0.5, 0.55
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio05},
			{ClientModelID: "qwen-3.7-max", UpstreamModelID: "qwen-3.7-max", PurchaseRatio: &ratio055},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{}
	probe := &supplierSyncProbeFake{}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	_, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Equal(t, 1, accounts.createCalls)
	require.Equal(t, 3, accounts.created[0].DiscountBand)
	require.Equal(t, 130, accounts.created[0].Priority)
	require.NotEmpty(t, accounts.updated)
	require.Equal(t, map[string]string{
		"deepseek-v4-pro": "deepseek-v4-pro",
		"qwen-3.7-max":    "qwen-3.7-max",
	}, accounts.updated[0].ModelMapping)
	require.True(t, accounts.updated[0].Schedulable)
	require.NotZero(t, accounts.getCalls, "addition must be read back before any removal can start")
}

func TestUS048_SupplierProbeAccountCarriesManagedIdentity(t *testing.T) {
	source := &SupplierSource{ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1"}
	account := supplierProbeAccount(nil, source, "secret", supplierTargetBand{
		Band: 3, Priority: 130, Mapping: map[string]string{"client-model": "vendor-model"},
	})

	require.True(t, IsSupplierManagedAccount(account))
	require.Equal(t, int64(7), account.Extra[SupplierSourceIDExtraKey])
	require.Equal(t, 3, account.Extra[SupplierDiscountBandExtraKey])
}

func TestUS048_ValidateFailureReturnsEveryResultAndWritesNothing(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "FMGo", ChannelName: "seedance", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "model-ok", UpstreamModelID: "model-ok", PurchaseRatio: &ratio},
			{ClientModelID: "doubao-seedance-2-0-260128", UpstreamModelID: "feimiao-seedance-2-0-260128", PurchaseRatio: &ratio},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{}
	probe := &supplierSyncProbeFake{results: map[string]SupplierProbeResult{
		"feimiao-seedance-2-0-260128": {Status: SupplierProbeStatusProtocolUnsupported, Detail: "supplier protocol unsupported"},
	}}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Validate(context.Background(), 7)

	require.ErrorIs(t, err, ErrSupplierSourceProbeFailed)
	require.Len(t, result.ProbeResults, 2)
	require.Equal(t, SupplierProbeStatusPassed, result.ProbeResults[0].Status)
	require.Equal(t, SupplierProbeStatusProtocolUnsupported, result.ProbeResults[1].Status)
	require.Zero(t, accounts.createCalls)
	require.Empty(t, accounts.updated)
}

func TestUS048_SupplierSyncReprobesImmediatelyBeforeProjection(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Len(t, result.ProbeResults, 1)
	require.Equal(t, SupplierProbeStatusPassed, result.ProbeResults[0].Status)
	require.Equal(t, 1, accounts.createCalls)
	require.NotEmpty(t, accounts.updated)
}

func TestUS048_SupplierSyncMetadataOnlySkipsProbe(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{{
		ID: 41, Name: "existing", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"model": "model"}, 1),
		Extra:    map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
		Priority: 102, Status: StatusActive, Schedulable: true, Concurrency: SupplierSourceDefaultAccountConcurrency,
	}}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Empty(t, result.ProbeResults)
	require.Len(t, accounts.updated, 1)
	require.True(t, accounts.updated[0].MetadataOnly)
	require.Equal(t, "佳杰/stbl-5 · 档位 3", accounts.updated[0].Name)
	require.Equal(t, 130, accounts.updated[0].Priority)
}

func TestUS048_SupplierSyncNameChangeUpdatesAccountWithoutProbe(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰科技", ChannelName: "stbl-6", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{{
		ID: 41, Name: "佳杰/stbl-5 · 档位 3", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"model": "model"}, 1),
		Extra:    map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
		Priority: 130, Status: StatusActive, Schedulable: true, Concurrency: SupplierSourceDefaultAccountConcurrency,
	}}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Empty(t, result.ProbeResults)
	require.Len(t, accounts.updated, 1)
	require.Equal(t, "佳杰科技/stbl-6 · 档位 3", accounts.updated[0].Name)
}

func TestUS048_SupplierSyncMovesModelByAddingBeforeRemoving(t *testing.T) {
	ratio := 0.7
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{{
		ID: 41, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"model": "model"}, 1),
		Extra:    map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
		Priority: 130, Status: StatusActive, Schedulable: true, Concurrency: SupplierSourceDefaultAccountConcurrency,
	}}}
	probe := &supplierSyncProbeFake{}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	_, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	addIndex, removeIndex := -1, -1
	for index, operation := range accounts.operations {
		if operation == "update:band=4:models=1" {
			addIndex = index
		}
		if operation == "update:band=3:models=0" {
			removeIndex = index
		}
	}
	require.GreaterOrEqual(t, addIndex, 0)
	require.Greater(t, removeIndex, addIndex)
}

func TestUS048_SupplierSyncStopsBeforeRemovalWhenVerifiedProjectionWriteFails(t *testing.T) {
	ratio := 0.7
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	accounts := &supplierSyncAccountStoreFake{
		managed: []*Account{supplierSyncManagedAccount(
			41, 7, 3, 130, map[string]string{"model": "model"}, true,
		)},
		updateErrAt: 1,
		updateErr:   ErrSupplierProjectionProtocolNotReady,
	}
	probe := &supplierSyncProbeFake{}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.ErrorIs(t, err, ErrSupplierProjectionProtocolNotReady)
	require.Equal(t, "add_band_4", result.FailedStep)
	require.Contains(t, accounts.operations, "update:band=4:models=1")
	require.NotContains(t, accounts.operations, "update:band=3:models=0")
	require.Len(t, result.Changes, 1, "the safely created empty account must remain visible when the atomic projection fails")
	require.Equal(t, "created", result.Changes[0].Action)
	require.Empty(t, result.Changes[0].AddedModels)
	require.False(t, result.Changes[0].SchedulableAfter)
}

func TestUS048_SupplierSyncClearsEmptyBandWithoutDeletingAccount(t *testing.T) {
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{},
	}}
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{supplierSyncManagedAccount(
		41, 7, 3, 130, map[string]string{"model": "model"}, true,
	)}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Empty(t, result.ProbeResults)
	require.Len(t, accounts.managed, 1)
	require.Equal(t, int64(41), accounts.managed[0].ID)
	require.Empty(t, supplierModelMapping(accounts.managed[0].Credentials))
	require.False(t, accounts.managed[0].Schedulable)
	require.Contains(t, accounts.operations, "update:band=3:models=0")
}

func TestUS048_SupplierSyncAdditionFailureStopsBeforeRemovalAndRetryConverges(t *testing.T) {
	ratio05, ratio07 := 0.5, 0.7
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "move-model", UpstreamModelID: "move-model", PurchaseRatio: &ratio05},
			{ClientModelID: "new-model", UpstreamModelID: "new-model", PurchaseRatio: &ratio07},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{
		managed: []*Account{supplierSyncManagedAccount(
			41, 7, 2, 102, map[string]string{"move-model": "move-model"}, true,
		)},
		updateErrAt: 2,
	}
	probe := &supplierSyncProbeFake{}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	firstResult, firstErr := svc.Sync(context.Background(), 7)

	require.Error(t, firstErr)
	require.Equal(t, "add_band_4", firstResult.FailedStep)
	require.Contains(t, accounts.operations, "update:band=3:models=1")
	require.NotContains(t, accounts.operations, "update:band=2:models=0")
	require.Equal(t, map[string]string{"move-model": "move-model"}, supplierModelMapping(accounts.managed[0].Credentials))
	require.Len(t, firstResult.Changes, 2, "the response must report the account created before the later add failed")
	require.Equal(t, "created", firstResult.Changes[1].Action)
	require.Empty(t, firstResult.Changes[1].AddedModels)
	require.False(t, firstResult.Changes[1].SchedulableAfter)

	accounts.updateErrAt = 0
	accounts.operations = nil
	secondResult, secondErr := svc.Sync(context.Background(), 7)

	require.NoError(t, secondErr)
	require.Empty(t, secondResult.FailedStep)
	require.Contains(t, accounts.operations, "update:band=2:models=0")
	managedByBand, err := supplierAccountsByBand(accounts.managed)
	require.NoError(t, err)
	require.Empty(t, supplierModelMapping(managedByBand[2].Credentials))
	require.False(t, managedByBand[2].Schedulable)
	require.Equal(t, map[string]string{"move-model": "move-model"}, supplierModelMapping(managedByBand[3].Credentials))
	require.Equal(t, map[string]string{"new-model": "new-model"}, supplierModelMapping(managedByBand[4].Credentials))
}

func TestUS048_SupplierSyncReadbackFailureReportsTheCompletedAddition(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	accounts := &supplierSyncAccountStoreFake{getErrAt: 1}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	result, err := svc.Sync(context.Background(), 7)

	require.Error(t, err)
	require.Equal(t, "verify_band_3", result.FailedStep)
	require.Len(t, result.Changes, 1)
	require.Equal(t, "created", result.Changes[0].Action)
	require.Equal(t, []string{"model"}, result.Changes[0].AddedModels)
	require.True(t, result.Changes[0].SchedulableAfter)
}

func TestUS048_SupplierSyncCreateUpdateFailureRetryConverges(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	accounts := &supplierSyncAccountStoreFake{updateErrAt: 1}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	firstResult, firstErr := svc.Sync(context.Background(), 7)

	require.Error(t, firstErr)
	require.Equal(t, "add_band_3", firstResult.FailedStep)
	require.Len(t, accounts.managed, 1)
	require.Empty(t, supplierModelMapping(accounts.managed[0].Credentials))
	require.False(t, accounts.managed[0].Schedulable)
	require.Len(t, firstResult.Changes, 1)
	require.Equal(t, "created", firstResult.Changes[0].Action)
	require.Empty(t, firstResult.Changes[0].AddedModels)

	accounts.updateErrAt = 0
	accounts.operations = nil
	secondResult, secondErr := svc.Sync(context.Background(), 7)

	require.NoError(t, secondErr)
	require.Empty(t, secondResult.FailedStep)
	require.Equal(t, map[string]string{"model": "model"}, supplierModelMapping(accounts.managed[0].Credentials))
	require.True(t, accounts.managed[0].Schedulable)
}

func TestUS048_ExistingSupplierAccountIsAdoptedOnlyOnUniqueNarrowMatch(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "百度", ChannelName: "千帆", ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
			{ClientModelID: "qwen-3.7-max", UpstreamModelID: "qwen-3.7-max", PurchaseRatio: &ratio},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{matches: []*Account{{
		ID: 90, Name: "百度千帆", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: supplierManagedCredentials(
			"https://qianfan.baidubce.com", "secret",
			map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"}, newapiconstant.ChannelTypeBaiduV2),
		GroupIDs: []int64{4, 9}, Status: StatusActive, Schedulable: true,
	}}}
	svc := NewSupplierSourceService(repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Zero(t, accounts.createCalls)
	require.NotEmpty(t, accounts.updated)
	require.True(t, accounts.updated[0].Adopt)
	require.Equal(t, int64(90), accounts.updated[0].AccountID)
	require.Equal(t, "adopted", result.Changes[0].Action)
}

func TestUS048_NonActiveExactAccountMatchBlocksDuplicateSupplierAccountCreation(t *testing.T) {
	ratio := 0.5
	for _, status := range []string{StatusDisabled, StatusError} {
		t.Run(status, func(t *testing.T) {
			repo := &supplierSourceRepoFake{stored: &SupplierSource{
				ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
				EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
				Models: []SupplierSourceModel{{
					ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio,
				}},
			}}
			accounts := &supplierSyncAccountStoreFake{matches: []*Account{{
				ID: 90, Name: "existing exact account", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
				ChannelType: newapiconstant.ChannelTypeOpenAI,
				Credentials: supplierManagedCredentials(
					"https://supplier.example/v1", "secret", map[string]string{"model": "model"}, 1),
				Status: status, Schedulable: false,
			}}}
			probe := &supplierSyncProbeFake{failIfCalled: true}
			svc := NewSupplierSourceService(
				repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
			)

			result, err := svc.Sync(context.Background(), 7)

			require.ErrorIs(t, err, ErrSupplierSourceIdentityConflict)
			require.Equal(t, "match_existing_account", result.FailedStep)
			require.Zero(t, accounts.createCalls)
			require.Empty(t, accounts.updated)
		})
	}
}

func TestUS048_IncompatibleTransportExactMatchBlocksAdoptionWithoutRewritingAccount(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "百度", ChannelName: "qianfan", ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{
			ClientModelID: "deepseek-v4-flash-0731", UpstreamModelID: "deepseek-v4-flash-0731", PurchaseRatio: &ratio,
		}},
	}}
	existing := &Account{
		ID: 90, Name: "百度千帆误配 OpenAI", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: supplierManagedCredentials(
			"https://qianfan.baidubce.com", "secret", map[string]string{"deepseek-v4-flash-0731": "deepseek-v4-flash-0731"}, newapiconstant.ChannelTypeOpenAI),
		Status: StatusActive, Schedulable: false,
	}
	accounts := &supplierSyncAccountStoreFake{matches: []*Account{existing}}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{failIfCalled: true}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	result, err := svc.Sync(context.Background(), 7)

	require.ErrorIs(t, err, ErrSupplierSourceIdentityConflict)
	require.Equal(t, "match_existing_account", result.FailedStep)
	require.Zero(t, accounts.createCalls)
	require.Empty(t, accounts.updated)
	require.Equal(t, newapiconstant.ChannelTypeOpenAI, existing.ChannelType, "wrong OpenAI transport on Qianfan must not be rewritten during blocked adoption")
}

func TestUS048_MultiBandExactAccountMatchBlocksDuplicateSupplierAccountCreation(t *testing.T) {
	ratio03, ratio05 := 0.3, 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "model-band-2", UpstreamModelID: "model-band-2", PurchaseRatio: &ratio03},
			{ClientModelID: "model-band-3", UpstreamModelID: "model-band-3", PurchaseRatio: &ratio05},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{matches: []*Account{{
		ID: 90, Name: "existing exact account", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"model-band-2": "model-band-2"}, 1),
		Status: StatusActive, Schedulable: true,
	}}}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	result, err := svc.Sync(context.Background(), 7)

	require.ErrorIs(t, err, ErrSupplierSourceIdentityConflict)
	require.Equal(t, "match_existing_account", result.FailedStep)
	require.Zero(t, accounts.createCalls)
	require.Empty(t, accounts.updated)
}

func TestUS048_SupplierSyncReprobesBeforeRepairingNonEmptySchedulingProjection(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	account := supplierSyncManagedAccount(41, 7, 3, 130, map[string]string{"model": "model"}, false)
	account.Name = supplierManagedAccountName(repo.stored, 3)
	account.Status = StatusError
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{account}}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	_, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.NotEmpty(t, accounts.updated)
	require.False(t, accounts.updated[0].MetadataOnly)
	require.Equal(t, StatusActive, accounts.managed[0].Status)
	require.True(t, accounts.managed[0].Schedulable)
}

func TestUS048_SupplierSyncRepairsEmptySchedulingProjectionWithoutProbe(t *testing.T) {
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{},
	}}
	account := supplierSyncManagedAccount(41, 7, 3, 130, map[string]string{}, true)
	account.Name = supplierManagedAccountName(repo.stored, 3)
	account.Status = StatusDisabled
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{account}}
	svc := NewSupplierSourceService(
		repo, accounts, &supplierSyncProbeFake{failIfCalled: true}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Empty(t, result.ProbeResults)
	require.NotEmpty(t, accounts.updated)
	require.False(t, accounts.updated[0].MetadataOnly)
	require.Equal(t, StatusActive, accounts.managed[0].Status)
	require.False(t, accounts.managed[0].Schedulable)
}

func TestUS048_QianfanBaiduV2ExactMatchIsAdopted(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "百度", ChannelName: "千帆", ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com/v2",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{
			ClientModelID: "glm-5.1", UpstreamModelID: "glm-5.1", PurchaseRatio: &ratio,
		}},
	}}
	accounts := &supplierSyncAccountStoreFake{matches: []*Account{{
		ID: 90, Name: "百度千帆", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: supplierManagedCredentials(
			"https://qianfan.baidubce.com", "secret",
			map[string]string{"glm-5.1": "glm-5.1"}, newapiconstant.ChannelTypeBaiduV2),
		GroupIDs: []int64{4, 9}, Status: StatusActive, Schedulable: true,
	}}}
	svc := NewSupplierSourceService(repo, accounts, &supplierSyncProbeFake{}, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Zero(t, accounts.createCalls)
	require.True(t, accounts.updated[0].Adopt)
	require.Equal(t, int64(90), accounts.updated[0].AccountID)
	require.Equal(t, "adopted", result.Changes[0].Action)
}

func TestUS048_BaiduV2TransportAgainstOpenAISupplierIsRejected(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://token.vstecscloud.com/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{
			ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio,
		}},
	}}
	accounts := &supplierSyncAccountStoreFake{matches: []*Account{{
		ID: 90, Name: "wrong transport", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: supplierManagedCredentials(
			"https://token.vstecscloud.com/v1", "secret",
			map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"}, 1),
		GroupIDs: []int64{4, 9}, Status: StatusActive, Schedulable: true,
	}}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.ErrorIs(t, err, ErrSupplierSourceIdentityConflict)
	require.Empty(t, result.ProbeResults)
	require.Zero(t, accounts.createCalls)
	require.Empty(t, accounts.updated)
}

func TestUS048_ExistingSupplierAccountMultipleMatchesStopBeforeProbeOrWrite(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "百度", ChannelName: "千帆", ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	match := &Account{
		ID: 90, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: supplierManagedCredentials("https://qianfan.baidubce.com", "secret", map[string]string{"model": "model"}, 46),
	}
	second := cloneSupplierProjectionAccount(match)
	second.ID = 91
	accounts := &supplierSyncAccountStoreFake{matches: []*Account{match, second}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.ErrorIs(t, err, ErrSupplierSourceMultipleMatches)
	require.Empty(t, result.ProbeResults)
	require.Zero(t, accounts.createCalls)
	require.Empty(t, accounts.updated)
}

func TestUS048_SupplierSyncSameBandRatioChangeDoesNotTouchAccounts(t *testing.T) {
	ratio := 0.59
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰新名称", ChannelName: "stbl-5-new", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}}
	account := supplierSyncManagedAccount(41, 7, 3, 130, map[string]string{"model": "model"}, true)
	account.Name = supplierManagedAccountName(repo.stored, 3)
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{account}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Empty(t, result.ProbeResults)
	require.Empty(t, result.Changes)
	require.Empty(t, accounts.updated)
}

func TestUS048_SupplierSyncEarlyErrorsAlwaysReportFailedStep(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", ChannelType: 1, Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		Models: []SupplierSourceModel{{ClientModelID: "model", UpstreamModelID: "model", PurchaseRatio: &ratio}},
	}

	tests := []struct {
		name      string
		repo      *supplierSourceRepoFake
		accounts  *supplierSyncAccountStoreFake
		encryptor SecretEncryptor
		wantStep  string
	}{
		{
			name: "load source", repo: &supplierSourceRepoFake{}, accounts: &supplierSyncAccountStoreFake{},
			encryptor: supplierSyncEncryptor{}, wantStep: "load_source",
		},
		{
			name: "decrypt credential", repo: &supplierSourceRepoFake{stored: source}, accounts: &supplierSyncAccountStoreFake{},
			encryptor: supplierSyncFailingEncryptor{}, wantStep: "decrypt_credential",
		},
		{
			name: "load managed accounts", repo: &supplierSourceRepoFake{stored: source},
			accounts:  &supplierSyncAccountStoreFake{listErr: errors.New("injected list failure")},
			encryptor: supplierSyncEncryptor{}, wantStep: "load_managed_accounts",
		},
		{
			name: "match existing account", repo: &supplierSourceRepoFake{stored: source},
			accounts:  &supplierSyncAccountStoreFake{matchErr: errors.New("injected match failure")},
			encryptor: supplierSyncEncryptor{}, wantStep: "match_existing_account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewSupplierSourceService(
				tt.repo, tt.accounts, &supplierSyncProbeFake{}, tt.encryptor, supplierSourceTestFingerprinter{},
			)

			result, err := svc.Sync(context.Background(), 7)

			require.Error(t, err)
			require.Equal(t, tt.wantStep, result.FailedStep)
			require.NotNil(t, result.Changes)
		})
	}
}

func supplierSyncManagedAccount(id, sourceID int64, band, priority int, mapping map[string]string, schedulable bool) *Account {
	return &Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: supplierManagedCredentials("https://supplier.example/v1", "secret", mapping, 1),
		Extra: map[string]any{
			SupplierSourceIDExtraKey: sourceID, SupplierDiscountBandExtraKey: band,
		},
		Priority: priority, Status: StatusActive, Schedulable: schedulable,
		Concurrency: SupplierSourceDefaultAccountConcurrency,
	}
}

type supplierSyncEncryptor struct{}

func (supplierSyncEncryptor) Encrypt(value string) (string, error) { return "enc:" + value, nil }
func (supplierSyncEncryptor) Decrypt(value string) (string, error) {
	return strings.TrimPrefix(value, "enc:"), nil
}

type supplierSyncFailingEncryptor struct{}

func (supplierSyncFailingEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (supplierSyncFailingEncryptor) Decrypt(string) (string, error) {
	return "", errors.New("injected decrypt failure")
}

type supplierSyncProbeFake struct {
	results      map[string]SupplierProbeResult
	failIfCalled bool
}

func (p *supplierSyncProbeFake) ProbeSupplierModel(_ context.Context, input SupplierProbeInput) SupplierProbeResult {
	if p.failIfCalled {
		panic("priority-only sync must not probe")
	}
	if result, ok := p.results[input.UpstreamModelID]; ok {
		return result
	}
	return SupplierProbeResult{Status: SupplierProbeStatusPassed, Protocol: "openai_chat_completions"}
}

type supplierSyncAccountStoreFake struct {
	managed            []*Account
	matches            []*Account
	created            []SupplierManagedAccountCreateInput
	updated            []SupplierManagedAccountUpdateInput
	concurrencyUpdates []int
	operations         []string
	createCalls        int
	getCalls           int
	nextID             int64
	updateErrAt        int
	updateErr          error
	getErrAt           int
	listErr            error
	matchErr           error
}

func (f *supplierSyncAccountStoreFake) UpdateManagedAccountConcurrency(
	_ context.Context,
	accountID, sourceID int64,
	discountBand, concurrency int,
) (*Account, error) {
	f.concurrencyUpdates = append(f.concurrencyUpdates, concurrency)
	for index, account := range f.managed {
		if account.ID != accountID {
			continue
		}
		managedSourceID, sourceOK := supplierSourceIDFromAccount(account)
		managedBand, bandOK := supplierDiscountBandFromAccount(account)
		if !sourceOK || !bandOK || managedSourceID != sourceID || managedBand != discountBand {
			return nil, ErrSupplierSourceIdentityConflict
		}
		account.Concurrency = concurrency
		f.managed[index] = account
		return cloneSupplierProjectionAccount(account), nil
	}
	return nil, ErrAccountNotFound
}

func (f *supplierSyncAccountStoreFake) ListManagedAccounts(context.Context, int64) ([]*Account, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]*Account, 0, len(f.managed))
	for _, account := range f.managed {
		result = append(result, cloneSupplierProjectionAccount(account))
	}
	return result, nil
}

func (f *supplierSyncAccountStoreFake) FindCredentialEndpointMatches(context.Context, SupplierAccountMatch) ([]*Account, error) {
	if f.matchErr != nil {
		return nil, f.matchErr
	}
	result := make([]*Account, 0, len(f.matches))
	for _, account := range f.matches {
		result = append(result, cloneSupplierProjectionAccount(account))
	}
	return result, nil
}

func (f *supplierSyncAccountStoreFake) CreateManagedAccount(_ context.Context, input SupplierManagedAccountCreateInput) (*Account, error) {
	f.createCalls++
	f.created = append(f.created, input)
	f.operations = append(f.operations, "create:band="+string(rune('0'+input.DiscountBand)))
	if f.nextID == 0 {
		f.nextID = 100
	}
	f.nextID++
	transport, _ := resolveSupplierManagedTransport(input.Endpoint, input.ChannelType)
	account := &Account{
		ID: f.nextID, Name: input.Name, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: transport.ChannelType,
		Credentials: supplierManagedCredentials(input.Endpoint, input.Credential, map[string]string{}, input.ChannelType),
		Extra:       map[string]any{SupplierSourceIDExtraKey: input.SourceID, SupplierDiscountBandExtraKey: input.DiscountBand},
		Priority:    input.Priority, Status: StatusActive, Schedulable: false,
	}
	f.managed = append(f.managed, account)
	return cloneSupplierProjectionAccount(account), nil
}

func (f *supplierSyncAccountStoreFake) UpdateManagedAccount(_ context.Context, input SupplierManagedAccountUpdateInput) (*Account, error) {
	f.updated = append(f.updated, input)
	f.operations = append(f.operations, syncOperationLabel(input))
	if len(input.ModelMapping) > 0 && !input.ChatProbePassed {
		return nil, ErrSupplierProjectionProtocolNotReady
	}
	if f.updateErrAt > 0 && len(f.updated) == f.updateErrAt {
		if f.updateErr != nil {
			return nil, f.updateErr
		}
		return nil, errors.New("injected update failure")
	}
	for index, account := range f.managed {
		if account.ID != input.AccountID {
			continue
		}
		if input.MetadataOnly {
			account.Name = input.Name
			account.Priority = input.Priority
		} else {
			transport, _ := resolveSupplierManagedTransport(input.Endpoint, input.ChannelType)
			account.ChannelType = transport.ChannelType
			account.Credentials = supplierManagedCredentials(input.Endpoint, input.Credential, input.ModelMapping, input.ChannelType)
			account.Priority = input.Priority
			account.Concurrency = ResolveSupplierSourceAccountConcurrency(input.Concurrency)
			account.Status = input.Status
			account.Schedulable = input.Schedulable && len(input.ModelMapping) > 0
		}
		f.managed[index] = account
		return cloneSupplierProjectionAccount(account), nil
	}
	if input.Adopt {
		transport, _ := resolveSupplierManagedTransport(input.Endpoint, input.ChannelType)
		account := &Account{
			ID: input.AccountID, Name: input.Name, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: transport.ChannelType,
			Credentials: supplierManagedCredentials(input.Endpoint, input.Credential, input.ModelMapping, input.ChannelType),
			Extra:       map[string]any{SupplierSourceIDExtraKey: input.SourceID, SupplierDiscountBandExtraKey: input.DiscountBand},
			Priority:    input.Priority, Status: input.Status, Schedulable: input.Schedulable,
		}
		f.managed = append(f.managed, account)
		return cloneSupplierProjectionAccount(account), nil
	}
	return nil, ErrAccountNotFound
}

func (f *supplierSyncAccountStoreFake) GetAccount(_ context.Context, accountID int64) (*Account, error) {
	f.getCalls++
	if f.getErrAt > 0 && f.getCalls == f.getErrAt {
		return nil, errors.New("injected readback failure")
	}
	for _, account := range f.managed {
		if account.ID == accountID {
			return cloneSupplierProjectionAccount(account), nil
		}
	}
	return nil, ErrAccountNotFound
}

func syncOperationLabel(input SupplierManagedAccountUpdateInput) string {
	return "update:band=" + string(rune('0'+input.DiscountBand)) + ":models=" + string(rune('0'+len(input.ModelMapping)))
}

func TestUS048_SupplierSyncAppliesSourceAccountConcurrency(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		AccountConcurrency: 1000,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
		},
	}}
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{{
		ID: 41, Name: "supplier/佳杰 · 档位 3", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret",
			map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"}, 1),
		Extra:    map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
		Priority: 130, Status: StatusActive, Schedulable: true, Concurrency: 1,
	}}}
	probe := &supplierSyncProbeFake{failIfCalled: true}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	_, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.Equal(t, []int{1000}, accounts.concurrencyUpdates)
	for _, update := range accounts.updated {
		require.True(t, update.MetadataOnly, "concurrency-only sync must not use the full projection write")
	}
	require.Equal(t, 1000, accounts.managed[0].Concurrency)
}

func TestUS048_SupplierSyncProtocolIdentityDriftReprobesBeforeRepair(t *testing.T) {
	ratio := 0.5
	capabilityID := int64(797)
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://supplier.example/v1",
		EncryptedCredential: "enc:secret", CredentialFingerprint: "fp:secret", BasePriority: 100,
		AccountConcurrency: 1000,
		Models: []SupplierSourceModel{{
			ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio,
		}},
	}}
	accounts := &supplierSyncAccountStoreFake{managed: []*Account{{
		ID: 41, Name: "supplier/佳杰 · 档位 3", Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: map[string]any{
			"base_url": "https://supplier.example/v1", "api_key": "secret",
			"model_mapping": map[string]string{"deepseek-v4-pro": "deepseek-v4-pro"},
		},
		Extra:    map[string]any{SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3},
		Priority: 130, Status: StatusActive, Schedulable: true, Concurrency: 1000,
		ProtocolEndpointCapabilityID: &capabilityID,
		ProtocolEndpointCapability:   &ProtocolEndpointCapability{ID: capabilityID, CapabilityKey: "stale-identity"},
	}}}
	probe := &supplierSyncProbeFake{}
	svc := NewSupplierSourceService(repo, accounts, probe, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	_, err := svc.Sync(context.Background(), 7)

	require.NoError(t, err)
	require.NotEmpty(t, accounts.updated)
	require.True(t, accounts.updated[0].ChatProbePassed)
}

func TestSupplierAccountNeedsProtocolRepublishDetectsTransportDrift(t *testing.T) {
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: supplierManagedCredentials(
			"https://qianfan.baidubce.com", "secret", map[string]string{"glm-5.3": "glm-5.3"},
			newapiconstant.ChannelTypeOpenAI,
		),
		ProtocolEndpointCapabilityID: func() *int64 { id := int64(797); return &id }(),
	}
	require.True(t, supplierAccountNeedsProtocolRepublish(
		account, "https://qianfan.baidubce.com", newapiconstant.ChannelTypeBaiduV2,
	))
}

func TestSupplierAccountNeedsProtocolRepublishDetectsLegacyNonExclusiveCredentials(t *testing.T) {
	// Pre-fix managed rows only had bare base_url; sync must republish exclusive shape.
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: map[string]any{
			"base_url":      "https://supplier.example/v1",
			"api_key":       "secret",
			"model_mapping": map[string]string{"model": "model"},
		},
	}
	require.False(t, accountDeclaresExclusiveProtocolEndpoints(account))
	require.True(t, supplierAccountNeedsProtocolRepublish(account, "https://supplier.example/v1", 1))

	migrated := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 1,
		Credentials: supplierManagedCredentials(
			"https://supplier.example/v1", "secret", map[string]string{"model": "model"}, 1),
	}
	require.False(t, supplierAccountNeedsProtocolRepublish(migrated, "https://supplier.example/v1", 1))
}
