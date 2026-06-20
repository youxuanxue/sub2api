import { describe, expect, it } from 'vitest'
import { normalizeCodexAdditionalLimits } from '../codexRateLimitWindows.tk'
import type { OpenAIQuotaUsage } from '@/api/admin/accounts'

describe('normalizeCodexAdditionalLimits', () => {
  it('returns [] for missing / empty input', () => {
    expect(normalizeCodexAdditionalLimits(null)).toEqual([])
    expect(normalizeCodexAdditionalLimits(undefined)).toEqual([])
    expect(normalizeCodexAdditionalLimits({ fetched_at: 0 } as OpenAIQuotaUsage)).toEqual([])
    expect(
      normalizeCodexAdditionalLimits({ fetched_at: 0, additional_rate_limits: [] })
    ).toEqual([])
  })

  it('surfaces the GPT-5.3-Codex-Spark per-model window (5h exhausted, 7d partial)', () => {
    // Mirrors the operator report: spark 5h is 100% used (0% remaining) while the
    // weekly window is 46% used (54% remaining).
    const resetAt5h = 1_900_000_000
    const resetAt7d = 1_900_500_000
    const usage: OpenAIQuotaUsage = {
      fetched_at: 0,
      additional_rate_limits: [
        {
          limit_name: 'GPT-5.3-Codex-Spark',
          metered_feature: 'gpt-5.3-codex-spark',
          rate_limit: {
            allowed: false,
            limit_reached: true,
            primary_window: {
              used_percent: 100,
              limit_window_seconds: 5 * 60 * 60,
              reset_after_seconds: 0,
              reset_at: resetAt5h
            },
            secondary_window: {
              used_percent: 46,
              limit_window_seconds: 7 * 24 * 60 * 60,
              reset_after_seconds: 0,
              reset_at: resetAt7d
            }
          }
        }
      ]
    }

    const [spark] = normalizeCodexAdditionalLimits(usage)
    expect(spark.name).toBe('GPT-5.3-Codex-Spark')
    expect(spark.meteredFeature).toBe('gpt-5.3-codex-spark')
    expect(spark.limitReached).toBe(true)
    expect(spark.fiveHour?.usedPercent).toBe(100)
    expect(spark.weekly?.usedPercent).toBe(46)
    // reset_at is unix seconds → ISO from ms.
    expect(spark.fiveHour?.resetsAtIso).toBe(new Date(resetAt5h * 1000).toISOString())
    expect(spark.weekly?.resetsAtIso).toBe(new Date(resetAt7d * 1000).toISOString())
  })

  it('classifies windows by duration regardless of primary/secondary ordering', () => {
    // primary is the 7d window, secondary is the 5h window — classification must
    // follow limit_window_seconds, not field name.
    const usage: OpenAIQuotaUsage = {
      fetched_at: 0,
      additional_rate_limits: [
        {
          limit_name: 'Swapped',
          metered_feature: 'swapped',
          rate_limit: {
            allowed: true,
            limit_reached: false,
            primary_window: {
              used_percent: 12,
              limit_window_seconds: 7 * 24 * 60 * 60,
              reset_after_seconds: 0,
              reset_at: 0
            },
            secondary_window: {
              used_percent: 88,
              limit_window_seconds: 5 * 60 * 60,
              reset_after_seconds: 0,
              reset_at: 0
            }
          }
        }
      ]
    }

    const [limit] = normalizeCodexAdditionalLimits(usage)
    expect(limit.fiveHour?.usedPercent).toBe(88)
    expect(limit.weekly?.usedPercent).toBe(12)
  })

  it('falls back to primary=5h / secondary=7d when durations are missing', () => {
    const usage: OpenAIQuotaUsage = {
      fetched_at: 0,
      additional_rate_limits: [
        {
          limit_name: 'NoDuration',
          metered_feature: 'no-duration',
          rate_limit: {
            allowed: true,
            limit_reached: false,
            primary_window: {
              used_percent: 30,
              limit_window_seconds: 0,
              reset_after_seconds: 600,
              reset_at: 0
            },
            secondary_window: {
              used_percent: 5,
              limit_window_seconds: 0,
              reset_after_seconds: 0,
              reset_at: 0
            }
          }
        }
      ]
    }

    const [limit] = normalizeCodexAdditionalLimits(usage)
    expect(limit.fiveHour?.usedPercent).toBe(30)
    expect(limit.weekly?.usedPercent).toBe(5)
    // reset_after_seconds path produces a relative ISO reset (non-null).
    expect(limit.fiveHour?.resetsAtIso).not.toBeNull()
    expect(limit.weekly?.resetsAtIso).toBeNull()
  })

  it('drops limits that carry no usable window data and uses metered_feature as a name fallback', () => {
    const usage: OpenAIQuotaUsage = {
      fetched_at: 0,
      additional_rate_limits: [
        { limit_name: '', metered_feature: '', rate_limit: null },
        {
          limit_name: '',
          metered_feature: 'fallback-name',
          rate_limit: {
            allowed: true,
            limit_reached: false,
            primary_window: {
              used_percent: 10,
              limit_window_seconds: 5 * 60 * 60,
              reset_after_seconds: 0,
              reset_at: 0
            },
            secondary_window: null
          }
        }
      ]
    }

    const result = normalizeCodexAdditionalLimits(usage)
    expect(result).toHaveLength(1)
    expect(result[0].name).toBe('fallback-name')
    expect(result[0].fiveHour?.usedPercent).toBe(10)
    expect(result[0].weekly).toBeNull()
  })
})
