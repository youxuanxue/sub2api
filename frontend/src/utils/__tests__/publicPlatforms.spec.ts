import { describe, expect, it } from 'vitest'
import {
  expandPublicPlatforms,
  getPublicPlatformStyleKey,
  getPublicPlatformLabel,
  normalizePlatformDashboardStats,
  normalizePlatformUsage,
} from '../publicPlatforms'
import { platformAccentColor, platformBadgeClass, platformLabel } from '../platformColors'

describe('public platform taxonomy', () => {
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
