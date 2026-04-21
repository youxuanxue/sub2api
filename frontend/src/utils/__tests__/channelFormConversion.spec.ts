import { describe, expect, it } from 'vitest'

import type { Channel } from '@/api/admin/channels'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AdminGroup, GroupPlatform } from '@/types'

import {
  apiToFormSections,
  formSectionsToApi,
  type PlatformSection,
} from '../channelFormConversion'

// ── Fixtures ─────────────────────────────────────────────────────────

function makeGroup(id: number, platform: GroupPlatform, name: string): AdminGroup {
  return {
    id,
    name,
    description: null,
    platform,
    rate_multiplier: 1,
    is_exclusive: false,
    status: 'active',
    subscription_type: 'standard',
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    image_price_1k: null,
    image_price_2k: null,
    image_price_4k: null,
    claude_code_only: false,
    fallback_group_id: null,
    fallback_group_id_on_invalid_request: null,
    require_oauth_only: false,
    require_privacy_set: false,
    created_at: '2026-04-20T00:00:00Z',
    updated_at: '2026-04-20T00:00:00Z',
    model_routing: null,
    model_routing_enabled: false,
    mcp_xml_inject: false,
    sort_order: 0,
  }
}

const ALL_GROUPS: AdminGroup[] = [
  makeGroup(1, 'anthropic', 'anthropic-pool'),
  makeGroup(2, 'openai', 'openai-pool'),
  makeGroup(3, 'gemini', 'gemini-pool'),
  makeGroup(4, 'antigravity', 'antigravity-pool'),
  makeGroup(5, 'newapi', 'newapi-deepseek'),
  makeGroup(6, 'newapi', 'newapi-zhipu'),
]

function makeChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: 1,
    name: 'test-channel',
    description: '',
    status: 'active',
    billing_model_source: 'channel_mapped',
    restrict_models: false,
    features_config: {},
    group_ids: [],
    model_pricing: [],
    model_mapping: {},
    apply_pricing_to_account_stats: false,
    account_stats_pricing_rules: [],
    created_at: '2026-04-20T00:00:00Z',
    updated_at: '2026-04-20T00:00:00Z',
    ...overrides,
  }
}

// Backend stores per-token; UI displays per-MTok. 0.000003 per-token = 3 per-MTok.
const PER_TOKEN_3_USD_PER_MTOK = 0.000003

// ── Tests ────────────────────────────────────────────────────────────

describe('channelFormConversion (US-017 — round-trip preserves all 5 gateway platforms)', () => {
  it('round-trips a channel that mixes anthropic + newapi without dropping newapi data (the data-loss bug we are fixing)', () => {
    // Reproduces the latent bug discovered during deep review of ChannelsView:
    // the previous in-component apiToForm filtered model_mapping keys by a
    // 4-element platformOrder array (line 1070), and formToAPI then iterated
    // form.platforms (line 1007). Saving an existing channel that the API
    // had returned with newapi data would therefore SILENTLY DELETE all
    // newapi rows. This test pins the contract that newapi survives the
    // round-trip when GATEWAY_PLATFORMS is the canonical order.
    const channel = makeChannel({
      group_ids: [1, 5, 6],
      model_mapping: {
        anthropic: { 'claude-3.5-sonnet': 'claude-3-5-sonnet-20241022' },
        newapi: { 'deepseek-chat': 'deepseek-v3', 'glm-4': 'glm-4-plus' },
      },
      model_pricing: [
        {
          platform: 'anthropic',
          models: ['claude-3.5-sonnet'],
          billing_mode: 'token',
          input_price: PER_TOKEN_3_USD_PER_MTOK,
          output_price: 0.000015,
          cache_write_price: null,
          cache_read_price: null,
          image_output_price: null,
          per_request_price: null,
          intervals: [],
        },
        {
          platform: 'newapi',
          models: ['deepseek-chat'],
          billing_mode: 'token',
          input_price: 0.0000001,
          output_price: 0.00000028,
          cache_write_price: null,
          cache_read_price: null,
          image_output_price: null,
          per_request_price: null,
          intervals: [],
        },
      ],
    })

    const sections = apiToFormSections(channel, ALL_GROUPS)

    const platforms = sections.map((s) => s.platform)
    expect(platforms).toContain('newapi')
    expect(platforms).toContain('anthropic')

    const newapiSection = sections.find((s) => s.platform === 'newapi')!
    expect(newapiSection.group_ids.sort()).toEqual([5, 6])
    expect(newapiSection.model_mapping).toEqual({
      'deepseek-chat': 'deepseek-v3',
      'glm-4': 'glm-4-plus',
    })
    expect(newapiSection.model_pricing).toHaveLength(1)
    expect(newapiSection.model_pricing[0].models).toEqual(['deepseek-chat'])

    // Round-trip: form → api should preserve everything.
    const payload = formSectionsToApi(sections, channel.features_config)

    expect(payload.group_ids.sort()).toEqual([1, 5, 6])
    expect(payload.model_mapping).toHaveProperty('newapi')
    expect(payload.model_mapping.newapi).toEqual({
      'deepseek-chat': 'deepseek-v3',
      'glm-4': 'glm-4-plus',
    })
    expect(payload.model_mapping).toHaveProperty('anthropic')
    expect(payload.model_pricing.map((p) => p.platform).sort()).toEqual([
      'anthropic',
      'newapi',
    ])

    // Per-token ↔ per-MTok conversion must round-trip the anthropic pricing.
    const antPricing = payload.model_pricing.find((p) => p.platform === 'anthropic')!
    expect(antPricing.input_price).toBeCloseTo(PER_TOKEN_3_USD_PER_MTOK, 12)
    expect(antPricing.output_price).toBeCloseTo(0.000015, 12)
  })

  it('NEGATIVE — apiToFormSections with the legacy 4-element platformOrder still drops newapi (regression-by-construction proof of the bug)', () => {
    // Demonstrates that the FIX (passing GATEWAY_PLATFORMS) is doing the work,
    // not some accidental property of the test fixtures. With the historical
    // 4-element list, newapi data is silently filtered out — exactly the bug
    // ChannelsView shipped before this PR.
    const legacyPlatformOrder: GroupPlatform[] = [
      'anthropic',
      'openai',
      'gemini',
      'antigravity',
    ]
    const channel = makeChannel({
      group_ids: [5],
      model_mapping: { newapi: { foo: 'bar' } },
      model_pricing: [
        {
          platform: 'newapi',
          models: ['foo'],
          billing_mode: 'token',
          input_price: 0.0000001,
          output_price: 0.0000002,
          cache_write_price: null,
          cache_read_price: null,
          image_output_price: null,
          per_request_price: null,
          intervals: [],
        },
      ],
    })

    const sections = apiToFormSections(channel, ALL_GROUPS, legacyPlatformOrder)

    expect(sections.map((s) => s.platform)).not.toContain('newapi')
    expect(sections).toHaveLength(0)
  })

  it('REGRESSION — round-trips a channel with only the 4 legacy platforms (no behavior change for existing channels)', () => {
    const channel = makeChannel({
      group_ids: [1, 2, 3, 4],
      model_mapping: {
        anthropic: { 'claude-3.5-sonnet': 'claude-3-5-sonnet' },
        openai: { 'gpt-4o': 'gpt-4o-2024-08-06' },
      },
      model_pricing: [
        {
          platform: 'anthropic',
          models: ['claude-3.5-sonnet'],
          billing_mode: 'token',
          input_price: 0.000003,
          output_price: 0.000015,
          cache_write_price: 0.00000375,
          cache_read_price: 0.0000003,
          image_output_price: null,
          per_request_price: null,
          intervals: [],
        },
      ],
    })

    const sections = apiToFormSections(channel, ALL_GROUPS)
    const payload = formSectionsToApi(sections, channel.features_config)

    expect(payload.group_ids.sort((a, b) => a - b)).toEqual([1, 2, 3, 4])
    expect(Object.keys(payload.model_mapping).sort()).toEqual([
      'anthropic',
      'openai',
    ])
    expect(payload.model_pricing).toHaveLength(1)
    expect(payload.model_pricing[0].platform).toBe('anthropic')
  })

  it('preserves features_config.web_search_emulation flag through the round-trip when anthropic section is enabled', () => {
    const channel = makeChannel({
      group_ids: [1],
      features_config: {
        web_search_emulation: { anthropic: true },
        custom_unrelated_field: 'must-survive',
      },
      model_mapping: { anthropic: { foo: 'bar' } },
    })

    const sections = apiToFormSections(channel, ALL_GROUPS)
    const ant = sections.find((s) => s.platform === 'anthropic')!
    expect(ant.web_search_emulation).toBe(true)

    const payload = formSectionsToApi(sections, channel.features_config)
    expect(payload.features_config.web_search_emulation).toEqual({ anthropic: true })
    // Non-managed keys must be preserved.
    expect(payload.features_config.custom_unrelated_field).toBe('must-survive')
  })

  it('clears web_search_emulation when the anthropic section is disabled (toggle on→off must persist)', () => {
    // Bug guard: the previous implementation always rewrote the key, so a
    // user toggling off in the UI MUST flip the persisted value, not leave a
    // stale `true`. Same contract here.
    const channel = makeChannel({
      group_ids: [1],
      features_config: { web_search_emulation: { anthropic: true } },
    })

    const sections = apiToFormSections(channel, ALL_GROUPS)
    const ant = sections.find((s) => s.platform === 'anthropic')!
    ant.web_search_emulation = false

    const payload = formSectionsToApi(sections, channel.features_config)
    expect(payload.features_config.web_search_emulation).toEqual({ anthropic: false })
  })

  it('drops features_config.web_search_emulation entirely when no anthropic section is enabled', () => {
    const channel = makeChannel({
      group_ids: [5],
      features_config: { web_search_emulation: { anthropic: true } },
    })

    const sections = apiToFormSections(channel, ALL_GROUPS)
    expect(sections.find((s) => s.platform === 'anthropic')).toBeUndefined()

    const payload = formSectionsToApi(sections, channel.features_config)
    expect(payload.features_config).not.toHaveProperty('web_search_emulation')
  })

  it('skips disabled sections when emitting payload (UI can stage edits then deselect a platform)', () => {
    const sections: PlatformSection[] = [
      {
        platform: 'newapi',
        enabled: false,
        collapsed: false,
        group_ids: [5],
        model_mapping: { foo: 'bar' },
        model_pricing: [],
        web_search_emulation: false,
        account_stats_pricing_rules: [],
      },
      {
        platform: 'anthropic',
        enabled: true,
        collapsed: false,
        group_ids: [1],
        model_mapping: { 'claude-3.5-sonnet': 'claude-3-5-sonnet' },
        model_pricing: [],
        web_search_emulation: false,
        account_stats_pricing_rules: [],
      },
    ]

    const payload = formSectionsToApi(sections)
    expect(payload.group_ids).toEqual([1])
    expect(Object.keys(payload.model_mapping)).toEqual(['anthropic'])
  })

  it('returns sections in GATEWAY_PLATFORMS canonical order (anthropic → openai → gemini → antigravity → newapi)', () => {
    const channel = makeChannel({
      group_ids: [5, 1, 3, 2, 4],
      model_mapping: {
        newapi: { a: 'b' },
        anthropic: { c: 'd' },
        openai: { e: 'f' },
        gemini: { g: 'h' },
        antigravity: { i: 'j' },
      },
    })

    const sections = apiToFormSections(channel, ALL_GROUPS)
    expect(sections.map((s) => s.platform)).toEqual([...GATEWAY_PLATFORMS])
  })

  it('drops empty model_pricing entries (models.length === 0) instead of posting them to the backend', () => {
    const sections: PlatformSection[] = [
      {
        platform: 'newapi',
        enabled: true,
        collapsed: false,
        group_ids: [5],
        model_mapping: {},
        model_pricing: [
          {
            models: [],
            billing_mode: 'token',
            input_price: 1,
            output_price: 1,
            cache_write_price: null,
            cache_read_price: null,
            image_output_price: null,
            per_request_price: null,
            intervals: [],
          },
          {
            models: ['deepseek-chat'],
            billing_mode: 'token',
            input_price: 0.5,
            output_price: 1,
            cache_write_price: null,
            cache_read_price: null,
            image_output_price: null,
            per_request_price: null,
            intervals: [],
          },
        ],
        web_search_emulation: false,
        account_stats_pricing_rules: [],
      },
    ]

    const payload = formSectionsToApi(sections)
    expect(payload.model_pricing).toHaveLength(1)
    expect(payload.model_pricing[0].models).toEqual(['deepseek-chat'])
  })
})
