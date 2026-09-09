import { describe, expect, it, vi } from 'vitest'
import { getModelMappingPresets } from '@/api/admin/accounts'
import { useServableModels } from '../useServableModels'
import { buildModelMappingObject, getModelsByPlatform, getPresetMappingsByPlatform, splitModelMappingObject } from '../useModelWhitelist'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  getModelMappingPresets: vi.fn(),
}))

describe('useModelWhitelist', () => {
  it.each(['openai', 'anthropic', 'antigravity', 'gemini', 'grok', 'kiro'])('uses the backend %s catalog and preserves its ordering', async (platform) => {
    expect(getModelsByPlatform(platform)).toEqual([])
    const catalog = [platform + '-current', platform + '-current-image', platform + '-alias']
    vi.mocked(getModelMappingPresets).mockResolvedValueOnce(catalog)
    await useServableModels().ensureLoaded(platform)
    expect(getModelMappingPresets).toHaveBeenLastCalledWith(platform)
    expect(getModelsByPlatform(platform)).toEqual(catalog)
    if (platform === 'anthropic') expect(getModelsByPlatform('claude')).toEqual(catalog)
    if (platform === 'grok') expect(getModelsByPlatform('xai')).toEqual(catalog)
  })

  it('keeps GPT-6 and Astra mapping presets', () => {
    expect(getPresetMappingsByPlatform('openai')).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 'gpt-6', to: 'gpt-6' }),
      expect.objectContaining({ from: 'gpt-6-astra', to: 'gpt-6-astra' }),
    ]))
  })

  it('keeps supported identity wildcards and rejects malformed patterns', () => {
    expect(buildModelMappingObject('whitelist', ['claude-*', 'cla*ude', 'gemini-3.1-flash-image'], [])).toEqual({
      'claude-*': 'claude-*',
      'gemini-3.1-flash-image': 'gemini-3.1-flash-image',
    })
  })

  it.each(['gpt-5.4-2026-03-05', 'gpt-5.4-mini'])('preserves exact whitelist mapping for %s', (model) => {
    expect(buildModelMappingObject('whitelist', [model], [])).toEqual({ [model]: model })
  })

  it('keeps Grok aliases in combined mode', () => {
    const aliases = ['grok-latest', 'grok-4.5-latest', 'grok-build-latest']
    expect(buildModelMappingObject('combined', ['grok-4.5'], aliases.map(from => ({ from, to: 'grok-4.5' })))).toEqual({
      'grok-4.5': 'grok-4.5',
      'grok-latest': 'grok-4.5',
      'grok-4.5-latest': 'grok-4.5',
      'grok-build-latest': 'grok-4.5',
    })
  })

  it('explicit rewrites take precedence over identity mappings without erasing wildcards', () => {
    expect(buildModelMappingObject('combined', ['gpt-5.4', 'claude-*'], [
      { from: 'gpt-latest', to: 'gpt-5.4' },
      { from: 'gpt-5.4', to: 'gpt-5.4-mini' },
    ])).toEqual({ 'claude-*': 'claude-*', 'gpt-5.4': 'gpt-5.4-mini', 'gpt-latest': 'gpt-5.4' })
  })

  it('restores identities as whitelist entries and keeps rewrites separately', () => {
    expect(splitModelMappingObject({ 'gpt-5.4': 'gpt-5.4', 'gpt-latest': 'gpt-5.4', ' ': 'gpt-empty', broken: 123 })).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }],
    })
  })
})
