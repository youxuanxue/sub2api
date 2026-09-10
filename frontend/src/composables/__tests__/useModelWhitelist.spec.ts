import { describe, expect, it, vi } from 'vitest'
import { getModelMappingPresets } from '@/api/admin/accounts'
import { useServableModels } from '../useServableModels'
import { buildModelMappingObject, getModelsByPlatform, splitModelMappingObject } from '../useModelWhitelist'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  getModelMappingPresets: vi.fn(),
}))

describe('useModelWhitelist', () => {
  it.each(['claude', 'openai', 'antigravity', 'gemini', 'grok', 'kiro'])(
    '%s suggestions follow the backend catalog, including its order',
    async platform => {
      expect(getModelsByPlatform(platform)).toEqual([])
      const models = [platform + '-new-snapshot', platform + '-stable']
      vi.mocked(getModelMappingPresets).mockResolvedValueOnce(models)
      await useServableModels().ensureLoaded(platform)
      expect(getModelsByPlatform(platform)).toEqual(models)
    },
  )

  it('keeps provider aliases on the same catalog', () => {
    expect(getModelsByPlatform('anthropic')).toEqual(getModelsByPlatform('claude'))
    expect(getModelsByPlatform('xai')).toEqual(getModelsByPlatform('grok'))
    expect(getModelsByPlatform('unknown-provider')).toEqual([])
  })

  it('whitelist preserves exact snapshots and identity wildcard mappings', () => {
    expect(buildModelMappingObject('whitelist', ['claude-*', 'gpt-5.4-2026-03-05'], [])).toEqual({
      'claude-*': 'claude-*',
      'gpt-5.4-2026-03-05': 'gpt-5.4-2026-03-05',
    })
  })

  it('combined mode keeps identity mappings and lets explicit aliases override them', () => {
    expect(buildModelMappingObject('combined', ['gpt-5.4', 'claude-*'], [
      { from: 'gpt-latest', to: 'gpt-5.4' },
      { from: 'gpt-5.4', to: 'gpt-5.4-mini' },
    ])).toEqual({
      'claude-*': 'claude-*',
      'gpt-5.4': 'gpt-5.4-mini',
      'gpt-latest': 'gpt-5.4',
    })
  })

  it('splits identity mappings into the whitelist and ignores malformed entries', () => {
    expect(splitModelMappingObject({
      'gpt-5.4': 'gpt-5.4',
      'claude-*': 'claude-*',
      'gpt-latest': 'gpt-5.4',
      ' ': 'gpt-empty',
      broken: 123,
    })).toEqual({
      allowedModels: ['gpt-5.4', 'claude-*'],
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }],
    })
  })
})
