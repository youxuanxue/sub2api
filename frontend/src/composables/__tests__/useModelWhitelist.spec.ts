import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  getModelMappingPresets: vi.fn()
}))

import { buildModelMappingObject, claudeModels, getModelsByPlatform, getPresetMappingsByPlatform, splitModelMappingObject } from '../useModelWhitelist'
import { BUILTIN_PLATFORM_CATALOG, resetPlatformCatalog, setPlatformCatalog } from '@/constants/platformCatalog'
import { apiBackedPlatforms } from '../useServableModels'

async function loadApiModels(platform: string, models: string[]): Promise<string[]> {
  const { getModelMappingPresets } = await import('@/api/admin/accounts')
  const { useServableModels } = await import('../useServableModels')
  const { getModelsByPlatform: getModels } = await import('../useModelWhitelist')
  vi.mocked(getModelMappingPresets).mockResolvedValue(models)
  await useServableModels().ensureLoaded(platform)
  return getModels(platform)
}

describe('useModelWhitelist', () => {
  it('平台清单中没有内置模型列表的多协议供应商不预填白名单', () => {
    setPlatformCatalog({
      ...BUILTIN_PLATFORM_CATALOG,
      platforms: [
        ...BUILTIN_PLATFORM_CATALOG.platforms,
        {
          id: 'acme_router',
          display_name: 'Acme Router',
          gateway: 'openai',
          cn_provider: false,
          multi_protocol: {
            default_mode: 'standard',
            routing: 'by_model',
            modes: [{ mode: 'standard', base_urls: { chat_completions: 'https://api.acme-router.example/v1' } }]
          }
        }
      ]
    })
    try {
      expect(getModelsByPlatform('acme_router')).toEqual([])
      expect(getModelsByPlatform('kimi').length).toBeGreaterThan(0)
      // 未知非多协议平台仍回退静态 Claude 列表；anthropic 本身已改走 API cache，不能再互相对齐。
      expect(getModelsByPlatform('bedrock')).toEqual(claudeModels)
    } finally {
      resetPlatformCatalog()
    }
  })

  it('openai 模型列表透传后端 GPT-5.4/GPT-6 官方快照', async () => {
    const models = await loadApiModels('openai', [
      'gpt-5.4', 'gpt-5.4-mini', 'gpt-5.4-2026-03-05', 'codex-auto-review',
      'gpt-5.6', 'gpt-6', 'gpt-6-astra', 'gpt-6.1-sol', 'gpt-6-sol', 'gpt-6-luna'
    ])

    expect(models).toContain('gpt-5.4')
    expect(models).toContain('gpt-5.4-mini')
    expect(models).toContain('gpt-5.4-2026-03-05')
    expect(models).toContain('codex-auto-review')
    expect(models).toContain('gpt-5.6')
    expect(models).toContain('gpt-6')
    expect(models).toContain('gpt-6-astra')
    expect(models).toContain('gpt-6.1-sol')
    expect(models).toContain('gpt-6-sol')
    expect(models).toContain('gpt-6-luna')
  })

  it('exposes one GPT-6.1 Sol mapping action with the TokenKey preset', () => {
    const presets = getPresetMappingsByPlatform('openai')
    expect(presets.filter(preset => preset.from === 'gpt-6.1-sol')).toEqual([
      expect.objectContaining({ label: 'GPT-6.1 Sol', from: 'gpt-6.1-sol', to: 'gpt-6.1-sol', color: expect.stringContaining('bg-rose-100') })
    ])
    expect(new Set(presets.map(preset => `${preset.from}:${preset.to}`)).size).toBe(presets.length)
  })

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

  it('Claude 默认白名单保存后允许 Haiku 5.5，预设使用同一模型 ID', async () => {
    const models = await loadApiModels('anthropic', ['claude-haiku-5-5', 'claude-sonnet-4-5-20250929'])
    const mapping: Record<string, string> | null = buildModelMappingObject('whitelist', models, [])

    expect(mapping?.['claude-haiku-5-5']).toBe('claude-haiku-5-5')
    expect(getPresetMappingsByPlatform('anthropic')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'Haiku 5.5', from: 'claude-haiku-5-5', to: 'claude-haiku-5-5' })
    ]))
  })

  it('xAI 模型列表透传后端 Grok 4.5 官方模型和别名', async () => {
    const models = await loadApiModels('grok', [
      'grok-4.6', 'grok-4.6-latest', 'grok-4.5', 'grok-4.5-latest',
      'grok-build-latest', 'grok-imagine-image-2.0', 'grok-imagine-video-1.5'
    ])

    expect(models).toContain('grok-4.6')
    expect(models).toContain('grok-4.6-latest')
    expect(models).toContain('grok-4.5')
    expect(models).toContain('grok-4.5-latest')
    expect(models).toContain('grok-build-latest')
    expect(models).toContain('grok-imagine-image-2.0')
    expect(models).toContain('grok-imagine-video-1.5')
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

  it('grok 模型列表透传后端 Composer 默认项和兼容别名', async () => {
    const models = await loadApiModels('grok', [
      'grok-composer-2.5-fast', 'composer-2.5', 'grok-4.5'
    ])

    expect(models).toContain('grok-composer-2.5-fast')
    expect(models).not.toContain('grok-composer')
    expect(models).toContain('composer-2.5')
  })

  it('gemini 模型列表透传后端原生生图模型并保留顺序', async () => {
    const models = await loadApiModels('gemini', [
      'gemini-3.1-flash-image', 'gemini-2.5-flash-image', 'gemini-2.5-flash', 'gemini-2.0-flash'
    ])

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.0-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
  })

  it('antigravity 模型列表透传后端排序（新 Gemini 图片模型靠前）', async () => {
    const models = await loadApiModels('antigravity', [
      'gemini-3.1-flash-image', 'gemini-2.5-flash-image', 'gemini-2.5-flash', 'gemini-2.5-flash-lite'
    ])

    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash-lite'))
  })

  it('antigravity 模型列表透传后端 Gemini 3.1 Pro 通用别名', async () => {
    const models = await loadApiModels('antigravity', ['gemini-3.1-pro', 'gemini-2.5-flash'])

    expect(models).toContain('gemini-3.1-pro')
  })

  it('Gemini 4 Argon 在 gemini 与 antigravity 平台使用对应真实模型 ID 与预设映射', async () => {
    const geminiModels = await loadApiModels('gemini', ['gemini-4-argon', 'gemini-2.5-flash'])
    expect(geminiModels).toContain('gemini-4-argon')

    const agModels = await loadApiModels('antigravity', [
      'gemini-4-argon-high', 'gemini-4-argon-medium', 'gemini-4-argon-low'
    ])
    expect(agModels).not.toContain('gemini-4-argon')
    expect(agModels).toContain('gemini-4-argon-high')
    expect(agModels).toContain('gemini-4-argon-medium')
    expect(agModels).toContain('gemini-4-argon-low')
    // TK 收敛面：Argon 别名一键落到 high；tier 后缀本身由模型列表承载，不再单独做 identity preset。
    expect(getPresetMappingsByPlatform('antigravity')).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 'gemini-4-argon', to: 'gemini-4-argon-high' })
    ]))
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

  it('combined mode retains a mapping when a whitelist entry has the same source', () => {
    expect(buildModelMappingObject('combined', ['gpt-latest'], [{ from: 'gpt-latest', to: 'deepseek-chat' }])).toEqual({
      'gpt-latest': 'deepseek-chat'
    })
  })

  it('split mapping keeps only identity entries in the whitelist after reopening', () => {
    expect(splitModelMappingObject({ 'gpt-latest': 'deepseek-chat', 'gpt-5.4': 'gpt-5.4' })).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'deepseek-chat' }]
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
