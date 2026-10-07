import { describe, expect, it } from 'vitest'
import { CONCRETE_PLATFORM_OPTIONS, GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import {
  PLATFORM_NEWAPI,
  PLATFORM_KIRO,
  PLATFORM_OPENAI,
  supportsUpstreamBillingProbe,
  UPSTREAM_BILLING_PROBE_PLATFORMS
} from '@/constants/gatewayPlatforms'

const concretePlatforms = [
  'anthropic',
  'openai',
  'gemini',
  'antigravity',
  'newapi',
  'kiro',
  'grok',
  'kimi',
  'zhipu',
  'deepseek',
  'minimax',
  'opencode_go',
  'typesafe'
]

describe('platform option catalogs', () => {
  it('exposes every concrete account platform', () => {
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(concretePlatforms)
  })

  it('adds composite for group-backed filters', () => {
    expect(GROUP_PLATFORM_OPTIONS.map((option) => option.value)).toEqual([
      ...concretePlatforms,
      'composite'
    ])
  })

  it('excludes newapi/kiro relays from upstream billing probe platforms', () => {
    expect(supportsUpstreamBillingProbe(PLATFORM_OPENAI)).toBe(true)
    expect(supportsUpstreamBillingProbe(PLATFORM_NEWAPI)).toBe(false)
    expect(supportsUpstreamBillingProbe(PLATFORM_KIRO)).toBe(false)
    expect(UPSTREAM_BILLING_PROBE_PLATFORMS).not.toContain(PLATFORM_NEWAPI)
    expect(UPSTREAM_BILLING_PROBE_PLATFORMS).not.toContain(PLATFORM_KIRO)
  })
})
