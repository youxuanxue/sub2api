import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  getModelMappingPresets: vi.fn()
}))

import { buildModelMappingObject, getModelsByPlatform, getPresetMappingsByPlatform, splitModelMappingObject } from '../useModelWhitelist'
import { apiBackedPlatforms } from '../useServableModels'

describe('useModelWhitelist', () => {
  it('keeps GPT-6 and Astra mapping presets', () => {
    expect(getPresetMappingsByPlatform('openai')).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 'gpt-6', to: 'gpt-6' }),
      expect.objectContaining({ from: 'gpt-6-astra', to: 'gpt-6-astra' })
    ]))
  })

  beforeEach(() => {
    vi.resetModules()
  })

  it.each(apiBackedPlatforms)('%s uses the backend model list and preserves its order', async platform => {
    const { getModelMappingPresets } = await import('@/api/admin/accounts')
    const { useServableModels } = await import('../useServableModels')
    const { getModelsByPlatform } = await import('../useModelWhitelist')
    const models = ['test-model-z', 'test-model-a']
    vi.mocked(getModelMappingPresets).mockResolvedValue(models)

    expect(getModelsByPlatform(platform)).toEqual([])
    await useServableModels().ensureLoaded(platform)

    expect(getModelMappingPresets).toHaveBeenCalledWith(platform)
    expect(getModelsByPlatform(platform)).toEqual(models)
  })

  it.each([['claude', 'anthropic'], ['xai', 'grok']])('%s reads the %s backend cache', async (alias, platform) => {
    const { getModelMappingPresets } = await import('@/api/admin/accounts')
    const { useServableModels } = await import('../useServableModels')
    const { getModelsByPlatform } = await import('../useModelWhitelist')
    const models = ['test-model']
    vi.mocked(getModelMappingPresets).mockResolvedValue(models)

    await useServableModels().ensureLoaded(alias)

    expect(getModelMappingPresets).toHaveBeenCalledWith(platform)
    expect(getModelsByPlatform(alias)).toEqual(models)
    expect(getModelsByPlatform(alias)).toBe(getModelsByPlatform(platform))
  })

  it('does not restore a static list when the backend catalog fails', async () => {
    const { getModelMappingPresets } = await import('@/api/admin/accounts')
    const { useServableModels } = await import('../useServableModels')
    const { getModelsByPlatform } = await import('../useModelWhitelist')
    vi.mocked(getModelMappingPresets).mockRejectedValue(new Error('catalog unavailable'))

    const { ensureLoaded, error } = useServableModels()
    await ensureLoaded('openai')

    expect(getModelsByPlatform('openai')).toEqual([])
    expect(error.value).toContain('catalog unavailable')
  })

  it('keeps static lists for providers without a backend catalog', () => {
    expect(getModelsByPlatform('qwen')).toContain('qwen3-8b')
  })

  it('combined 模式支持 Grok 4.5 官方别名映射', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['grok-4.5'],
      [
        { from: 'grok-latest', to: 'grok-4.5' },
        { from: 'grok-4.5-latest', to: 'grok-4.5' },
        { from: 'grok-build-latest', to: 'grok-4.5' }
      ]
    )

    expect(mapping).toEqual({
      'grok-4.5': 'grok-4.5',
      'grok-latest': 'grok-4.5',
      'grok-4.5-latest': 'grok-4.5',
      'grok-build-latest': 'grok-4.5',
    })
  })

  it('whitelist 模式保留末尾通配符，拒绝中间通配符', () => {
    const mapping = buildModelMappingObject('whitelist', ['claude-*', 'cla*ude-*', 'gemini-3.1-flash-image'], [])
    expect(mapping).toEqual({
      'claude-*': 'claude-*',
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
      'claude-*': 'claude-*',
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
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }],
    })
  })
})
