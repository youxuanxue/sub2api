import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/groups', () => ({
  getModelsListCandidates: vi.fn()
}))
vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  getModelMappingPresets: vi.fn(),
}))

import {
  buildModelMappingObject,
  getModelsByPlatform,
  getPresetMappingsByPlatform,
  qwen3DenseModels,
  splitModelMappingObject
} from '../useModelWhitelist'

describe('useModelWhitelist', () => {
  // R-003: membership of the API-backed platforms (anthropic/openai/gemini/
  // antigravity) is no longer pinned here — it is the self-healing backend's
  // truth, asserted in Go (TestTkServableCandidateIDs). getModelsByPlatform
  // returns the reactive servable cache for those, which is empty until a fetch
  // resolves; the custom input is the escape hatch. Only newapi + the long-tail
  // direct providers keep static frontend lists.
  it('API-backed platforms read the (empty-until-loaded) servable cache', () => {
    // No fetch triggered in this unit context → empty, not a stale hardcoded list.
    expect(getModelsByPlatform('openai')).toEqual([])
    expect(getModelsByPlatform('claude')).toEqual([])
    expect(getModelsByPlatform('anthropic')).toEqual([])
    expect(getModelsByPlatform('gemini')).toEqual([])
    expect(getModelsByPlatform('antigravity')).toEqual([])
    expect(getModelsByPlatform('grok')).toEqual([])
    expect(getModelsByPlatform('kiro')).toEqual([])
  })

  it('newapi keeps its own static list (channel-driven, no backend allowlist)', () => {
    const newapiModels = getModelsByPlatform('newapi')
    expect(newapiModels).toContain('gpt-5.4')
    expect(newapiModels).toContain('gpt-5.3-codex-spark')
    expect(newapiModels).not.toContain('gpt-5.3-codex')
    expect(newapiModels).not.toContain('codex-auto-review')
  })

  it('newapi picker offers the qwen3 dense ids (PR-B: dropdown discoverability)', () => {
    // The newapi modal hardcodes platform='newapi', so the dropdown reads this
    // list — before PR-B it was GPT-only and these three dense ids could only
    // be added via the custom-model input (the drop itself was phantom).
    // INTERIM truth lives in tk_served_models.json (backend manifest); PR-C
    // will derive this list from a servable endpoint.
    const newapiModels = getModelsByPlatform('newapi')
    for (const model of qwen3DenseModels) {
      expect(newapiModels).toContain(model)
    }
  })

  it('long-tail direct providers keep static lists', () => {
    expect(getModelsByPlatform('deepseek').length).toBeGreaterThan(0)
    const qwen = getModelsByPlatform('qwen')
    expect(qwen.length).toBeGreaterThan(0)
    // dense ids are also offered on the direct qwen platform
    for (const model of qwen3DenseModels) {
      expect(qwen).toContain(model)
    }
  })

  it('unknown platform yields an empty list (custom input is the escape hatch)', () => {
    expect(getModelsByPlatform('totally-unknown')).toEqual([])
  })

  it('antigravity 预设映射包含 Gemini 3.1 Pro 通用别名', () => {
    const mappings = getPresetMappingsByPlatform('antigravity')

    expect(mappings).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ from: 'gemini-3.1-pro', to: 'gemini-pro-agent' }),
      ])
    )
  })

  it('whitelist 模式会忽略通配符条目', () => {
    const mapping = buildModelMappingObject('whitelist', ['claude-*', 'gemini-3.1-flash-image'], [])
    expect(mapping).toEqual({
      'gemini-3.1-flash-image': 'gemini-3.1-flash-image'
    })
  })

  it('whitelist 模式会保留 GPT-5.4 官方快照的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-2026-03-05'], [])

    expect(mapping).toEqual({
      'gpt-5.4-2026-03-05': 'gpt-5.4-2026-03-05'
    })
  })

  it('whitelist keeps GPT-5.4 mini exact mappings', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-mini'], [])

    expect(mapping).toEqual({
      'gpt-5.4-mini': 'gpt-5.4-mini'
    })
  })

  it('newapi 预设映射独立于 openai（不共享对象）', () => {
    const openaiMappings = getPresetMappingsByPlatform('openai')
    const newapiMappings = getPresetMappingsByPlatform('newapi')

    expect(newapiMappings).not.toBe(openaiMappings)
    expect(newapiMappings.some(item => item.from === 'gpt-5.4' && item.to === 'gpt-5.4')).toBe(true)
  })

  it('antigravity presets only target live Claude upstream ids', () => {
    const mappings = getPresetMappingsByPlatform('antigravity')
    const claudeMappings = mappings.filter(item => item.from.startsWith('claude-'))

    expect(claudeMappings).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ from: 'claude-sonnet-4-6', to: 'claude-sonnet-4-6' }),
        expect.objectContaining({ from: 'claude-opus-4-6', to: 'claude-opus-4-6-thinking' }),
        expect.objectContaining({ from: 'claude-opus-4-6-thinking', to: 'claude-opus-4-6-thinking' }),
      ])
    )
    for (const item of claudeMappings) {
      expect(['claude-sonnet-4-6', 'claude-opus-4-6-thinking']).toContain(item.to)
    }
    expect(claudeMappings.some(item => item.from === 'claude-fable-5')).toBe(false)
    expect(claudeMappings.some(item => item.from === 'claude-opus-4-8')).toBe(false)
    expect(claudeMappings.some(item => item.to === 'claude-sonnet-4-5')).toBe(false)
  })

  it('combined 模式会同时保留白名单身份映射和模型映射', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['gpt-5.4', 'claude-*'],
      [
        { from: 'gpt-latest', to: 'gpt-5.4' },
        { from: 'gpt-5.4', to: 'gpt-5.4-mini' }
      ]
    )

    expect(mapping).toEqual({
      'gpt-5.4': 'gpt-5.4-mini',
      'gpt-latest': 'gpt-5.4'
    })
  })

  it('splitModelMappingObject 会把身份映射还原成白名单，其余保留为映射', () => {
    const parsed = splitModelMappingObject({
      'gpt-5.4': 'gpt-5.4',
      'gpt-latest': 'gpt-5.4',
      ' ': 'gpt-empty',
      broken: 123
    })

    expect(parsed).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }]
    })
  })
})
