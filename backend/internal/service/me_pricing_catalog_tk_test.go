//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- fakes -----

type fakeKeyAccess struct {
	groups   []Group
	groupErr error
	rates    map[int64]float64
	rateErr  error
	keys     []APIKey
	listErr  error
}

func (f *fakeKeyAccess) GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error) {
	if f.groupErr != nil {
		return nil, f.groupErr
	}
	return f.groups, nil
}

func (f *fakeKeyAccess) GetUserGroupRates(ctx context.Context, userID int64) (map[int64]float64, error) {
	if f.rateErr != nil {
		return nil, f.rateErr
	}
	return f.rates, nil
}

func (f *fakeKeyAccess) List(
	ctx context.Context, userID int64,
	_ pagination.PaginationParams, _ APIKeyListFilters,
) ([]APIKey, *pagination.PaginationResult, error) {
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	return f.keys, &pagination.PaginationResult{Total: int64(len(f.keys)), Page: 1, PageSize: 200, Pages: 1}, nil
}

type fakeChannelLister struct {
	channels []AvailableChannel
	err      error
}

func (f *fakeChannelLister) ListAvailable(ctx context.Context) ([]AvailableChannel, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.channels, nil
}

type fakeCatalogProvider struct {
	resp *PublicCatalogResponse
}

func (f *fakeCatalogProvider) BuildPublicCatalog(ctx context.Context) *PublicCatalogResponse {
	return f.resp
}

type fakeAccountSource struct {
	accounts []Account
	err      error
}

func (f *fakeAccountSource) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.accounts, nil
}

// ----- builders -----

func mkGroupForMe(id int64, name, platform string, rate float64) Group {
	return Group{
		ID: id, Name: name, Platform: platform,
		RateMultiplier: rate, IsExclusive: false,
		Status: StatusActive, SubscriptionType: "standard",
	}
}

func mkKeyForMe(id, userID int64, name string, groupID *int64) APIKey {
	return APIKey{
		ID: id, UserID: userID, Name: name,
		Key: "sk-test-" + name, Status: StatusAPIKeyActive,
		GroupID: groupID,
	}
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int64) *int64     { return &v }

func mkChannelWithModel(
	id int64, name string,
	groups []AvailableGroupRef,
	models []SupportedModel,
) AvailableChannel {
	return AvailableChannel{
		ID: id, Name: name, Status: StatusActive,
		Groups: groups, SupportedModels: models,
	}
}

func mkSupportedModel(modelID, platform string, p *ChannelModelPricing) SupportedModel {
	return SupportedModel{Name: modelID, Platform: platform, Pricing: p}
}

func mkPricing(input, output, cacheR float64) *ChannelModelPricing {
	return &ChannelModelPricing{
		BillingMode: BillingModeToken,
		InputPrice:  ptrF(input),
		OutputPrice: ptrF(output),
		CacheReadPrice: func() *float64 {
			if cacheR == 0 {
				return nil
			}
			return ptrF(cacheR)
		}(),
	}
}

func newService(k *fakeKeyAccess, c *fakeChannelLister, cat *fakeCatalogProvider) *MePricingCatalogService {
	return &MePricingCatalogService{keys: k, channels: c, catalog: cat}
}

// newServiceWithAccounts mirrors newService but wires the account-whitelist
// fallback source (introduced by the post-#325/#326 bridge PR). Existing
// tests that pass nil accounts get the legacy "channel-only" behavior.
func newServiceWithAccounts(
	k *fakeKeyAccess,
	c *fakeChannelLister,
	cat *fakeCatalogProvider,
	a *fakeAccountSource,
) *MePricingCatalogService {
	return &MePricingCatalogService{keys: k, channels: c, catalog: cat, accounts: a}
}

// mkAccountWithWhitelist builds an Account whose credentials.model_mapping
// is the identity map for the supplied model IDs — exactly the JSON shape
// admin UI's ModelWhitelistSelector emits when in whitelist mode.
func mkAccountWithWhitelist(id int64, name, platform string, channelType int, whitelist []string) Account {
	creds := map[string]any{}
	if len(whitelist) > 0 {
		mm := make(map[string]any, len(whitelist))
		for _, m := range whitelist {
			mm[m] = m
		}
		creds["model_mapping"] = mm
	}
	return Account{
		ID: id, Name: name, Platform: platform, ChannelType: channelType,
		Status: StatusActive, Schedulable: true,
		Credentials: creds,
	}
}

// mkPublicCatalogModel builds a PublicCatalogResponse entry with per-1k
// prices. CacheReadPer1K is optional — pass 0 to leave it absent.
func mkPublicCatalogModel(modelID, vendor string, in, out, cacheR float64) PublicCatalogModel {
	return PublicCatalogModel{
		ModelID:      modelID,
		Vendor:       vendor,
		Capabilities: []string{},
		Pricing: PublicCatalogPricing{
			Currency:          "USD",
			InputPer1KTokens:  in,
			OutputPer1KTokens: out,
			CacheReadPer1K:    cacheR,
		},
	}
}

// ----- tests -----

func TestMePricingCatalog_DefaultToFirstKeyGroup_AppliesGroupMultiplier(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.5)
	gMax := mkGroupForMe(20, "Max", "newapi", 0.8)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))

	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro, gMax}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{
			mkChannelWithModel(100, "ch1",
				[]AvailableGroupRef{{ID: 10, Name: "Pro", Platform: "newapi", RateMultiplier: 1.5}},
				[]SupportedModel{
					mkSupportedModel("gpt-4o", "newapi", mkPricing(0.000003, 0.000015, 0)),
				},
			),
		}},
		&fakeCatalogProvider{resp: nil},
	)

	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int64(10), resp.TargetGroup.ID)
	assert.Equal(t, 1.5, resp.TargetGroup.RateMultiplier)
	assert.Equal(t, 1.5, resp.TargetGroup.ListMultiplier)
	assert.False(t, resp.TargetGroup.HasOverride)
	require.Len(t, resp.Models, 1)
	// 0.000003 * 1000 * 1.5 = 0.0045
	assert.InDelta(t, 0.0045, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
	assert.InDelta(t, 0.0225, *resp.Models[0].YourPrice.OutputPer1K, 1e-9)
	// my_keys + accessible_groups populated
	require.Len(t, resp.MyKeys, 1)
	assert.Equal(t, int64(1), resp.MyKeys[0].ID)
	require.Len(t, resp.AccessibleGroups, 2)
	// IsCurrentForKey flag set correctly
	for _, g := range resp.AccessibleGroups {
		if g.ID == 10 {
			assert.True(t, g.IsCurrentForKey)
		} else {
			assert.False(t, g.IsCurrentForKey)
		}
	}
}

func TestMePricingCatalog_UserOverrideTakesPrecedence(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))

	svc := newService(
		&fakeKeyAccess{
			groups: []Group{gPro},
			keys:   []APIKey{k1},
			rates:  map[int64]float64{10: 0.5},
		},
		&fakeChannelLister{channels: []AvailableChannel{
			mkChannelWithModel(100, "ch1",
				[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
				[]SupportedModel{
					mkSupportedModel("gpt-4o", "newapi", mkPricing(0.000010, 0.000020, 0)),
				},
			),
		}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0.5, resp.TargetGroup.RateMultiplier)
	assert.Equal(t, 1.0, resp.TargetGroup.ListMultiplier)
	assert.True(t, resp.TargetGroup.HasOverride)
	// 0.000010 * 1000 * 0.5 = 0.005
	assert.InDelta(t, 0.005, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
	assert.InDelta(t, 0.010, *resp.Models[0].YourPrice.OutputPer1K, 1e-9)
}

func TestMePricingCatalog_ZeroRateIsLegitFree(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))

	svc := newService(
		&fakeKeyAccess{
			groups: []Group{gPro},
			keys:   []APIKey{k1},
			rates:  map[int64]float64{10: 0},
		},
		&fakeChannelLister{channels: []AvailableChannel{
			mkChannelWithModel(100, "ch1",
				[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
				[]SupportedModel{
					mkSupportedModel("gpt-4o", "newapi", mkPricing(0.000010, 0.000020, 0)),
				},
			),
		}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0.0, resp.TargetGroup.RateMultiplier)
	assert.True(t, resp.TargetGroup.HasOverride)
	assert.Equal(t, 0.0, *resp.Models[0].YourPrice.InputPer1K)
	assert.Equal(t, 0.0, *resp.Models[0].YourPrice.OutputPer1K)
}

func TestMePricingCatalog_ExploreOtherGroup(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	gMax := mkGroupForMe(20, "Max", "newapi", 0.8)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))

	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro, gMax}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{
			mkChannelWithModel(100, "ch1",
				[]AvailableGroupRef{{ID: 20, Platform: "newapi"}},
				[]SupportedModel{
					mkSupportedModel("gpt-4o", "newapi", mkPricing(0.000010, 0.000020, 0)),
				},
			),
		}},
		&fakeCatalogProvider{},
	)
	gid := int64(20)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{GroupID: &gid})
	require.NoError(t, err)
	assert.Equal(t, int64(20), resp.TargetGroup.ID)
	assert.Equal(t, 0.8, resp.TargetGroup.RateMultiplier)
	// Both groups still in picker; only Max is current
	require.Len(t, resp.AccessibleGroups, 2)
	for _, g := range resp.AccessibleGroups {
		if g.ID == 20 {
			assert.True(t, g.IsCurrentForKey)
		} else {
			assert.False(t, g.IsCurrentForKey)
		}
	}
}

func TestMePricingCatalog_ForbiddenGroup(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}},
		&fakeChannelLister{},
		&fakeCatalogProvider{},
	)
	other := int64(99)
	_, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{GroupID: &other})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMePricingGroupForbidden)
}

func TestMePricingCatalog_APIKeyConflictsWithGroup(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	gMax := mkGroupForMe(20, "Max", "newapi", 0.8)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro, gMax}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{},
	)
	keyID := int64(1)
	otherGroup := int64(20)
	_, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{
		APIKeyID: &keyID, GroupID: &otherGroup,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMePricingConflictingTargets)
}

func TestMePricingCatalog_APIKeyNotFound(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}},
		&fakeChannelLister{},
		&fakeCatalogProvider{},
	)
	keyID := int64(9999)
	_, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{APIKeyID: &keyID})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMePricingAPIKeyNotFound)
}

func TestMePricingCatalog_KeyWithoutGroupSkippedInPicker(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	kOrphan := mkKeyForMe(2, 7, "orphan", nil)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{kOrphan, k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.MyKeys, 1)
	assert.Equal(t, int64(1), resp.MyKeys[0].ID)
}

func TestMePricingCatalog_NoAccessibleGroups(t *testing.T) {
	svc := newService(&fakeKeyAccess{}, &fakeChannelLister{}, &fakeCatalogProvider{})
	_, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMePricingNoAccessibleGroups)
}

func TestMePricingCatalog_MultiChannelKeepsCheapest(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	expensive := mkChannelWithModel(100, "expensive",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00010, 0.00020, 0))},
	)
	cheap := mkChannelWithModel(101, "cheap",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00001, 0.00002, 0))},
	)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{expensive, cheap}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	// cheap: 0.00001*1000=0.01 input; expensive: 0.10 input — keep cheap
	assert.InDelta(t, 0.01, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
}

func TestMePricingCatalog_MultiChannelOneWithoutPrice(t *testing.T) {
	// A no-price candidate must NOT win over a priced one (LiteLLM-fallback gone wrong).
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	noPrice := mkChannelWithModel(100, "noPrice",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{{Name: "gpt-4o", Platform: "newapi", Pricing: nil}},
	)
	priced := mkChannelWithModel(101, "priced",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00001, 0.00002, 0))},
	)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{noPrice, priced}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	require.NotNil(t, resp.Models[0].YourPrice.InputPer1K)
}

func TestMePricingCatalog_CrossPlatformLeakGuard(t *testing.T) {
	// Channel mapped to a newapi group must NOT bleed an anthropic-platform
	// model into the newapi menu.
	gNewapi := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	mixed := mkChannelWithModel(100, "mixed",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{
			mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00001, 0.00002, 0)),
			mkSupportedModel("claude-opus-4-7", "anthropic", mkPricing(0.00003, 0.00015, 0)),
		},
	)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gNewapi}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{mixed}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "gpt-4o", resp.Models[0].ModelID)
}

func TestMePricingCatalog_NonActiveChannelExcluded(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	disabled := AvailableChannel{
		ID: 100, Name: "disabled", Status: "disabled",
		Groups:          []AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		SupportedModels: []SupportedModel{mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00001, 0.00002, 0))},
	}
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{disabled}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.Empty(t, resp.Models)
}

func TestMePricingCatalog_JoinsCatalogMetadata(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	ch := mkChannelWithModel(100, "ch",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00001, 0.00002, 0))},
	)
	catalog := &fakeCatalogProvider{resp: &PublicCatalogResponse{
		Data: []PublicCatalogModel{{
			ModelID: "gpt-4o", Vendor: "openai",
			ContextWindow: 128000, MaxOutputTokens: 16384,
			Capabilities: []string{"vision", "tool_use"},
		}},
	}}
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{ch}},
		catalog,
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, 128000, resp.Models[0].ContextWindow)
	assert.Equal(t, 16384, resp.Models[0].MaxOutputTokens)
	assert.Equal(t, []string{"vision", "tool_use"}, resp.Models[0].Capabilities)
	assert.Equal(t, "openai", resp.Models[0].Vendor)
}

func TestMePricingCatalog_CatalogMissingGracefulDegrade(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	ch := mkChannelWithModel(100, "ch",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{mkSupportedModel("gpt-4o", "newapi", mkPricing(0.00001, 0.00002, 0))},
	)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{ch}},
		&fakeCatalogProvider{resp: nil}, // nil catalog → still ok
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, 0, resp.Models[0].ContextWindow)
	assert.Equal(t, []string{}, resp.Models[0].Capabilities)
	assert.NotNil(t, resp.Models[0].YourPrice.InputPer1K)
}

func TestMePricingCatalog_ResolveByAPIKeyID(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 1.0)
	gMax := mkGroupForMe(20, "Max", "newapi", 0.8)
	kA := mkKeyForMe(1, 7, "proKey", ptrI(10))
	kB := mkKeyForMe(2, 7, "maxKey", ptrI(20))
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro, gMax}, keys: []APIKey{kA, kB}},
		&fakeChannelLister{},
		&fakeCatalogProvider{},
	)
	keyID := int64(2)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{APIKeyID: &keyID})
	require.NoError(t, err)
	assert.Equal(t, int64(20), resp.TargetGroup.ID)
}

func TestMePricingCatalog_UpstreamErrorsPropagate(t *testing.T) {
	wantErr := errors.New("groups boom")
	svc := newService(
		&fakeKeyAccess{groupErr: wantErr},
		&fakeChannelLister{},
		&fakeCatalogProvider{},
	)
	_, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.ErrorIs(t, err, wantErr)
}

func TestMePricingCatalog_PerRequestBillingPreservesPrice(t *testing.T) {
	gPro := mkGroupForMe(10, "Pro", "newapi", 2.0)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))
	pr := &ChannelModelPricing{
		BillingMode:     BillingModePerRequest,
		PerRequestPrice: ptrF(0.02),
	}
	ch := mkChannelWithModel(100, "ch",
		[]AvailableGroupRef{{ID: 10, Platform: "newapi"}},
		[]SupportedModel{mkSupportedModel("flux-fast", "newapi", pr)},
	)
	svc := newService(
		&fakeKeyAccess{groups: []Group{gPro}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{ch}},
		&fakeCatalogProvider{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, string(BillingModePerRequest), resp.Models[0].BillingMode)
	require.NotNil(t, resp.Models[0].YourPrice.PerRequest)
	assert.InDelta(t, 0.04, *resp.Models[0].YourPrice.PerRequest, 1e-9)
	assert.Nil(t, resp.Models[0].YourPrice.InputPer1K)
}

// ----- account-whitelist fallback (post-#325/#326 bridge) -----

// TestBuildForUser_AccountWhitelistOnly_NoChannels mirrors the production
// incident that motivated the bridge: operator created an account, ticked
// the model-whitelist boxes in admin, never configured any channels. The
// menu should reflect the whitelist with LiteLLM-derived default prices
// × group rate, not be empty.
func TestBuildForUser_AccountWhitelistOnly_NoChannels(t *testing.T) {
	gOpenAI := mkGroupForMe(30, "GPT", "openai", 2.0)
	k1 := mkKeyForMe(1, 7, "gpt-key", ptrI(30))
	acct := mkAccountWithWhitelist(11, "openai-oauth", "openai", 0, []string{"gpt-5.2", "gpt-4o"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gpt-5.2", "OpenAI", 0.005, 0.020, 0),
			mkPublicCatalogModel("gpt-4o", "OpenAI", 0.0025, 0.010, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gOpenAI}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 2, "both whitelisted GPT models should surface")
	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}
	require.Contains(t, byID, "gpt-5.2")
	require.NotNil(t, byID["gpt-5.2"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.010, *byID["gpt-5.2"].YourPrice.InputPer1K, 1e-9, "0.005 catalog × 2.0 effective rate")
	require.NotNil(t, byID["gpt-5.2"].YourPrice.OutputPer1K)
	assert.InDelta(t, 0.040, *byID["gpt-5.2"].YourPrice.OutputPer1K, 1e-9, "0.020 catalog × 2.0 effective rate")
	require.NotNil(t, byID["gpt-4o"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.005, *byID["gpt-4o"].YourPrice.InputPer1K, 1e-9)
}

// TestBuildForUser_ChannelAndAccount_ChannelWins guards the "channel
// price is authoritative on conflict" rule. The catalog input for the
// shared model is set to a very different number than the channel price
// so a regression that overwrote channel rows would be obvious.
func TestBuildForUser_ChannelAndAccount_ChannelWins(t *testing.T) {
	gOpenAI := mkGroupForMe(30, "GPT", "openai", 1.0)
	k1 := mkKeyForMe(1, 7, "gpt-key", ptrI(30))
	channel := mkChannelWithModel(100, "operator-ch",
		[]AvailableGroupRef{{ID: 30, Platform: "openai"}},
		[]SupportedModel{mkSupportedModel("gpt-5.2", "openai", mkPricing(0.001, 0.002, 0))},
	)
	acct := mkAccountWithWhitelist(11, "openai-oauth", "openai", 0, []string{"gpt-5.2", "gpt-4o"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gpt-5.2", "OpenAI", 0.999, 0.999, 0),
			mkPublicCatalogModel("gpt-4o", "OpenAI", 0.0025, 0.010, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gOpenAI}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{channel}},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 2)
	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}
	require.NotNil(t, byID["gpt-5.2"].YourPrice.InputPer1K)
	assert.InDelta(t, 1.0, *byID["gpt-5.2"].YourPrice.InputPer1K, 1e-9,
		"0.001 channel × 1000 (per-token → per-1k) × 1.0 rate; catalog 0.999 MUST NOT win")
	require.NotNil(t, byID["gpt-4o"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.0025, *byID["gpt-4o"].YourPrice.InputPer1K, 1e-9, "gpt-4o is account-only — catalog 0.0025 × 1.0 rate")
}

// TestBuildForUser_AccountWhitelist_VendorPrefix exercises reuse of
// stripVendorPrefixForCatalogLookup (PR #326). An account whitelisting an
// OpenRouter-style "<vendor>/<model>" must still resolve to the LiteLLM
// catalog's bare model_id row.
func TestBuildForUser_AccountWhitelist_VendorPrefix(t *testing.T) {
	gAnthropic := mkGroupForMe(40, "claude", "anthropic", 1.0)
	k1 := mkKeyForMe(1, 7, "claude-key", ptrI(40))
	acct := mkAccountWithWhitelist(11, "anthropic-key", "anthropic", 0,
		[]string{"anthropic/claude-3-5-sonnet"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("claude-3-5-sonnet", "Anthropic", 0.003, 0.015, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAnthropic}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "anthropic/claude-3-5-sonnet", resp.Models[0].ModelID,
		"row keeps the operator-facing ID; only the catalog lookup uses the stripped form")
	require.NotNil(t, resp.Models[0].YourPrice.InputPer1K)
	assert.InDelta(t, 0.003, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
}

// TestBuildForUser_AccountWhitelist_CrossPlatformGuard confirms an
// openai-platform account does not leak into an anthropic-platform group
// even when both share a group binding edge.
func TestBuildForUser_AccountWhitelist_CrossPlatformGuard(t *testing.T) {
	gAnthropic := mkGroupForMe(40, "claude", "anthropic", 1.0)
	k1 := mkKeyForMe(1, 7, "claude-key", ptrI(40))
	// openai account that somehow ended up on an anthropic group's
	// schedulable list (the scheduler would reject it; we double-check
	// the menu builder enforces the same rule).
	acct := mkAccountWithWhitelist(11, "stray-openai", "openai", 0, []string{"gpt-5.2"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gpt-5.2", "OpenAI", 0.005, 0.020, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAnthropic}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.Empty(t, resp.Models, "openai account must not surface on an anthropic group menu")
}

// TestBuildForUser_AccountWhitelist_MappingModeIgnored documents the
// whitelist-only contract: an entry with from != to is a routing
// rewrite (alias), not a user-visible menu item.
func TestBuildForUser_AccountWhitelist_MappingModeIgnored(t *testing.T) {
	gAnthropic := mkGroupForMe(40, "claude", "anthropic", 1.0)
	k1 := mkKeyForMe(1, 7, "claude-key", ptrI(40))
	acct := Account{
		ID: 11, Name: "alias-only", Platform: "anthropic",
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				// mapping mode: "claude-3-haiku" requests routed to claude-3-5-sonnet upstream
				"claude-3-haiku": "claude-3-5-sonnet",
				// whitelist mode: real menu offering
				"claude-3-5-sonnet": "claude-3-5-sonnet",
			},
		},
	}
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("claude-3-haiku", "Anthropic", 0.0005, 0.0025, 0),
			mkPublicCatalogModel("claude-3-5-sonnet", "Anthropic", 0.003, 0.015, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAnthropic}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "claude-3-5-sonnet", resp.Models[0].ModelID,
		"only the identity-mapped (whitelist) entry surfaces; the alias rewrite contributes nothing")
}

// TestBuildForUser_AccountWhitelist_MissingFromLiteLLM_StillEmits
// documents the forgiving fallback: a model that LiteLLM doesn't price
// yet (e.g. a freshly released family) still appears in the menu with
// nil prices, so the user knows the gateway can serve it.
func TestBuildForUser_AccountWhitelist_MissingFromLiteLLM_StillEmits(t *testing.T) {
	gOpenAI := mkGroupForMe(30, "GPT", "openai", 1.0)
	k1 := mkKeyForMe(1, 7, "gpt-key", ptrI(30))
	acct := mkAccountWithWhitelist(11, "oauth", "openai", 0, []string{"gpt-5.5-not-in-catalog"})
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gOpenAI}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: &PublicCatalogResponse{}},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	assert.Equal(t, "gpt-5.5-not-in-catalog", resp.Models[0].ModelID)
	assert.Nil(t, resp.Models[0].YourPrice.InputPer1K, "unknown to catalog → nil (UI renders —)")
	assert.Nil(t, resp.Models[0].YourPrice.OutputPer1K)
}

// TestBuildForUser_AccountSourceError_DoesNotKillResponse documents the
// best-effort posture of the fallback: a failing account read must not
// break the catalog endpoint, otherwise a transient DB hiccup in account
// listing would 500 the /pricing page.
func TestBuildForUser_AccountSourceError_DoesNotKillResponse(t *testing.T) {
	gOpenAI := mkGroupForMe(30, "GPT", "openai", 1.0)
	k1 := mkKeyForMe(1, 7, "gpt-key", ptrI(30))
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gOpenAI}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: &PublicCatalogResponse{}},
		&fakeAccountSource{err: errors.New("db unavailable")},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Models)
}

// TestBuildForUser_AccountWhitelist_NewapiRequiresChannelType pins the
// fifth-platform doctrine (docs/approved/newapi-as-fifth-platform.md §2.1):
// newapi accounts join the pool ONLY when channel_type > 0 — an
// incompletely configured newapi account (channel_type = 0) has no New
// API adaptor target and must not contribute menu rows. The scheduler
// applies the same rule via IsOpenAICompatPoolMember; the fallback path
// must not diverge.
func TestBuildForUser_AccountWhitelist_NewapiRequiresChannelType(t *testing.T) {
	gNewapi := mkGroupForMe(50, "newapi-pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 7, "newapi-key", ptrI(50))
	bad := mkAccountWithWhitelist(11, "incomplete-newapi", "newapi", 0, []string{"gemini-2.5-pro"})
	good := mkAccountWithWhitelist(12, "configured-newapi", "newapi", 31, []string{"qwen-3-max"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gemini-2.5-pro", "Google", 0.00125, 0.005, 0),
			mkPublicCatalogModel("qwen-3-max", "Alibaba", 0.0012, 0.0024, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gNewapi}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{bad, good}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1, "channel_type=0 newapi account must be filtered out (no adaptor target)")
	assert.Equal(t, "qwen-3-max", resp.Models[0].ModelID,
		"only the channel_type>0 newapi account surfaces — matches scheduler IsOpenAICompatPoolMember")
}

// TestBuildForUser_ChannelsErrorDoesNotKillFallback documents the
// best-effort posture on the channel-listing side: a transient channel
// read failure must NOT empty the whole menu when an account-whitelist
// fallback could still populate it. Behavior change from the pre-bridge
// code, which used to return an empty []MePricingModel on channel err.
func TestBuildForUser_ChannelsErrorDoesNotKillFallback(t *testing.T) {
	gOpenAI := mkGroupForMe(30, "GPT", "openai", 1.0)
	k1 := mkKeyForMe(1, 7, "gpt-key", ptrI(30))
	acct := mkAccountWithWhitelist(11, "openai-oauth", "openai", 0, []string{"gpt-4o"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gpt-4o", "OpenAI", 0.0025, 0.010, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gOpenAI}, keys: []APIKey{k1}},
		&fakeChannelLister{err: errors.New("channel db unavailable")},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1, "channel error must not block account fallback")
	assert.Equal(t, "gpt-4o", resp.Models[0].ModelID)
	require.NotNil(t, resp.Models[0].YourPrice.InputPer1K)
	assert.InDelta(t, 0.0025, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
}
