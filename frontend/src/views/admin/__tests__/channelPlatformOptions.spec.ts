import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { listPlatformIds } from '@/constants/platformCatalog'
import type { Channel } from '@/api/admin/channels'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { apiToFormSections, formSectionsToApi } from '@/utils/channelFormConversion'

const cnConcretePlatforms = ['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'] as const

describe('Composite channel platform options', () => {
  it('includes the CN concrete providers for pricing and model mapping', () => {
    const source = readFileSync(resolve('src/views/admin/ChannelsView.vue'), 'utf8')
    // 平台列表来自平台清单（后端 domain/platforms.go），组合分组覆盖全部具体平台。
    expect(source).toMatch(/const platformOrder = computed<GroupPlatform\[\]>\(\(\) => listPlatformIds\(\)\)/)
    expect(source).toContain('const compositePlatforms = platformOrder')

    expect(listPlatformIds()).toEqual(
      expect.arrayContaining(['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'])
    )
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(
      expect.arrayContaining([...cnConcretePlatforms])
    )
  })

  // TK：编辑渠道时国产平台的 model_mapping 映射经表单分节往返不丢失。
  it.each(cnConcretePlatforms)('preserves %s mapping when editing a channel', (platform) => {
    const mapping = { 'client-model': 'supplier-model' }
    const channel = { model_mapping: { [platform]: mapping }, model_pricing: [], group_ids: [] } as unknown as Channel
    const sections = apiToFormSections(channel, [])
    expect(sections.find(section => section.platform === platform)?.enabled).toBe(true)
    expect(formSectionsToApi(sections).model_mapping[platform]).toEqual(mapping)
  })
})
