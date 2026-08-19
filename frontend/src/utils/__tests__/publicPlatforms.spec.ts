import { describe, expect, it } from 'vitest'
import {
  expandPublicPlatforms,
  getPublicPlatformStyleKey,
  getPublicPlatformLabel,
  normalizePublicMonitorMatrixRows,
  normalizePublicMonitorViews,
  normalizePublicPlatformQuotas,
  normalizePlatformDashboardStats,
  normalizePlatformUsage,
} from '../publicPlatforms'
import { platformAccentColor, platformBadgeClass, platformLabel } from '../platformColors'

describe('public platform taxonomy', () => {
  it('merges internal Google quota windows into one public Google quota', () => {
    expect(normalizePublicPlatformQuotas([
      {
        platform: 'gemini',
        daily_limit_usd: 10,
        daily_usage_usd: 3,
        daily_window_resets_at: '2026-08-20T00:00:00Z',
        weekly_limit_usd: null,
        weekly_usage_usd: 4,
        weekly_window_resets_at: null,
        monthly_limit_usd: 30,
        monthly_usage_usd: 5,
        monthly_window_resets_at: '2026-09-01T00:00:00Z',
      },
      {
        platform: 'antigravity',
        daily_limit_usd: 20,
        daily_usage_usd: 4,
        daily_window_resets_at: '2026-08-20T00:00:00Z',
        weekly_limit_usd: 40,
        weekly_usage_usd: 6,
        weekly_window_resets_at: '2026-08-25T00:00:00Z',
        monthly_limit_usd: 50,
        monthly_usage_usd: 7,
        monthly_window_resets_at: '2026-09-01T00:00:00Z',
      },
    ])).toEqual([{
      platform: 'google',
      daily_limit_usd: 30,
      daily_usage_usd: 7,
      daily_window_resets_at: '2026-08-20T00:00:00Z',
      weekly_limit_usd: null,
      weekly_usage_usd: 10,
      weekly_window_resets_at: null,
      monthly_limit_usd: 80,
      monthly_usage_usd: 12,
      monthly_window_resets_at: '2026-09-01T00:00:00Z',
    }])
  })

  it('merges public monitor rows and same-time buckets by Google dimension', () => {
    const metric = (requestCount: number, errorRequests = 0) => ({
      success_requests: requestCount - errorRequests,
      error_requests: errorRequests,
      request_count: requestCount,
      token_count: requestCount * 10,
      rpm: requestCount,
      tpm: requestCount * 10,
      error_rate: requestCount ? errorRequests / requestCount : 0,
      cache_rate: 0.5,
      cache_rate_numerator: requestCount,
      cache_rate_denominator: requestCount * 2,
      ttft: { sample_count: requestCount, p50_ms: 100, p95_ms: 200, avg_ms: 100 },
      duration: { sample_count: requestCount, p50_ms: 300, p95_ms: 400, avg_ms: 300 },
    })
    const health = { overall: 'healthy' as const, error_rate: 'healthy' as const, ttft: 'healthy' as const, minimum_sample: 1 }
    const rows = normalizePublicMonitorMatrixRows([
      { platform: 'gemini', group_id: 7, group_name: 'Google', model: 'gemini-2.5-pro', metrics: metric(2), health, buckets: [{ bucket_start: '2026-08-19T00:00:00Z', metrics: metric(2), health }] },
      { platform: 'antigravity', group_id: 7, group_name: 'Google', model: 'gemini-2.5-pro', metrics: metric(3, 1), health, buckets: [{ bucket_start: '2026-08-19T00:00:00Z', metrics: metric(3, 1), health }] },
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].platform).toBe('google')
    expect(rows[0].metrics.request_count).toBe(5)
    expect(rows[0].metrics.error_requests).toBe(1)
    expect(rows[0].buckets).toHaveLength(1)
    expect(rows[0].buckets[0].metrics.request_count).toBe(5)
  })

  it('merges legacy public monitor cards by Google dimension', () => {
    const base = {
      group_name: 'default',
      primary_model: 'gemini-2.5-pro',
      primary_status: 'operational' as const,
      primary_latency_ms: 10,
      primary_ping_latency_ms: 12,
      availability_7d: 99,
      extra_models: [],
      timeline: [],
    }
    const rows = normalizePublicMonitorViews([
      { id: 1, name: 'Gemini channel', provider: 'gemini', ...base },
      { id: 2, name: 'Antigravity channel', provider: 'antigravity' as never, ...base, availability_7d: 97 },
    ])
    expect(rows).toHaveLength(1)
    expect(rows[0].provider).toBe('gemini')
    expect(rows[0].availability_7d).toBe(97)
  })

  it('merges Gemini and Antigravity dashboard metrics into Google', () => {
    expect(normalizePlatformDashboardStats([
      {
        platform: 'gemini',
        total_requests: 2,
        total_tokens: 20,
        total_actual_cost: 2,
        today_requests: 1,
        today_tokens: 10,
        today_actual_cost: 1,
      },
      {
        platform: 'antigravity',
        total_requests: 3,
        total_tokens: 30,
        total_actual_cost: 3,
        today_requests: 2,
        today_tokens: 20,
        today_actual_cost: 2,
      },
    ])).toEqual([{
      platform: 'google',
      total_requests: 5,
      total_tokens: 50,
      total_actual_cost: 5,
      today_requests: 3,
      today_tokens: 30,
      today_actual_cost: 3,
    }])
  })

  it('uses the same Google taxonomy for compact admin usage rows', () => {
    expect(normalizePlatformUsage([
      { platform: 'gemini', today_actual_cost: 1, total_actual_cost: 2 },
      { platform: 'antigravity', today_actual_cost: 3, total_actual_cost: 4 },
    ])).toEqual([
      { platform: 'google', today_actual_cost: 4, total_actual_cost: 6 },
    ])
    expect(getPublicPlatformLabel('gemini')).toBe('Google')
    expect(getPublicPlatformLabel('antigravity')).toBe('Google')
    expect(getPublicPlatformLabel(' Gemini ')).toBe('Google')
    expect(getPublicPlatformLabel('ANTIGRAVITY')).toBe('Google')
    expect(getPublicPlatformStyleKey('gemini')).toBe('gemini')
    expect(getPublicPlatformStyleKey('antigravity')).toBe('gemini')
  })

  it('keeps generic admin platform presentation diagnostic', () => {
    expect(platformLabel('gemini')).toBe('Gemini')
    expect(platformLabel('antigravity')).toBe('Antigravity')
    expect(platformAccentColor('gemini')).not.toBe(platformAccentColor('antigravity'))
    expect(platformBadgeClass('gemini')).not.toBe(platformBadgeClass('antigravity'))
  })

  it('expands the public Google filter back to every internal Google source', () => {
    expect(expandPublicPlatforms(['openai', 'google', 'GOOGLE', 'gemini'])).toEqual([
      'openai',
      'gemini',
      'antigravity',
      'google',
    ])
  })
})
