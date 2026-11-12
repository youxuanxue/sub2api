//go:build unit

package lifecycle

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSharedCleanupRequiresEveryHistoricalSourceAndRetention(t *testing.T) {
	hour := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	completion := SourceCompletion{State: "complete", ManifestSHA256: strings.Repeat("a", 64), SealDigest: strings.Repeat("b", 64)}
	newPlan := func() SharedCleanupPlan {
		return SharedCleanupPlan{SchemaVersion: "qa-data-machine-cleanup-v1", Environment: "prod",
			WindowStart: hour, WindowEnd: hour.Add(time.Hour), ExpectedSources: []string{"a", "b"},
			Sources: map[string]SourceCompletion{"a": completion, "b": completion}, Action: "delete"}
	}
	anchor := hour.Add(26 * time.Hour)
	require.NoError(t, newPlan().Validate("prod", []string{"b", "a"}, anchor))
	for name, mutate := range map[string]func(*SharedCleanupPlan){
		"missing source":     func(p *SharedCleanupPlan) { delete(p.Sources, "a") },
		"wrong site":         func(p *SharedCleanupPlan) { p.Environment = "cn-prod" },
		"gap not authorized": func(p *SharedCleanupPlan) { p.Action = "gap-delete" },
		"wrong upper":        func(p *SharedCleanupPlan) { p.WindowEnd = hour.Add(2 * time.Hour) },
		"unknown source":     func(p *SharedCleanupPlan) { p.ExpectedSources = []string{"a", "c"} },
		"duplicate source":   func(p *SharedCleanupPlan) { p.ExpectedSources = []string{"a", "a"} },
		"snapshot without seal": func(p *SharedCleanupPlan) {
			p.Sources["a"] = SourceCompletion{State: "complete", ManifestSHA256: completion.ManifestSHA256}
		},
		"unsealed source": func(p *SharedCleanupPlan) { p.Sources["a"] = SourceCompletion{State: "missing"} },
	} {
		t.Run(name, func(t *testing.T) {
			plan := newPlan()
			mutate(&plan)
			require.Error(t, plan.Validate("prod", []string{"a", "b"}, anchor))
		})
	}
	require.Error(t, newPlan().Validate("prod", []string{"a", "b"}, hour.Add(24*time.Hour)))
	require.Error(t, newPlan().Validate("prod", []string{"a"}, anchor))
	require.Error(t, newPlan().Validate("prod", nil, anchor))
}
