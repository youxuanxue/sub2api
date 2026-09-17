import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Channel } from '@/api/admin/channels'
import { apiToFormSections, formSectionsToApi } from '@/utils/channelFormConversion'

describe('Composite channel platform options', () => {
  it.each(['kimi', 'zhipu', 'deepseek', 'minimax'] as const)('preserves %s mapping when editing a channel', (platform) => {
    const mapping = { 'client-model': 'supplier-model' }
    const channel = { model_mapping: { [platform]: mapping }, model_pricing: [], group_ids: [] } as unknown as Channel
    const sections = apiToFormSections(channel, [])
    expect(sections.find(section => section.platform === platform)?.enabled).toBe(true)
    expect(formSectionsToApi(sections).model_mapping[platform]).toEqual(mapping)
  })

  it('includes the CN concrete providers for pricing and model mapping', () => {
    const source = readFileSync(resolve('src/views/admin/ChannelsView.vue'), 'utf8')
    const declaration = source.match(/const compositePlatforms:[^=]+=[^
]+/)?.[0]

    expect(declaration).toContain("'kimi'")
    expect(declaration).toContain("'zhipu'")
    expect(declaration).toContain("'deepseek'")
    expect(declaration).toContain("'minimax'")
    expect(declaration).toContain("'opencode_go'")
  })
})
