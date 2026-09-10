//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestPricedServingGroupPriceMatchesSettlement(t *testing.T) {
	const model = "opaque-group-priced-model"
	group := &Group{ID: 10, Platform: PlatformNewAPI, ModelPricing: []ChannelModelPricing{{
		Models: []string{model}, BillingMode: BillingModeToken,
		InputPrice: ptrF(0.001), OutputPrice: ptrF(0.002),
	}}}
	billing := NewBillingService(nil, nil)
	resolver := NewModelPricingResolver(nil, billing)
	probe := tkChannelPricingProbeFromResolver(resolver)
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	resolved := resolver.Resolve(ctx, PricingInput{Model: model, GroupID: &group.ID, Group: group})
	require.Equal(t, PricingSourceGroup, resolved.Source)
	require.True(t, tkResolvedPricingChargeable(resolved))
	require.False(t, tkPricedServingGateRejected(ctx, billing.GetModelPricing, probe, newGateSettingService(PlatformNewAPI), model, PlatformNewAPI, group.ID))
	require.False(t, probe(ctx, model, group.ID+1), "another group's price cannot grant admission")
	require.False(t, probe(ctx, "other-opaque-model", group.ID), "a price card only admits matching models")
	group.ModelPricing[0].InputPrice, group.ModelPricing[0].OutputPrice = ptrF(0), ptrF(0)
	require.True(t, tkPricedServingGateRejected(ctx, billing.GetModelPricing, probe, newGateSettingService(PlatformNewAPI), model, PlatformNewAPI, group.ID))
}

func TestPricedServingUsesCurrentKeyBillingGroup(t *testing.T) {
	const model = "opaque-group-priced-model"
	group := &Group{ID: 20, ModelPricing: []ChannelModelPricing{{
		Models: []string{model}, BillingMode: BillingModePerRequest, PerRequestPrice: ptrF(0.1),
	}}}
	billing := NewBillingService(nil, nil)
	resolver := NewModelPricingResolver(nil, billing)
	c, response := newGateTestContext()
	c.Set("api_key", &APIKey{Group: group, GroupID: &group.ID})
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 10})
	require.True(t, tkCheckPricedServingGate(ctx, billing.GetModelPricing, tkChannelPricingProbeFromResolver(resolver), newGateSettingService(PlatformNewAPI), nil, c, tkGateWireOpenAI, PlatformNewAPI, model, model))
	require.Equal(t, 200, response.Code)
}
