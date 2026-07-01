//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
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

// TestMePricingCatalog_TiersFromPublicCatalog pins the single-source-of-truth
// contract: Your-Menu surfaces the input-token interval (阶梯) ladder copied
// verbatim from the public catalog (no rate scaling — me-pricing is the official
// list price), so /pricing and me/pricing-catalog never diverge on tiers.
func TestMePricingCatalog_TiersFromPublicCatalog(t *testing.T) {
	g := mkGroupForMe(10, "Pro", "newapi", 1.5)
	k1 := mkKeyForMe(1, 7, "default", ptrI(10))

	maxTok := func(v int) *int { return &v }
	catalogModel := PublicCatalogModel{
		ModelID:      "qwen-plus",
		Vendor:       "dashscope",
		Capabilities: []string{},
		Pricing: PublicCatalogPricing{
			Currency:          "USD",
			InputPer1KTokens:  0.0001194,
			OutputPer1KTokens: 0.0002985,
			Tiers: []PublicCatalogTier{
				{MinTokens: 0, MaxTokens: maxTok(128000), InputPer1KTokens: 0.0001194, OutputPer1KTokens: 0.0002985, CacheReadPer1K: 0.0001194},
				{MinTokens: 128000, MaxTokens: nil, InputPer1KTokens: 0.0007164, OutputPer1KTokens: 0.0071642},
			},
		},
	}

	svc := newService(
		&fakeKeyAccess{groups: []Group{g}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{
			mkChannelWithModel(100, "ch1",
				[]AvailableGroupRef{{ID: 10, Name: "Pro", Platform: "newapi", RateMultiplier: 1.5}},
				[]SupportedModel{mkSupportedModel("qwen-plus", "newapi", mkPricing(0.0000001194, 0.0000002985, 0))},
			),
		}},
		&fakeCatalogProvider{resp: &PublicCatalogResponse{Object: "list", Data: []PublicCatalogModel{catalogModel}}},
	)

	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1)
	tiers := resp.Models[0].YourPrice.Tiers
	require.Len(t, tiers, 2, "ladder copied from the public catalog")

	// tier 1: bounded, verbatim (no ×1.5 scaling — official list price).
	assert.Equal(t, 0, tiers[0].MinTokens)
	require.NotNil(t, tiers[0].MaxTokens)
	assert.Equal(t, 128000, *tiers[0].MaxTokens)
	require.NotNil(t, tiers[0].InputPer1K)
	assert.InDelta(t, 0.0001194, *tiers[0].InputPer1K, 1e-12, "verbatim from catalog, not scaled by 1.5")
	require.NotNil(t, tiers[0].CacheReadPer1K)
	assert.InDelta(t, 0.0001194, *tiers[0].CacheReadPer1K, 1e-12)

	// top tier: open-ended, costlier; no cache-read → pointer stays nil.
	assert.Nil(t, tiers[1].MaxTokens)
	assert.InDelta(t, 0.0007164, *tiers[1].InputPer1K, 1e-12)
	assert.Nil(t, tiers[1].CacheReadPer1K)
}

// TK: 价格一律官方基价（倍率 1.0），TargetGroup 的 RateMultiplier 字段仍反映
// 真实生效倍率（1.5），但不再作用于 YourPrice。
func TestMePricingCatalog_DefaultToFirstKeyGroup_OfficialPrice(t *testing.T) {
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
	// 官方基价：0.000003 * 1000 = 0.003（不乘 1.5 倍率）
	assert.InDelta(t, 0.003, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
	assert.InDelta(t, 0.015, *resp.Models[0].YourPrice.OutputPer1K, 1e-9)
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
	// 覆写仍体现在 RateMultiplier 字段（生效 0.5），但价格按官方基价展示。
	assert.Equal(t, 0.5, resp.TargetGroup.RateMultiplier)
	assert.Equal(t, 1.0, resp.TargetGroup.ListMultiplier)
	assert.True(t, resp.TargetGroup.HasOverride)
	// 官方基价：0.000010 * 1000 = 0.01（不乘 0.5 覆写）
	assert.InDelta(t, 0.01, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
	assert.InDelta(t, 0.02, *resp.Models[0].YourPrice.OutputPer1K, 1e-9)
}

// TK: rate 0（免费订阅）仍体现在 RateMultiplier 字段，但 pricing 页与倍率
// 脱钩——展示官方基价而非 0。真实计费在网关按 0 倍率执行。
func TestMePricingCatalog_ZeroRateShownAsOfficialPrice(t *testing.T) {
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
	// 官方基价（不因 rate 0 归零）：0.000010 * 1000 = 0.01
	assert.InDelta(t, 0.01, *resp.Models[0].YourPrice.InputPer1K, 1e-9)
	assert.InDelta(t, 0.02, *resp.Models[0].YourPrice.OutputPer1K, 1e-9)
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
	// 官方基价：per-request 0.02（不乘 2.0 倍率）
	assert.InDelta(t, 0.02, *resp.Models[0].YourPrice.PerRequest, 1e-9)
	assert.Nil(t, resp.Models[0].YourPrice.InputPer1K)
}

// ----- account-whitelist fallback (post-#325/#326 bridge) -----

// TestBuildForUser_AccountWhitelistOnly_NoChannels mirrors the production
// incident that motivated the bridge: operator created an account, ticked
// the model-whitelist boxes in admin, never configured any channels. The
// menu should reflect the whitelist with LiteLLM-derived OFFICIAL prices
// (decoupled from group rate), not be empty.
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
	assert.InDelta(t, 0.005, *byID["gpt-5.2"].YourPrice.InputPer1K, 1e-9, "0.005 catalog 官方价（不乘 2.0 倍率）")
	require.NotNil(t, byID["gpt-5.2"].YourPrice.OutputPer1K)
	assert.InDelta(t, 0.020, *byID["gpt-5.2"].YourPrice.OutputPer1K, 1e-9, "0.020 catalog 官方价（不乘 2.0 倍率）")
	require.NotNil(t, byID["gpt-4o"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.0025, *byID["gpt-4o"].YourPrice.InputPer1K, 1e-9, "0.0025 catalog 官方价")
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

// TestBuildForUser_AuthorizedGroups_CrossGroup pins the "授权分组" column: each
// model's AuthorizedGroups lists every accessible group that can serve it (not
// just the target group), so the user can see which key/group to use. Ordering:
// current/target group first, then exclusive, then by name.
func TestBuildForUser_AuthorizedGroups_CrossGroup(t *testing.T) {
	gTarget := mkGroupForMe(30, "GPT专属", "openai", 1.0)
	gTarget.IsExclusive = true
	gOther := mkGroupForMe(31, "GPT公开", "openai", 1.0) // public
	k1 := mkKeyForMe(1, 7, "gpt-key", ptrI(30))
	// gpt-5 served by BOTH groups; gpt-a only by the target group.
	chShared := mkChannelWithModel(100, "shared-ch",
		[]AvailableGroupRef{{ID: 30, Platform: "openai"}, {ID: 31, Platform: "openai"}},
		[]SupportedModel{mkSupportedModel("gpt-5", "openai", mkPricing(0.001, 0.002, 0))},
	)
	chTargetOnly := mkChannelWithModel(101, "target-ch",
		[]AvailableGroupRef{{ID: 30, Platform: "openai"}},
		[]SupportedModel{mkSupportedModel("gpt-a", "openai", mkPricing(0.001, 0.002, 0))},
	)
	catalog := &PublicCatalogResponse{Data: []PublicCatalogModel{
		mkPublicCatalogModel("gpt-5", "OpenAI", 0.001, 0.002, 0),
		mkPublicCatalogModel("gpt-a", "OpenAI", 0.001, 0.002, 0),
	}}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gTarget, gOther}, keys: []APIKey{k1}},
		&fakeChannelLister{channels: []AvailableChannel{chShared, chTargetOnly}},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{},
	)
	resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
	require.NoError(t, err)
	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}

	// gpt-5 is served by the target group AND the other group → both listed,
	// current/exclusive target first.
	g5, ok := byID["gpt-5"]
	require.True(t, ok)
	require.Len(t, g5.AuthorizedGroups, 2, "gpt-5 served by both accessible groups")
	assert.Equal(t, int64(30), g5.AuthorizedGroups[0].ID, "current/target group sorts first")
	assert.True(t, g5.AuthorizedGroups[0].IsCurrentForKey)
	assert.True(t, g5.AuthorizedGroups[0].IsExclusive, "target group is exclusive")
	assert.Equal(t, int64(31), g5.AuthorizedGroups[1].ID)
	assert.False(t, g5.AuthorizedGroups[1].IsCurrentForKey)

	// gpt-a is only on the target group → single-group column.
	ga, ok := byID["gpt-a"]
	require.True(t, ok)
	require.Len(t, ga.AuthorizedGroups, 1)
	assert.Equal(t, int64(30), ga.AuthorizedGroups[0].ID)

	require.NotNil(t, resp.AuthorizedGroupsByModel)
	assert.Len(t, resp.AuthorizedGroupsByModel["gpt-5"], 2)
	assert.Len(t, resp.AuthorizedGroupsByModel["gpt-a"], 1)
}
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
	good := mkAccountWithWhitelist(12, "configured-newapi", "newapi", 31, []string{"qwen-plus"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gemini-2.5-pro", "Google", 0.00125, 0.005, 0),
			mkPublicCatalogModel("qwen-plus", "Alibaba", 0.0012, 0.0024, 0),
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
	assert.Equal(t, "qwen-plus", resp.Models[0].ModelID,
		"only manifest-listed newapi whitelist models surface in Group Catalog")
}

// ----- platform-default fallback (unrestricted native OAuth accounts) -----

// modelIDsOf collects the model_id set of a menu for order-independent
// comparison (buildModelsForGroup sorts alpha; the canonical list is in
// declaration order).
func modelIDsOf(models []MePricingModel) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ModelID)
	}
	sort.Strings(ids)
	return ids
}

// TestBuildForUser_AnthropicUnrestricted_ListsServableModels is the core
// fix for the user_id=16 incident: a native Anthropic OAuth account carries
// no channel and no model_mapping whitelist (empty = all models allowed),
// so before the platform-default fallback both the channel stage and the
// whitelist stage produced nothing and the menu was empty. The menu must now
// list the empirically-servable Claude set — the SAME source the public
// /pricing catalog filters by (supportedAnthropicCatalogModels) — with
// catalog prices joined where available.
func TestBuildForUser_AnthropicUnrestricted_ListsServableModels(t *testing.T) {
	gAnthropic := mkGroupForMe(40, "default", "anthropic", 1.0)
	k1 := mkKeyForMe(1, 16, "default", ptrI(40))
	// nil whitelist → creds has no model_mapping → unrestricted account.
	acct := mkAccountWithWhitelist(11, "claude-oauth", "anthropic", 0, nil)
	require.False(t, accountHasModelRestriction(acct.Credentials),
		"empty model_mapping must read as unrestricted")
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("claude-opus-4-8", "Anthropic", 0.005, 0.025, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAnthropic}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)

	want := supportedCatalogModelIDsForPlatform(PlatformAnthropic)
	sort.Strings(want)
	assert.Equal(t, want, modelIDsOf(resp.Models),
		"menu must equal the empirically-servable Claude set (same source as public catalog)")

	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}
	// Catalog-priced model gets its price joined (× rate 1.0).
	require.Contains(t, byID, "claude-opus-4-8")
	require.NotNil(t, byID["claude-opus-4-8"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.005, *byID["claude-opus-4-8"].YourPrice.InputPer1K, 1e-9)
	// A model not in the catalog still surfaces (name is what the user needs).
	require.Contains(t, byID, "claude-sonnet-4-6")
	// Servable extra beyond canonical DefaultModels is included (claude-opus-4-1).
	require.Contains(t, byID, "claude-opus-4-1")
	// Deprecated IDs are never advertised.
	assert.NotContains(t, byID, "claude-3-5-haiku-20241022",
		"deprecated models must not appear in the menu")
}

// TestBuildForUser_AnthropicRestricted_RegistryNotMixedIn guards the
// branch boundary: an Anthropic account WITH a whitelist is restricted to
// exactly those models — the canonical platform list must not leak in.
func TestBuildForUser_AnthropicRestricted_RegistryNotMixedIn(t *testing.T) {
	gAnthropic := mkGroupForMe(40, "default", "anthropic", 1.0)
	k1 := mkKeyForMe(1, 16, "default", ptrI(40))
	acct := mkAccountWithWhitelist(11, "claude-oauth", "anthropic", 0, []string{"claude-opus-4-8"})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("claude-opus-4-8", "Anthropic", 0.005, 0.025, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAnthropic}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Models, 1, "restricted account → only its whitelist, no canonical list")
	assert.Equal(t, "claude-opus-4-8", resp.Models[0].ModelID)
}

// TestBuildForUser_EmptyPool_StaysEmpty confirms a group with no
// schedulable accounts advertises nothing — we do not list models a dead
// group cannot serve.
func TestBuildForUser_EmptyPool_StaysEmpty(t *testing.T) {
	gAnthropic := mkGroupForMe(40, "default", "anthropic", 1.0)
	k1 := mkKeyForMe(1, 16, "default", ptrI(40))
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAnthropic}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: &PublicCatalogResponse{}},
		&fakeAccountSource{accounts: []Account{}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.Empty(t, resp.Models)
}

// TestBuildForUser_NewapiUnrestricted_NoCanonicalLeak pins the platform
// scoping: newapi is channel/channel_type driven and owns no canonical
// model list, so an unrestricted newapi account must NOT fall back to the
// Claude default arm of defaultModelsListCandidateIDs.
func TestBuildForUser_NewapiUnrestricted_NoCanonicalLeak(t *testing.T) {
	gNewapi := mkGroupForMe(50, "newapi-pro", "newapi", 1.0)
	k1 := mkKeyForMe(1, 16, "newapi-key", ptrI(50))
	// channel_type>0 so the account IS in scope — isolates the platform
	// registry decision from the pool-membership decision.
	acct := mkAccountWithWhitelist(11, "newapi-oauth", "newapi", 31, nil)
	require.False(t, accountHasModelRestriction(acct.Credentials))
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gNewapi}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: &PublicCatalogResponse{}},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)
	assert.Empty(t, resp.Models, "newapi has no canonical list — no Claude models may leak in")
}

func TestBuildForUser_GeminiUnrestricted_ListsServableModels(t *testing.T) {
	gGemini := mkGroupForMe(55, "google", "gemini", 1.0)
	k1 := mkKeyForMe(1, 16, "gemini-key", ptrI(55))
	acct := mkAccountWithWhitelist(11, "gemini-oauth", "gemini", 0, nil)
	require.False(t, accountHasModelRestriction(acct.Credentials),
		"empty model_mapping must read as unrestricted")
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gemini-2.5-flash", "Google", 0.0003, 0.0025, 0),
			mkPublicCatalogModel("gemini-2.5-pro", "Google", 0.00125, 0.005, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gGemini}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)

	want := supportedCatalogModelIDsForPlatform(PlatformGemini)
	require.NotEmpty(t, want, "gemini served set must be non-empty")
	sort.Strings(want)
	assert.Equal(t, want, modelIDsOf(resp.Models),
		"gemini menu must equal the empirical served set, not raw geminicli.DefaultModels")

	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}
	require.Contains(t, byID, "gemini-2.5-flash")
	require.Contains(t, byID, "gemini-2.5-pro")
	for _, dead := range []string{"gemini-2.0-flash", "gemini-3-flash-preview", "gemini-3-pro-preview", "gemini-3.1-pro-preview", "gemini-3.5-flash"} {
		assert.NotContains(t, byID, dead, "advertised_dead %s must not appear in the menu", dead)
	}
}

func TestBuildForUser_AntigravityMapped_ListsPricedReprobedGeminiModels(t *testing.T) {
	gAG := mkGroupForMe(56, "antigravity", "antigravity", 1.0)
	k1 := mkKeyForMe(1, 16, "ag-key", ptrI(56))
	acct := mkAccountWithWhitelist(11, "ag-oauth", "antigravity", 0, []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-thinking",
		"gemini-3-flash",
		"gemini-2.5-pro", // priced in Gemini, but 2026-06-23 Antigravity reprobe stayed inconclusive; must stay hidden.
	})
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("gemini-2.5-flash", "vertex_ai-language-models", 0.0003, 0.0025, 0.00003),
			mkPublicCatalogModel("gemini-3-flash", "vertex_ai-language-models", 0.0005, 0.003, 0.00005),
			mkPublicCatalogModel("gemini-2.5-pro", "vertex_ai-language-models", 0.00125, 0.005, 0),
			mkPublicCatalogModel("gemini-2.5-flash-thinking", "antigravity", 0.0003, 0.0025, 0.00003),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gAG}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)

	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}
	require.Contains(t, byID, "gemini-2.5-flash")
	require.Contains(t, byID, "gemini-3-flash")
	require.NotContains(t, byID, "gemini-2.5-pro",
		"Antigravity user menu must not expose mapping ids outside the 200-probed allowlist")
	require.Contains(t, byID, "gemini-2.5-flash-thinking")
	thinking := byID["gemini-2.5-flash-thinking"]
	require.NotNil(t, thinking.YourPrice.InputPer1K, "Antigravity thinking id must show a price in the user menu")
	assert.InDelta(t, 0.0003, *thinking.YourPrice.InputPer1K, 1e-12)
	require.NotNil(t, thinking.YourPrice.OutputPer1K)
	assert.InDelta(t, 0.0025, *thinking.YourPrice.OutputPer1K, 1e-12)
	require.NotNil(t, thinking.YourPrice.CacheReadPer1K)
	assert.InDelta(t, 0.00003, *thinking.YourPrice.CacheReadPer1K, 1e-12)
	assert.Equal(t, "antigravity", thinking.Vendor)
}

// TestBuildForUser_GrokUnrestricted_ListsServableModels is the regression for
// the 2026-06-20 incident: selecting a grok group on /pricing showed an EMPTY
// "分组目录". Grok is a native OAuth-relay platform — its accounts carry no
// channel and no credentials.model_mapping (unrestricted), so before the
// platformDefaultModelIDs grok arm both the channel stage and the whitelist
// stage produced nothing. The menu must now list the curated grok served set
// (the priced overlay models — the SAME source the public /pricing catalog
// filters by), with catalog prices joined where available.
func TestBuildForUser_GrokUnrestricted_ListsServableModels(t *testing.T) {
	gGrok := mkGroupForMe(60, "grok", "grok", 1.0)
	k1 := mkKeyForMe(1, 16, "grok-key", ptrI(60))
	// channel_type=0, nil whitelist → unrestricted native grok OAuth account.
	acct := mkAccountWithWhitelist(11, "grok-oauth", "grok", 0, nil)
	require.False(t, accountHasModelRestriction(acct.Credentials),
		"empty model_mapping must read as unrestricted")
	require.True(t, accountInGroupScope(&acct, "grok"),
		"a channel_type=0 grok account must be in scope for a grok group")
	catalog := &PublicCatalogResponse{
		Data: []PublicCatalogModel{
			mkPublicCatalogModel("grok-4.3", "xai", 0.00125, 0.0025, 0),
			mkPublicCatalogModel("grok-code-fast-1", "xai", 0.001, 0.002, 0),
		},
	}
	svc := newServiceWithAccounts(
		&fakeKeyAccess{groups: []Group{gGrok}, keys: []APIKey{k1}},
		&fakeChannelLister{},
		&fakeCatalogProvider{resp: catalog},
		&fakeAccountSource{accounts: []Account{acct}},
	)
	resp, err := svc.BuildForUser(context.Background(), 16, MePricingCatalogOptions{})
	require.NoError(t, err)

	want := supportedCatalogModelIDsForPlatform(PlatformGrok)
	require.NotEmpty(t, want, "grok served set must be non-empty")
	sort.Strings(want)
	assert.Equal(t, want, modelIDsOf(resp.Models),
		"grok menu must equal the curated grok served set (same source as public catalog)")

	byID := map[string]MePricingModel{}
	for _, m := range resp.Models {
		byID[m.ModelID] = m
	}
	// Catalog-priced model gets its price joined.
	require.Contains(t, byID, "grok-4.3")
	require.NotNil(t, byID["grok-4.3"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.00125, *byID["grok-4.3"].YourPrice.InputPer1K, 1e-9)
	require.Contains(t, byID, "grok-code-fast-1")
	require.NotNil(t, byID["grok-code-fast-1"].YourPrice.InputPer1K)
	assert.InDelta(t, 0.001, *byID["grok-code-fast-1"].YourPrice.InputPer1K, 1e-9)
	// The grok-imagine media models surface even without a catalog row joined.
	require.Contains(t, byID, "grok-imagine-image")
	require.Contains(t, byID, "grok-imagine-video")
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
