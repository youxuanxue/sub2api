package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestResolveDataLifecyclePolicyRoleLimits(t *testing.T) {
	cleanup := config.OpsCleanupConfig{
		SystemLogRetentionDays: 20,
		ErrorLogRetentionDays:  60,
	}

	prod := resolveDataLifecyclePolicy(&config.Config{
		Server: config.ServerConfig{FrontendURL: "https://api.tokenkey.dev"},
		DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays:         180,
				UsageBillingDedupDays: 365,
			},
		},
	}, cleanup)
	require.Equal(t, "prod", prod.role)
	require.Equal(t, 90, prod.usageLogRetentionDays)
	require.Equal(t, 30, prod.errorLogRetentionDays)
	require.Equal(t, 7, prod.systemLogRetentionDays)
	require.Equal(t, 365, prod.billingDedupRetentionDays)

	edge := resolveDataLifecyclePolicy(&config.Config{
		Server: config.ServerConfig{FrontendURL: "https://api-us1.tokenkey.dev"},
		DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays:         90,
				UsageBillingDedupDays: 365,
			},
		},
	}, cleanup)
	require.Equal(t, "edge", edge.role)
	require.Equal(t, 7, edge.usageLogRetentionDays)
	require.Equal(t, 14, edge.errorLogRetentionDays)
	require.Equal(t, 3, edge.systemLogRetentionDays)
	require.Equal(t, 365, edge.billingDedupRetentionDays)
}

func TestResolveDataLifecyclePolicyPreservesShorterAndZeroValues(t *testing.T) {
	policy := resolveDataLifecyclePolicy(&config.Config{
		Server: config.ServerConfig{FrontendURL: "https://api-us1.tokenkey.dev"},
		DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays:         2,
				UsageBillingDedupDays: 0,
			},
		},
	}, config.OpsCleanupConfig{
		SystemLogRetentionDays: 0,
		ErrorLogRetentionDays:  5,
	})

	require.Equal(t, 2, policy.usageLogRetentionDays)
	require.Equal(t, 5, policy.errorLogRetentionDays)
	require.Zero(t, policy.systemLogRetentionDays)
	require.Equal(t, 365, policy.billingDedupRetentionDays)
}

func TestResolveDataLifecyclePolicyFixesBillingDedupAtOneYear(t *testing.T) {
	for _, configured := range []int{0, 30, 730} {
		cfg := &config.Config{
			DashboardAgg: config.DashboardAggregationConfig{
				Retention: config.DashboardAggregationRetentionConfig{
					UsageBillingDedupDays: configured,
				},
			},
		}
		policy := resolveDataLifecyclePolicy(cfg, config.OpsCleanupConfig{})
		require.Equal(t, 365, policy.billingDedupRetentionDays)
	}
}

func TestDataLifecycleRoleKnown(t *testing.T) {
	require.True(t, dataLifecycleRoleKnown("https://api.tokenkey.dev"))
	require.True(t, dataLifecycleRoleKnown("https://api-jp1.tokenkey.dev"))
	require.False(t, dataLifecycleRoleKnown(""))
	require.False(t, dataLifecycleRoleKnown("https://custom.example.com"))
	require.False(t, dataLifecycleRoleKnown("https://api-jp1.example.com"))
}

func TestResolveDataLifecyclePolicyTreatsCustomAPISubdomainAsProdSafe(t *testing.T) {
	policy := resolveDataLifecyclePolicy(&config.Config{
		Server: config.ServerConfig{FrontendURL: "https://api-us1.example.com"},
		DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{UsageLogsDays: 90},
		},
	}, config.OpsCleanupConfig{
		SystemLogRetentionDays: 7,
		ErrorLogRetentionDays:  30,
	})

	require.Equal(t, "prod", policy.role)
	require.Equal(t, 90, policy.usageLogRetentionDays)
	require.Equal(t, 30, policy.errorLogRetentionDays)
	require.Equal(t, 7, policy.systemLogRetentionDays)
}
