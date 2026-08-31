package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUS048_CreateSupplierSourceEncryptsCredentialAndDefaultsBasePriority(t *testing.T) {
	ratio := 0.5
	repo := &supplierSourceRepoFake{}
	svc := NewSupplierSourceService(repo, nil, nil, supplierSourceTestEncryptor{}, supplierSourceTestFingerprinter{})

	created, err := svc.Create(context.Background(), SupplierSourceInput{
		SupplierName: " 佳杰 ", ChannelName: " stbl-5 ", Endpoint: "https://token.vstecscloud.com/v1/",
		Credential: "secret", Notes: " lowest ratio only ",
		Models: []SupplierSourceModelInput{{
			ClientModelID: " deepseek-v4-pro ", UpstreamModelID: " deepseek-v4-pro ", PurchaseRatio: &ratio,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.createCalls)
	require.Equal(t, "佳杰", created.SupplierName)
	require.Equal(t, "stbl-5", created.ChannelName)
	require.Equal(t, "https://token.vstecscloud.com/v1", created.Endpoint)
	require.Equal(t, "enc:secret", repo.stored.EncryptedCredential)
	require.Equal(t, "fp:secret", repo.stored.CredentialFingerprint)
	require.Equal(t, 100, repo.stored.BasePriority)
	require.Equal(t, "lowest ratio only", repo.stored.Notes)
	require.Len(t, repo.stored.Models, 1)
	require.Equal(t, "deepseek-v4-pro", repo.stored.Models[0].ClientModelID)
}

func TestUS048_CreateSupplierSourceAcceptsEmptyModelList(t *testing.T) {
	repo := &supplierSourceRepoFake{}
	svc := NewSupplierSourceService(repo, nil, nil, supplierSourceTestEncryptor{}, supplierSourceTestFingerprinter{})

	created, err := svc.Create(context.Background(), SupplierSourceInput{
		SupplierName: "FMGo", ChannelName: "seedance", Endpoint: "https://fmgo.example/v1", Credential: "secret",
	})

	require.NoError(t, err)
	require.NotNil(t, created.Models)
	require.Empty(t, created.Models)
}

func TestUS048_UpdateSupplierSourceBlankCredentialKeepsStoredSecretIdentity(t *testing.T) {
	basePriority := 120
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://old.example/v1",
		EncryptedCredential: "enc:old", CredentialFingerprint: "fp:old", BasePriority: 100,
		Models: []SupplierSourceModel{}, CreatedAt: time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC),
	}}
	svc := NewSupplierSourceService(repo, nil, nil, supplierSourceTestEncryptor{}, supplierSourceTestFingerprinter{})

	updated, err := svc.Update(context.Background(), 7, SupplierSourceInput{
		SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://new.example/v1",
		Credential: "", BasePriority: &basePriority, Models: []SupplierSourceModelInput{},
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "enc:old", updated.EncryptedCredential)
	require.Equal(t, "fp:old", updated.CredentialFingerprint)
	require.Equal(t, 120, updated.BasePriority)
	require.Equal(t, time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC), updated.CreatedAt)
}

func TestUS048_UpdateSupplierSourceProvidedCredentialRotatesStoredSecretIdentity(t *testing.T) {
	repo := &supplierSourceRepoFake{stored: &SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://old.example/v1",
		EncryptedCredential: "enc:old", CredentialFingerprint: "fp:old", BasePriority: 100,
	}}
	svc := NewSupplierSourceService(repo, nil, nil, supplierSourceTestEncryptor{}, supplierSourceTestFingerprinter{})

	updated, err := svc.Update(context.Background(), 7, SupplierSourceInput{
		SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://old.example/v1",
		Credential: "new-secret", Models: []SupplierSourceModelInput{},
	})

	require.NoError(t, err)
	require.Equal(t, "enc:new-secret", updated.EncryptedCredential)
	require.Equal(t, "fp:new-secret", updated.CredentialFingerprint)
	require.Equal(t, 100, updated.BasePriority)
}

func TestUS048_PriorityPreviewGroupsModelsBySourceBand(t *testing.T) {
	ratio05, ratio055, ratio09 := 0.5, 0.55, 0.9
	repo := &supplierSourceRepoFake{items: []SupplierSource{{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio05},
			{ClientModelID: "qwen-3.7-max", UpstreamModelID: "qwen-3.7-max", PurchaseRatio: &ratio055},
			{ClientModelID: "fallback-model", UpstreamModelID: "fallback-model", PurchaseRatio: &ratio09},
		},
	}}}
	svc := NewSupplierSourceService(repo, nil, nil, supplierSourceTestEncryptor{}, supplierSourceTestFingerprinter{})

	preview, err := svc.PriorityPreview(context.Background())

	require.NoError(t, err)
	require.Equal(t, []SupplierPriorityPreviewEntry{
		{SourceID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", DiscountBand: 3, DiscountPriority: 3, Priority: 103, ClientModelIDs: []string{"deepseek-v4-pro", "qwen-3.7-max"}},
		{SourceID: 7, SupplierName: "佳杰", ChannelName: "stbl-5", DiscountBand: 5, DiscountPriority: 5, Priority: 105, ClientModelIDs: []string{"fallback-model"}},
	}, preview.Entries)
}

type supplierSourceTestEncryptor struct{}

func (supplierSourceTestEncryptor) Encrypt(value string) (string, error) { return "enc:" + value, nil }
func (supplierSourceTestEncryptor) Decrypt(value string) (string, error) { return value, nil }

type supplierSourceTestFingerprinter struct{}

func (supplierSourceTestFingerprinter) Fingerprint(value string) (string, error) {
	return "fp:" + value, nil
}

type supplierSourceRepoFake struct {
	stored      *SupplierSource
	items       []SupplierSource
	createCalls int
	updateCalls int
}

func (r *supplierSourceRepoFake) Create(_ context.Context, source *SupplierSource) error {
	r.createCalls++
	copySource := cloneSupplierSourceForTest(source)
	if copySource.ID == 0 {
		copySource.ID = 1
	}
	r.stored = copySource
	*source = *cloneSupplierSourceForTest(copySource)
	return nil
}

func (r *supplierSourceRepoFake) Update(_ context.Context, source *SupplierSource) error {
	r.updateCalls++
	r.stored = cloneSupplierSourceForTest(source)
	return nil
}

func (r *supplierSourceRepoFake) Get(_ context.Context, id int64) (*SupplierSource, error) {
	if r.stored == nil || r.stored.ID != id {
		return nil, ErrSupplierSourceNotFound
	}
	cloned := cloneSupplierSourceForTest(r.stored)
	cloned.AccountConcurrency = ResolveSupplierSourceAccountConcurrency(cloned.AccountConcurrency)
	return cloned, nil
}

func (r *supplierSourceRepoFake) List(context.Context) ([]SupplierSource, error) {
	items := make([]SupplierSource, len(r.items))
	for index := range r.items {
		items[index] = *cloneSupplierSourceForTest(&r.items[index])
	}
	return items, nil
}

func cloneSupplierSourceForTest(source *SupplierSource) *SupplierSource {
	if source == nil {
		return nil
	}
	copySource := *source
	copySource.Models = make([]SupplierSourceModel, len(source.Models))
	copy(copySource.Models, source.Models)
	return &copySource
}
